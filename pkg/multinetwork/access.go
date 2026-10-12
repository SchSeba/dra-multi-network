// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	nri "github.com/containerd/nri/pkg/api"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/dynamic-resource-allocation/resourceclaim"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

// Access comes from the actual container's claim/request references, never its
// annotations. Create sees kubelet's CDI edits; Start checks the final OCI state
// again after other NRI plugins have run. Synchronize rechecks recovered runtime
// state before reporting the agent ready. Native state is also verified.
func containerAccessRoots(p Preparation, pod corev1.Pod, container *nri.Container) (map[string]bool, error) {
	if container == nil {
		return nil, fmt.Errorf("E_CONTAINER_IDENTITY: missing container")
	}
	var resources *corev1.ResourceRequirements
	var security *corev1.SecurityContext
	for _, containers := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, c := range containers {
			if c.Name == container.Name {
				resources, security = &c.Resources, c.SecurityContext
			}
		}
	}
	for _, c := range pod.Spec.EphemeralContainers {
		if c.Name == container.Name {
			resources, security = &c.Resources, c.SecurityContext
		}
	}
	if resources == nil {
		return nil, fmt.Errorf("E_CONTAINER_IDENTITY: unknown container %s", container.Name)
	}
	if security != nil && security.Privileged != nil && *security.Privileged {
		return nil, fmt.Errorf("E_DEVICE_ACCESS_SCOPE: privileged containers bypass scoped device access")
	}
	claimNames := map[string]bool{}
	for _, ref := range pod.Spec.ResourceClaims {
		name, _, err := resourceclaim.Name(&pod, &ref)
		if err != nil {
			return nil, err
		}
		if name != nil && *name == p.Claim.Name {
			claimNames[ref.Name] = true
		}
	}
	if pod.Namespace != p.Claim.Namespace || len(claimNames) != 1 {
		return nil, fmt.Errorf("E_DEVICE_ACCESS_CLAIM")
	}
	roots := map[string]bool{}
	for _, ref := range resources.Claims {
		if !claimNames[ref.Name] {
			continue
		}
		found := false
		for _, root := range p.Roots {
			if ref.Request == "" || ref.Request == root.Result.Request {
				roots[root.Step], found = true, true
			}
		}
		if !found {
			return nil, fmt.Errorf("E_DEVICE_ACCESS_REQUEST: %s", ref.Request)
		}
	}
	return roots, nil
}

func validateContainerAccess(p Preparation, pod corev1.Pod, container *nri.Container) error {
	roots, err := containerAccessRoots(p, pod, container)
	if err != nil {
		return err
	}
	allowed := map[string]effects.DeviceNode{}
	protected := map[[2]int64]bool{}
	privateMajors := map[int64]bool{}
	for output, h := range p.Hardware.Outputs {
		root, _, _ := strings.Cut(output, ".")
		for _, node := range h.Devices {
			protected[[2]int64{node.Major, node.Minor}] = true
			// The common control devices share a major with unrelated devices.
			if node.Path != "/dev/vfio/vfio" && node.Path != "/dev/infiniband/rdma_cm" {
				privateMajors[node.Major] = true
			}
			if roots[root] {
				allowed[node.Path] = node
			}
		}
	}
	seen := map[string]bool{}
	for _, node := range container.GetLinux().GetDevices() {
		if node == nil {
			return fmt.Errorf("E_DEVICE_ACCESS_NODE: missing device")
		}
		if accessPath(node.Path) || protected[[2]int64{node.Major, node.Minor}] || privateMajors[node.Major] {
			grant, ok := allowed[node.Path]
			if !ok || node.Type != "c" || node.Major != grant.Major || node.Minor != grant.Minor || seen[node.Path] {
				return fmt.Errorf("E_DEVICE_ACCESS_NODE: %s", node.Path)
			}
			seen[node.Path] = true
		}
	}
	for path := range allowed {
		if !seen[path] {
			return fmt.Errorf("E_DEVICE_ACCESS_MISSING: %s", path)
		}
	}
	ids := map[string]bool{}
	for _, root := range p.Roots {
		if roots[root.Step] {
			for _, id := range root.CDIDeviceIDs {
				ids[id] = true
			}
		}
	}
	for _, device := range container.CDIDevices {
		if device == nil {
			return fmt.Errorf("E_DEVICE_ACCESS_CDI: missing device")
		}
		if strings.HasPrefix(device.Name, api.Driver+"/") && !ids[device.Name] {
			return fmt.Errorf("E_DEVICE_ACCESS_CDI: %s", device.Name)
		}
	}
	for _, mount := range container.Mounts {
		if mount == nil {
			return fmt.Errorf("E_DEVICE_ACCESS_MOUNT: missing mount")
		}
		source := filepath.Clean(mount.Source)
		if (mount.Type == "bind" || slices.Contains(mount.Options, "bind") || slices.Contains(mount.Options, "rbind")) && (source == "/" || source == "/dev" || accessPath(source)) {
			return fmt.Errorf("E_DEVICE_ACCESS_MOUNT: access must use recorded CDI device nodes")
		}
	}
	readWrite := map[string]uint8{}
	for _, rule := range container.GetLinux().GetResources().GetDevices() {
		if rule == nil {
			return fmt.Errorf("E_DEVICE_ACCESS_CGROUP: missing rule")
		}
		if !strings.ContainsAny(rule.Access, "rw") || rule.Type == "b" {
			continue
		}
		wildMajor := rule.Major == nil || rule.Major.GetValue() < 0
		wildMinor := rule.Minor == nil || rule.Minor.GetValue() < 0
		var mask uint8
		if strings.Contains(rule.Access, "r") {
			mask |= 1
		}
		if strings.Contains(rule.Access, "w") {
			mask |= 2
		}
		for path, grant := range allowed {
			if (wildMajor || rule.Major.GetValue() == grant.Major) && (wildMinor || rule.Minor.GetValue() == grant.Minor) {
				if rule.Allow {
					readWrite[path] |= mask
				} else {
					readWrite[path] &^= mask
				}
			}
		}
		if !rule.Allow {
			continue
		}
		relevant := wildMajor || privateMajors[rule.Major.GetValue()]
		for pair := range protected {
			relevant = relevant || (rule.Major.GetValue() == pair[0] && (wildMinor || rule.Minor.GetValue() == pair[1]))
		}
		if !relevant {
			continue
		}
		if rule.Type != "c" || wildMajor || wildMinor {
			return fmt.Errorf("E_DEVICE_ACCESS_CGROUP: wildcard access is outside the grant")
		}
		matched := false
		for _, grant := range allowed {
			if rule.Major.GetValue() == grant.Major && rule.Minor.GetValue() == grant.Minor {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("E_DEVICE_ACCESS_CGROUP: device is outside the container's requests")
		}
	}
	for path := range allowed {
		if readWrite[path] != 3 {
			return fmt.Errorf("E_DEVICE_ACCESS_CGROUP: missing read/write grant for %s", path)
		}
	}
	return nil
}

func accessPath(path string) bool {
	path = filepath.Clean(path)
	return path == "/dev/vfio" || strings.HasPrefix(path, "/dev/vfio/") || path == "/dev/infiniband" || strings.HasPrefix(path, "/dev/infiniband/")
}

// A namespace move recreates RDMA clients. NRI grants only the post-move nodes
// acknowledged by the journal; Start and recovery validate the final OCI state.
func adjustRDMAAccess(p Preparation, pod corev1.Pod, container *nri.Container) (*nri.ContainerAdjustment, error) {
	roots, err := containerAccessRoots(p, pod, container)
	if err != nil {
		return nil, err
	}
	planned := proto.Clone(container).(*nri.Container)
	if planned.Linux == nil {
		planned.Linux = &nri.LinuxContainer{}
	}
	if planned.Linux.Resources == nil {
		planned.Linux.Resources = &nri.LinuxResources{}
	}
	adjustment := &nri.ContainerAdjustment{}
	for _, output := range slices.Sorted(maps.Keys(p.Hardware.Outputs)) {
		h := p.Hardware.Outputs[output]
		root, _, _ := strings.Cut(output, ".")
		if h.Type != topology.RDMAType || !roots[root] {
			continue
		}
		for _, node := range h.Devices {
			exists := false
			for _, device := range planned.Linux.Devices {
				if device != nil && device.Path == node.Path {
					exists = true
				}
			}
			if exists {
				continue
			} // Verify existing device identity/permissions below.
			mode := os.FileMode(0600)
			device := &nri.LinuxDevice{Path: node.Path, Type: "c", Major: node.Major, Minor: node.Minor, FileMode: nri.FileMode(&mode)}
			if user := container.GetUser(); user != nil {
				device.Uid = nri.UInt32(user.Uid)
				device.Gid = nri.UInt32(user.Gid)
			}
			adjustment.AddDevice(device)
			planned.Linux.Devices = append(planned.Linux.Devices, device)
			rule := &nri.LinuxDeviceCgroup{Allow: true, Type: "c", Major: nri.Int64(node.Major), Minor: nri.Int64(node.Minor), Access: "rw"}
			if adjustment.Linux.Resources == nil {
				adjustment.Linux.Resources = &nri.LinuxResources{}
			}
			adjustment.Linux.Resources.Devices = append(adjustment.Linux.Resources.Devices, rule)
			planned.Linux.Resources.Devices = append(planned.Linux.Resources.Devices, rule)
		}
	}
	if err := validateContainerAccess(p, pod, planned); err != nil {
		return nil, err
	}
	if adjustment.Linux == nil {
		return nil, nil
	}
	return adjustment, nil
}

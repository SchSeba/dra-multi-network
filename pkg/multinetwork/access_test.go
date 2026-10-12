// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"strings"
	"testing"

	nri "github.com/containerd/nri/pkg/api"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
)

func TestContainerAccessUsesActualClaimRequestsAndFinalDevices(t *testing.T) {
	control := effects.DeviceNode{Path: "/dev/vfio/vfio", Major: 10, Minor: 196}
	left := effects.DeviceNode{Path: "/dev/vfio/30", Major: 238, Minor: 0}
	right := effects.DeviceNode{Path: "/dev/vfio/26", Major: 238, Minor: 1}
	leftID, rightID := api.Driver+"/vf=claim-left", api.Driver+"/vf=claim-right"
	p := Preparation{Claim: resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "allocated", Namespace: "test"}}, Roots: []Root{{Step: "left", Result: resourceapi.DeviceRequestAllocationResult{Request: "requested-left"}, CDIDeviceIDs: []string{leftID}}, {Step: "right", Result: resourceapi.DeviceRequestAllocationResult{Request: "requested-right"}, CDIDeviceIDs: []string{rightID}}}, Hardware: effects.Execution{Outputs: map[string]effects.Handle{"left.vfio": {Devices: []effects.DeviceNode{control, left}}, "right.vfio": {Devices: []effects.DeviceNode{control, right}}}}}
	claims := func(request string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Claims: []corev1.ResourceClaim{{Name: "network", Request: request}}}
	}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "test"}, Spec: corev1.PodSpec{
		ResourceClaims:      []corev1.PodResourceClaim{{Name: "network", ResourceClaimName: ptr.To("allocated")}},
		InitContainers:      []corev1.Container{{Name: "init", Resources: claims("requested-left")}},
		Containers:          []corev1.Container{{Name: "left", Resources: claims("requested-left")}, {Name: "right", Resources: claims("requested-right")}, {Name: "all", Resources: claims("")}, {Name: "sidecar"}},
		EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}},
	}}
	container := func(name string, nodes ...effects.DeviceNode) *nri.Container {
		c := &nri.Container{Name: name, Linux: &nri.LinuxContainer{Resources: &nri.LinuxResources{Devices: []*nri.LinuxDeviceCgroup{{Allow: true, Type: "c", Access: "m"}}}}}
		for _, node := range nodes {
			c.Linux.Devices = append(c.Linux.Devices, &nri.LinuxDevice{Path: node.Path, Type: "c", Major: node.Major, Minor: node.Minor})
			c.Linux.Resources.Devices = append(c.Linux.Resources.Devices, &nri.LinuxDeviceCgroup{Allow: true, Type: "c", Major: nri.Int64(node.Major), Minor: nri.Int64(node.Minor), Access: "rw"})
		}
		return c
	}
	for _, c := range []*nri.Container{container("init", control, left), container("left", control, left), container("right", control, right), container("all", control, left, right), container("sidecar"), container("debug")} {
		if err := validateContainerAccess(p, pod, c); err != nil {
			t.Fatalf("valid %s rejected: %v", c.Name, err)
		}
	}
	generated := *pod.DeepCopy()
	generated.Spec.ResourceClaims[0].ResourceClaimName = nil
	generated.Spec.ResourceClaims[0].ResourceClaimTemplateName = ptr.To("template")
	generated.Status.ResourceClaimStatuses = []corev1.PodResourceClaimStatus{{Name: "network", ResourceClaimName: ptr.To("allocated")}}
	if err := validateContainerAccess(p, generated, container("left", control, left)); err != nil {
		t.Fatalf("generated claim rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.Pod, *nri.Container)
		reason string
	}{
		{"wrong request", func(_ *corev1.Pod, c *nri.Container) {
			c.Linux.Devices[1].Path = right.Path
			c.Linux.Devices[1].Minor = right.Minor
		}, "E_DEVICE_ACCESS_NODE"},
		{"undeclared sidecar", func(_ *corev1.Pod, c *nri.Container) { c.Name = "sidecar" }, "E_DEVICE_ACCESS_NODE"},
		{"ephemeral injection", func(_ *corev1.Pod, c *nri.Container) { c.Name = "debug" }, "E_DEVICE_ACCESS_NODE"},
		{"alias", func(_ *corev1.Pod, c *nri.Container) { c.Linux.Devices[1].Path = "/dev/hidden" }, "E_DEVICE_ACCESS_NODE"},
		{"foreign group alias", func(_ *corev1.Pod, c *nri.Container) {
			c.Linux.Devices = append(c.Linux.Devices, &nri.LinuxDevice{Path: "/dev/hidden", Type: "c", Major: 238, Minor: 99})
		}, "E_DEVICE_ACCESS_NODE"},
		{"wrong major", func(_ *corev1.Pod, c *nri.Container) { c.Linux.Devices[1].Major++ }, "E_DEVICE_ACCESS_NODE"},
		{"missing device", func(_ *corev1.Pod, c *nri.Container) { c.Linux.Devices = c.Linux.Devices[:1] }, "E_DEVICE_ACCESS_MISSING"},
		{"foreign CDI", func(_ *corev1.Pod, c *nri.Container) { c.CDIDevices = []*nri.CDIDevice{{Name: rightID}} }, "E_DEVICE_ACCESS_CDI"},
		{"unknown container", func(_ *corev1.Pod, c *nri.Container) { c.Name = "absent" }, "E_CONTAINER_IDENTITY"},
		{"unknown request", func(p *corev1.Pod, _ *nri.Container) { p.Spec.Containers[0].Resources.Claims[0].Request = "missing" }, "E_DEVICE_ACCESS_REQUEST"},
		{"privileged", func(p *corev1.Pod, _ *nri.Container) {
			p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(true)}
		}, "E_DEVICE_ACCESS_SCOPE"},
		{"host device mount", func(_ *corev1.Pod, c *nri.Container) {
			c.Mounts = []*nri.Mount{{Source: "/dev/vfio", Destination: "/hostdev", Options: []string{"rbind"}}}
		}, "E_DEVICE_ACCESS_MOUNT"},
		{"wildcard cgroup", func(_ *corev1.Pod, c *nri.Container) {
			c.Linux.Resources.Devices = append(c.Linux.Resources.Devices, &nri.LinuxDeviceCgroup{Allow: true, Type: "c", Access: "rw"})
		}, "E_DEVICE_ACCESS_CGROUP"},
		{"negative wildcard cgroup", func(_ *corev1.Pod, c *nri.Container) {
			c.Linux.Resources.Devices = append(c.Linux.Resources.Devices, &nri.LinuxDeviceCgroup{Allow: true, Type: "c", Major: nri.Int64(int64(-1)), Minor: nri.Int64(int64(-1)), Access: "rw"})
		}, "E_DEVICE_ACCESS_CGROUP"},
		{"revoked cgroup grant", func(_ *corev1.Pod, c *nri.Container) {
			c.Linux.Resources.Devices = append(c.Linux.Resources.Devices, &nri.LinuxDeviceCgroup{Allow: false, Type: "c", Major: nri.Int64(int64(238)), Minor: nri.Int64(int64(0)), Access: "w"})
		}, "E_DEVICE_ACCESS_CGROUP"},
		{"other request cgroup", func(_ *corev1.Pod, c *nri.Container) {
			c.Linux.Resources.Devices = append(c.Linux.Resources.Devices, &nri.LinuxDeviceCgroup{Allow: true, Type: "c", Major: nri.Int64(238), Minor: nri.Int64(1), Access: "rw"})
		}, "E_DEVICE_ACCESS_CGROUP"},
		{"missing cgroup", func(_ *corev1.Pod, c *nri.Container) { c.Linux.Resources.Devices = nil }, "E_DEVICE_ACCESS_CGROUP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, c := pod.DeepCopy(), container("left", control, left)
			tc.mutate(actual, c)
			if err := validateContainerAccess(p, *actual, c); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s, got %v", tc.reason, err)
			}
		})
	}
}

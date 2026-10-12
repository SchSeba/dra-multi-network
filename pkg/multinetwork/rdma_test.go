// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"strings"
	"testing"

	nri "github.com/containerd/nri/pkg/api"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func rdmaAccessFixture() (Preparation, Attachment, corev1.Pod) {
	ns := effects.NamespaceIdentity{Device: 1, Inode: 2}
	original := effects.RDMADevice{Index: 7, Name: "mlx5_0", PCI: "0000:29:10.0", NodeGUID: "node", SystemImageGUID: "system"}
	p := Preparation{Claim: resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "allocated", Namespace: "test", UID: "claim"}}, Fingerprint: "fingerprint", NodeUID: "node", BootID: "boot", Revision: api.NetworkTopologyRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision", UID: "revision"}, Spec: api.RevisionSpec{Digest: "digest"}}, Roots: []Root{{Step: "root", Result: resourceapi.DeviceRequestAllocationResult{Request: "rdma"}, Persona: Persona{Inventory: Inventory{PCI: original.PCI, RDMA: []effects.RDMADevice{original}}, Policy: api.DeviceExposurePolicy{Spec: api.PolicySpec{Exposure: &api.Exposure{Mode: topology.RDMAMode}}}}}}}
	p.Revision.Spec.Topology.Steps = []api.Step{{ID: "root", Kind: topology.AllocationKind, PluginRef: api.PluginRef{Name: "host-cni-roce"}, Config: ext.JSON{Raw: []byte(`{"mode":"rdma","rdmaProfile":"roce-single-port-v1"}`)}}}
	p.Revision.Spec.Plugins = []api.LockedPlugin{{Name: "host-cni-roce", Spec: topology.PluginRegistry(nil)["host-cni-roce"].Spec}}
	original.Namespace = &ns
	at := Attachment{Spec: api.AttachmentSpec{ClaimUID: "claim", Fingerprint: "fingerprint", NodeUID: "node", BootID: "boot", RevisionRef: api.RevisionRef{UID: "revision", Digest: "digest"}}, Phase: Ready, Pin: "pin", SandboxNS: ns, Execution: effects.Execution{Outputs: map[string]effects.Handle{"root.rdma": {Type: topology.RDMAType, Scope: effects.SandboxScope, PCIAddress: original.PCI, Name: original.Name, RDMA: &original, NamespacePath: "pin", Devices: []effects.DeviceNode{{Path: "/dev/infiniband/uverbs9", Major: 231, Minor: 9}, {Path: "/dev/infiniband/rdma_cm", Major: 10, Minor: 134}}}}}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "test"}, Spec: corev1.PodSpec{ResourceClaims: []corev1.PodResourceClaim{{Name: "network", ResourceClaimName: ptr.To("allocated")}}, Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Claims: []corev1.ResourceClaim{{Name: "network", Request: "rdma"}}}}, {Name: "sidecar"}}}}
	return p, at, pod
}

func TestRDMAAccessUsesPostMoveAcknowledgementAndActualRequests(t *testing.T) {
	p, at, pod := rdmaAccessFixture()
	access, err := runtimeDevicePreparation(p, at)
	if err != nil {
		t.Fatal(err)
	}
	c := &nri.Container{Name: "app", User: &nri.User{Uid: 1000, Gid: 1000}, Linux: &nri.LinuxContainer{}}
	adjustment, err := adjustRDMAAccess(access, pod, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(adjustment.GetLinux().GetDevices()) != 2 || len(adjustment.Linux.Resources.Devices) != 2 || len(c.Linux.Devices) != 0 || len(p.Hardware.Outputs) != 0 {
		t.Fatalf("grant mutated its authority/input or omitted nodes: %+v", adjustment)
	}
	for _, d := range adjustment.Linux.Devices {
		if d.Uid.GetValue() != 1000 || d.Gid.GetValue() != 1000 || d.FileMode.GetValue() != 0600 {
			t.Fatalf("device ownership differs: %+v", d)
		}
	}
	final := &nri.Container{Name: "app", Linux: &nri.LinuxContainer{Devices: adjustment.Linux.Devices, Resources: adjustment.Linux.Resources}}
	if err := validateContainerAccess(access, pod, final); err != nil {
		t.Fatal(err)
	}
	if next, err := adjustRDMAAccess(access, pod, final); err != nil || next != nil {
		t.Fatalf("idempotent creation differs: %+v %v", next, err)
	}
	if next, err := adjustRDMAAccess(access, pod, &nri.Container{Name: "sidecar", Linux: &nri.LinuxContainer{}}); err != nil || next != nil {
		t.Fatalf("sidecar received a grant: %+v %v", next, err)
	}
	final.Name = "sidecar"
	if err := validateContainerAccess(access, pod, final); err == nil {
		t.Fatal("unrequested sidecar accepted RDMA nodes")
	}
	for _, node := range []*nri.LinuxDevice{{Path: "/dev/foreign", Type: "c", Major: 231, Minor: 8}, {Path: "/dev/hidden-cm", Type: "c", Major: 10, Minor: 134}} {
		bad := &nri.Container{Name: "app", Linux: &nri.LinuxContainer{Devices: []*nri.LinuxDevice{node}}}
		if _, err := adjustRDMAAccess(access, pod, bad); err == nil || !strings.Contains(err.Error(), "E_DEVICE_ACCESS_NODE") {
			t.Fatalf("foreign alias was repaired/accepted: %v", err)
		}
	}
	bad := &nri.Container{Name: "app", Linux: &nri.LinuxContainer{Resources: &nri.LinuxResources{Devices: []*nri.LinuxDeviceCgroup{{Allow: true, Type: "c", Major: nri.Int64(10), Minor: nri.Int64(134), Access: "rw"}}}}}
	bad.Name = "sidecar"
	if _, err := adjustRDMAAccess(access, pod, bad); err == nil {
		t.Fatal("CM cgroup grant without a request accepted")
	}
	if _, err := adjustRDMAAccess(access, pod, nil); err == nil {
		t.Fatal("missing container accepted")
	}
}

func TestRDMAAccessRequiresMatchingReadyAttachment(t *testing.T) {
	for _, change := range []func(*Attachment){
		func(a *Attachment) { a.Phase = Removing }, func(a *Attachment) { a.Spec.Fingerprint = "other" }, func(a *Attachment) { a.Spec.NodeUID = "other" }, func(a *Attachment) { a.Spec.ClaimUID = "other" }, func(a *Attachment) { a.Spec.RevisionRef.Digest = "other" }, func(a *Attachment) { a.Spec.BootID = "other" },
		func(a *Attachment) { a.SandboxNS.Inode++ }, func(a *Attachment) { a.Pin = "other" }, func(a *Attachment) { a.Execution.Outputs = map[string]effects.Handle{} },
		func(a *Attachment) {
			h := a.Execution.Outputs["root.rdma"]
			h.RDMA.NodeGUID = "other"
			a.Execution.Outputs["root.rdma"] = h
		},
	} {
		p, at, _ := rdmaAccessFixture()
		change(&at)
		if _, err := runtimeDevicePreparation(p, at); err == nil {
			t.Fatalf("accepted unrelated/unready attachment: %+v", at)
		}
	}
}

func TestMADAccessRequiresRequestedIBProfileAndKeepsSidecarUnprivileged(t *testing.T) {
	p, at, pod := rdmaAccessFixture()
	h := at.Execution.Outputs["root.rdma"]
	h.MADAccess = true
	h.Devices = append(h.Devices, effects.DeviceNode{Path: "/dev/infiniband/umad9", Major: 231, Minor: 1})
	at.Execution.Outputs["root.rdma"] = h
	if _, err := runtimeDevicePreparation(p, at); err == nil {
		t.Fatal("RoCE acquired UMAD access")
	}
	p.Revision.Spec.Topology.Steps[0].Config.Raw = []byte(`{"mode":"rdma","rdmaProfile":"ib-ipoib-single-port-v1","madAccess":true}`)
	if _, err := runtimeDevicePreparation(p, at); err == nil {
		t.Fatal("ungranted MAD request accepted")
	}
	p.Roots[0].Persona.Policy.Spec.Exposure.AllowedMADAccess = true
	access, err := runtimeDevicePreparation(p, at)
	if err != nil {
		t.Fatal(err)
	}
	adjustment, err := adjustRDMAAccess(access, pod, &nri.Container{Name: "app", User: &nri.User{Uid: 1000, Gid: 1000}, Linux: &nri.LinuxContainer{}})
	if err != nil || len(adjustment.GetLinux().GetDevices()) != 3 {
		t.Fatal("request-scoped UMAD omitted", err)
	}
	if adjustment, err := adjustRDMAAccess(access, pod, &nri.Container{Name: "sidecar", Linux: &nri.LinuxContainer{}}); err != nil || adjustment != nil {
		t.Fatal("sidecar received MAD", err)
	}
	p.Revision.Spec.Topology.Steps[0].Config.Raw = []byte(`{"mode":"rdma","rdmaProfile":"ib-ipoib-single-port-v1","madAccess":false}`)
	if _, err := runtimeDevicePreparation(p, at); err == nil {
		t.Fatal("policy allowance substituted for the actual MAD request")
	}
}

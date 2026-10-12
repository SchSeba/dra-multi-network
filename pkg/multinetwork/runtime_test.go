// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nri "github.com/containerd/nri/pkg/api"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/journal"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestCleanupUsesSavedPreparationContract(t *testing.T) {
	data, err := os.ReadFile("../../examples/bond-vlan/topology.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.YAMLToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	var source api.NetworkTopology
	if err := topology.Decode(raw, &source); err != nil {
		t.Fatal(err)
	}
	revision, err := topology.Compile(source, topology.PluginRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := Preparation{Claim: resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: "claim"}}, Fingerprint: "allocation", Revision: api.NetworkTopologyRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision", UID: "revision-uid"}, Spec: revision}}
	if err := store.Put("prepare/claim", p); err != nil {
		t.Fatal(err)
	}
	at := Attachment{Spec: api.AttachmentSpec{ClaimUID: "claim", Fingerprint: p.Fingerprint, RevisionRef: api.RevisionRef{Name: p.Revision.Name, UID: string(p.Revision.UID), Digest: revision.Digest}}, Execution: effects.Execution{ID: "saved-execution"}}
	a := Agent{store: store}
	for _, change := range []func(*Attachment){
		func(v *Attachment) { v.Spec.ClaimUID = "foreign" },
		func(v *Attachment) { v.Spec.Fingerprint = "changed" },
		func(v *Attachment) { v.Spec.RevisionRef.Name = "foreign" },
		func(v *Attachment) { v.Spec.RevisionRef.UID = "foreign" },
		func(v *Attachment) { v.Spec.RevisionRef.Digest = "changed" },
		func(v *Attachment) { v.Execution.ID = "" },
	} {
		foreign := at
		change(&foreign)
		if err := a.restoreAttachment(nil, &foreign, func() error { t.Fatal("foreign cleanup changed journal"); return nil }); err == nil {
			t.Fatal("foreign cleanup reached native restoration")
		}
	}
	// No namespace or runtime binary is needed for an execution with no effects.
	if err := a.restoreAttachment(&effects.Native{}, &at, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestContainerNamespaceMatchesRetainedSandbox(t *testing.T) {
	identity, err := effects.IdentifyNamespace("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	at := Attachment{Spec: api.AttachmentSpec{SandboxID: "sandbox"}, SandboxNS: identity}
	container := func(path string) *nri.Container {
		return &nri.Container{PodSandboxId: "sandbox", Linux: &nri.LinuxContainer{Namespaces: []*nri.LinuxNamespace{{Type: "network", Path: path}}}}
	}
	for _, path := range []string{"/proc/self/ns/net", "/proc/thread-self/ns/net"} {
		if err := verifyContainerNamespace(at, container(path)); err != nil {
			t.Fatalf("same namespace through %s rejected: %v", path, err)
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*Attachment, *nri.Container)
	}{
		{"wrong sandbox", func(_ *Attachment, c *nri.Container) { c.PodSandboxId = "other" }},
		{"no Linux spec", func(_ *Attachment, c *nri.Container) { c.Linux = nil }},
		{"missing network namespace", func(_ *Attachment, c *nri.Container) { c.Linux.Namespaces = nil }},
		{"new network namespace", func(_ *Attachment, c *nri.Container) { c.Linux.Namespaces[0].Path = "" }},
		{"duplicate network namespace", func(_ *Attachment, c *nri.Container) {
			c.Linux.Namespaces = append(c.Linux.Namespaces, c.Linux.Namespaces[0])
		}},
		{"nil namespace", func(_ *Attachment, c *nri.Container) { c.Linux.Namespaces = append(c.Linux.Namespaces, nil) }},
		{"different namespace", func(_ *Attachment, c *nri.Container) { c.Linux.Namespaces[0].Path = "/proc/self/ns/mnt" }},
		{"missing path", func(_ *Attachment, c *nri.Container) { c.Linux.Namespaces[0].Path = "/does-not-exist" }},
		{"ordinary file", func(_ *Attachment, c *nri.Container) { c.Linux.Namespaces[0].Path = "/etc/hosts" }},
		{"missing saved identity", func(a *Attachment, _ *nri.Container) { a.SandboxNS = effects.NamespaceIdentity{} }},
		{"changed saved identity", func(a *Attachment, _ *nri.Container) { a.SandboxNS.Inode++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, c := at, container("/proc/self/ns/net")
			tc.edit(&actual, c)
			if err := verifyContainerNamespace(actual, c); err == nil || !strings.HasPrefix(err.Error(), "E_CONTAINER_") {
				t.Fatalf("invalid container namespace accepted: %v", err)
			}
		})
	}
	if err := verifyContainerNamespace(at, nil); err == nil {
		t.Fatal("nil container accepted")
	}
}

func TestSynchronizationChecksLiveContainerNamespace(t *testing.T) {
	if os.Getenv("DRA_NATIVE_TEST") != "1" {
		t.Skip("requires Linux namespace/mount capabilities; DRA_NATIVE_TEST=1")
	}
	dir := t.TempDir()
	store, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := Attachment{Spec: api.AttachmentSpec{SandboxID: "sandbox", PodUID: "pod", NodeUID: "node", BootID: "boot"}, Phase: Ready, Pin: filepath.Join(dir, "sandbox"), Stage: filepath.Join(dir, "stage")}
	for _, path := range []string{at.Pin, at.Stage} {
		if err := effects.CreateStagingNamespace(path); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := effects.Unpin(path); err != nil {
				t.Error(err)
			}
		})
	}
	at.SandboxNS, err = effects.IdentifyNamespace(at.Pin)
	if err != nil {
		t.Fatal(err)
	}
	at.StageNS, err = effects.IdentifyNamespace(at.Stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put("attach/test", at); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		stopped    bool
		wantError  bool
	}{
		{name: "joined sandbox", path: at.Pin},
		{name: "different network namespace", path: at.Stage, wantError: true},
		{name: "new network namespace", wantError: true},
		{name: "stopped container", path: at.Stage, stopped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Agent{store: store, node: corev1.Node{ObjectMeta: metav1.ObjectMeta{UID: "node"}}, boot: "boot", client: fake.NewClientBuilder().Build(), mutation: make(chan struct{}, 1), syncDone: make(chan struct{})}
			c := &nri.Container{PodSandboxId: "sandbox", State: nri.ContainerState_CONTAINER_RUNNING, Linux: &nri.LinuxContainer{Namespaces: []*nri.LinuxNamespace{{Type: "network", Path: tc.path}}}}
			if tc.stopped {
				c.State = nri.ContainerState_CONTAINER_STOPPED
			}
			_, err := a.Synchronize(context.Background(), []*nri.PodSandbox{{Id: "sandbox", Uid: "pod"}}, []*nri.Container{c})
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "E_CONTAINER_NAMESPACE") || a.synced {
					t.Fatalf("invalid runtime namespace admitted during recovery: %v", err)
				}
			} else if err != nil || !a.synced {
				t.Fatalf("valid runtime state rejected: %v", err)
			}
		})
	}
}

func TestPreparationRecoveryUsesRuntimeQuiescence(t *testing.T) {
	for _, tc := range []struct {
		name, state, want                 string
		allocated, live, unknown, stopped bool
	}{
		{name: "orphan", state: Prepared, want: Released},
		{name: "live orphan", state: Prepared, live: true, want: Prepared},
		{name: "unmapped live container", state: Prepared, unknown: true, want: Prepared},
		{name: "stopped unmapped container", state: Prepared, unknown: true, stopped: true, want: Released},
		{name: "allocated", state: Prepared, allocated: true, want: Prepared},
		{name: "interrupted prepare", state: CleanupPending, allocated: true, want: Preparing},
		{name: "requested release", state: Unpreparing, allocated: true, want: Released},
		{name: "release with live consumer", state: Unpreparing, allocated: true, live: true, want: Unpreparing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := resourceapi.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			store, err := journal.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			claim := resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "network", Namespace: "test", UID: "claim"}, Status: resourceapi.ResourceClaimStatus{Allocation: &resourceapi.AllocationResult{}}}
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tc.allocated {
				builder.WithObjects(&claim)
			}
			var pods []*nri.PodSandbox
			if tc.live {
				pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "test", UID: "pod"}, Spec: corev1.PodSpec{ResourceClaims: []corev1.PodResourceClaim{{Name: "network", ResourceClaimName: ptr.To("network")}}}}
				builder.WithObjects(&pod)
				pods = []*nri.PodSandbox{{Id: "sandbox", Uid: "pod", Name: "consumer", Namespace: "test"}}
			}
			var containers []*nri.Container
			if tc.unknown {
				state := nri.ContainerState_CONTAINER_RUNNING
				if tc.stopped {
					state = nri.ContainerState_CONTAINER_STOPPED
				}
				containers = []*nri.Container{{PodSandboxId: "unknown", State: state}}
			}
			a := Agent{store: store, client: builder.Build(), node: corev1.Node{ObjectMeta: metav1.ObjectMeta{UID: "node"}}, boot: "boot"}
			p := Preparation{Claim: claim, Fingerprint: allocationFingerprint(&claim), State: tc.state, NodeUID: "node", BootID: "boot"}
			if err := store.Put("prepare/claim", p); err != nil {
				t.Fatal(err)
			}
			if err := a.recoverPreparations(context.Background(), pods, containers); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Get("prepare/claim", &p); err != nil {
				t.Fatal(err)
			}
			if p.State != tc.want {
				t.Fatalf("state %s, want %s", p.State, tc.want)
			}
		})
	}
}

func TestPreparationRecoveryRechecksContainerDeviceAccess(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		edit         func(*corev1.Pod, *nri.Container, *resourceapi.ResourceClaim)
	}{
		{name: "valid scoped consumer"},
		{name: "unrequested sidecar", reason: "E_DEVICE_ACCESS_NODE", edit: func(_ *corev1.Pod, c *nri.Container, _ *resourceapi.ResourceClaim) { c.Name = "observer" }},
		{name: "missing device", reason: "E_DEVICE_ACCESS_MISSING", edit: func(_ *corev1.Pod, c *nri.Container, _ *resourceapi.ResourceClaim) { c.Linux.Devices = nil }},
		{name: "wildcard permission", reason: "E_DEVICE_ACCESS_CGROUP", edit: func(_ *corev1.Pod, c *nri.Container, _ *resourceapi.ResourceClaim) {
			c.Linux.Resources.Devices = append(c.Linux.Resources.Devices, &nri.LinuxDeviceCgroup{Allow: true, Type: "c", Access: "rw"})
		}},
		{name: "foreign allocation", reason: "E_PREPARATION_IDENTITY", edit: func(_ *corev1.Pod, _ *nri.Container, claim *resourceapi.ResourceClaim) { claim.Status.Allocation = nil }},
		{name: "foreign reservation", reason: "E_CONSUMER_BINDING", edit: func(_ *corev1.Pod, _ *nri.Container, claim *resourceapi.ResourceClaim) {
			claim.Status.ReservedFor[0].UID = "other"
		}},
		{name: "foreign Pod UID", reason: "E_POD_IDENTITY", edit: func(p *corev1.Pod, _ *nri.Container, _ *resourceapi.ResourceClaim) { p.UID = "other" }},
		{name: "wrong node", reason: "E_POD_IDENTITY", edit: func(p *corev1.Pod, _ *nri.Container, _ *resourceapi.ResourceClaim) { p.Spec.NodeName = "other" }},
		{name: "stopped consumer", edit: func(_ *corev1.Pod, c *nri.Container, _ *resourceapi.ResourceClaim) {
			c.State = nri.ContainerState_CONTAINER_STOPPED
			c.Linux.Devices = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := resourceapi.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			store, err := journal.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			claim := resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "allocated", Namespace: "test", UID: "claim"}, Status: resourceapi.ResourceClaimStatus{Allocation: &resourceapi.AllocationResult{}, ReservedFor: []resourceapi.ResourceClaimConsumerReference{{Resource: "pods", Name: "consumer", UID: "pod"}}}}
			node := effects.DeviceNode{Path: "/dev/vfio/30", Major: 238, Minor: 0}
			p := Preparation{Claim: claim, State: Prepared, Fingerprint: allocationFingerprint(&claim), NodeUID: "node", BootID: "boot", Roots: []Root{{Step: "vf", Result: resourceapi.DeviceRequestAllocationResult{Request: "device"}}}, Hardware: effects.Execution{Outputs: map[string]effects.Handle{"vf.vfio": {Devices: []effects.DeviceNode{node}}}}}
			pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "test", UID: "pod"}, Spec: corev1.PodSpec{NodeName: "node", ResourceClaims: []corev1.PodResourceClaim{{Name: "network", ResourceClaimName: ptr.To("allocated")}}, Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Claims: []corev1.ResourceClaim{{Name: "network", Request: "device"}}}}, {Name: "observer"}}}}
			container := &nri.Container{Name: "app", PodSandboxId: "sandbox", State: nri.ContainerState_CONTAINER_RUNNING, Linux: &nri.LinuxContainer{Devices: []*nri.LinuxDevice{{Path: node.Path, Type: "c", Major: node.Major, Minor: node.Minor}}, Resources: &nri.LinuxResources{Devices: []*nri.LinuxDeviceCgroup{{Allow: true, Type: "c", Major: nri.Int64(node.Major), Minor: nri.Int64(node.Minor), Access: "rw"}}}}}
			if tc.edit != nil {
				tc.edit(&pod, container, &claim)
			}
			if err := store.Put("prepare/claim", p); err != nil {
				t.Fatal(err)
			}
			if err := store.Put("attach/test", Attachment{Spec: api.AttachmentSpec{SandboxID: "sandbox", ClaimUID: "claim"}, Phase: Ready}); err != nil {
				t.Fatal(err)
			}
			a := Agent{store: store, client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(&pod, &claim).Build(), node: corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node"}}, boot: "boot"}
			err = a.recoverPreparations(context.Background(), []*nri.PodSandbox{{Id: "sandbox", Uid: "pod", Name: "consumer", Namespace: "test"}}, []*nri.Container{container})
			if tc.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s, got %v", tc.reason, err)
			}
			var saved Preparation
			if _, err := store.Get("prepare/claim", &saved); err != nil {
				t.Fatal(err)
			}
			if saved.State != Prepared {
				t.Fatalf("device reservation released during recovery: %s", saved.State)
			}
		})
	}
}

func TestUnpreparePersistsRequestWhileAttachmentIsActive(t *testing.T) {
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := Preparation{Claim: resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: "claim"}}, State: Prepared}
	if err := store.Put("prepare/claim", p); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("attach/test", Attachment{Spec: api.AttachmentSpec{ClaimUID: "claim"}, Phase: Ready}); err != nil {
		t.Fatal(err)
	}
	a := Agent{store: store}
	if err := a.unprepare("claim"); err == nil {
		t.Fatal("released an active attachment")
	}
	if _, err := store.Get("prepare/claim", &p); err != nil {
		t.Fatal(err)
	}
	if p.State != Unpreparing {
		t.Fatalf("release intent was lost: %s", p.State)
	}
}

func TestRecoveryBeforeNamespacePinsExist(t *testing.T) {
	if os.Getenv("DRA_NATIVE_TEST") != "1" {
		t.Skip("requires Linux mount capabilities; DRA_NATIVE_TEST=1")
	}
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := Attachment{Spec: api.AttachmentSpec{NodeUID: "node", BootID: "boot", SandboxID: "sandbox"}, Phase: "Connecting", Pin: filepath.Join(t.TempDir(), "missing"), Stage: filepath.Join(t.TempDir(), "missing")}
	if err := store.Put("attach/test", at); err != nil {
		t.Fatal(err)
	}
	a := Agent{store: store, node: corev1.Node{ObjectMeta: metav1.ObjectMeta{UID: "node"}}, boot: "boot", client: fake.NewClientBuilder().Build(), mutation: make(chan struct{}, 1), syncDone: make(chan struct{})}
	if _, err := a.Synchronize(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("attach/test", &at); err != nil {
		t.Fatal(err)
	}
	if at.Phase != Removed || !a.synced {
		t.Fatalf("recovery stalled before the first effect: %s", at.Phase)
	}
}

func TestSandboxStopAllowsUnprepareBeforeCRIRemoval(t *testing.T) {
	if os.Getenv("DRA_NATIVE_TEST") != "1" {
		t.Skip("requires root and Linux namespace/mount capabilities; DRA_NATIVE_TEST=1")
	}
	dir := t.TempDir()
	store, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := Attachment{Spec: api.AttachmentSpec{PodUID: "pod", SandboxID: "sandbox", ClaimUID: "claim"}, Phase: Ready, Pin: filepath.Join(dir, "sandbox"), Stage: filepath.Join(dir, "stage")}
	for _, path := range []string{at.Pin, at.Stage} {
		if err := effects.CreateStagingNamespace(path); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := effects.Unpin(path); err != nil {
				t.Error(err)
			}
		})
	}
	if err := store.Put("attach/test", at); err != nil {
		t.Fatal(err)
	}
	p := Preparation{Claim: resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: "claim"}}, State: Prepared}
	if err := store.Put("prepare/claim", p); err != nil {
		t.Fatal(err)
	}
	a := Agent{store: store, mutation: make(chan struct{}, 1)}
	pod := &nri.PodSandbox{Uid: "pod", Id: "sandbox"}
	if err := a.StopPodSandbox(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if err := a.unprepare("claim"); err != nil {
		t.Fatalf("kubelet cannot release claim before CRI removal: %v", err)
	}
	if err := a.RemovePodSandbox(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(at.Pin); !os.IsNotExist(err) {
		t.Fatalf("namespace retention leaked: %v", err)
	}
}

func TestStatusProjectionSurvivesDeletedNamespacesAndBrokenRecords(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	terminating := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "terminating", DeletionTimestamp: ptr.To(metav1.Now()), Finalizers: []string{"test"}}}
	broken := &api.NetworkAttachmentStatus{ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "live"}, Spec: api.AttachmentSpec{PodUID: "different"}}
	a := Agent{store: store, client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.NetworkAttachmentStatus{}).WithObjects(terminating, broken, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "live"}}).Build()}
	for _, at := range []Attachment{
		{Name: "deleted", Namespace: "deleted", Phase: Removed},
		{Name: "terminating", Namespace: "terminating", Phase: Removed},
		{Name: "broken", Namespace: "live", Phase: Ready},
		{Name: "current", Namespace: "live", Phase: Ready, Spec: api.AttachmentSpec{PodUID: "current"}, Execution: effects.Execution{Outputs: map[string]effects.Handle{"vf.interface": {Name: "vf0"}}}},
	} {
		if err := store.Put("attach/"+at.Name, at); err != nil {
			t.Fatal(err)
		}
	}
	// Repeat to catch map iteration ordering: one broken record must never
	// prevent a current attachment from projecting its committed status.
	for range 4 {
		if err := a.reportStatuses(ctx); err == nil || !strings.Contains(err.Error(), "E_STATUS_IDENTITY") {
			t.Fatalf("identity failure must remain visible: %v", err)
		}
	}
	var current api.NetworkAttachmentStatus
	if err := a.client.Get(ctx, client.ObjectKey{Namespace: "live", Name: "current"}, &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != Ready || len(current.Status.Outputs) != 1 || current.Status.Outputs[0].IfName != "vf0" {
		t.Fatalf("current attachment not projected: %+v", current.Status)
	}
	for _, name := range []string{"deleted", "terminating"} {
		var at Attachment
		if found, err := store.Get("attach/"+name, &at); err != nil || !found || !at.Reported {
			t.Fatalf("removed record in absent/terminating namespace not acknowledged: %+v, %v", at, err)
		}
	}
}

func TestUnrelatedIPAMAllocationIsNotATopologyRoot(t *testing.T) {
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{UID: "claim"}, Status: resourceapi.ResourceClaimStatus{Allocation: &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{Results: []resourceapi.DeviceRequestAllocationResult{{Driver: topology.IPAMDriver, Request: "unrelated", Pool: "external", Device: "subnet"}}}}}}
	agent := Agent{store: store}
	_, err = agent.prepare(context.Background(), claim)
	if err == nil || !strings.Contains(err.Error(), "E_ROOT_COVERAGE: empty") {
		t.Fatal("foreign allocation was treated as a project root", err)
	}
}

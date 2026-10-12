package controller_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/controller"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func testNodeCapabilities(t *testing.T, ctx context.Context, c client.Client, source api.NetworkTopology, revision api.NetworkTopologyRevision) {
	t.Helper()
	const namespace = "capability-test"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "dra-multi-network-node", Namespace: namespace}}); err != nil {
		t.Fatal(err)
	}
	ds := appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: namespace}, Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "agent"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "agent"}}, Spec: corev1.PodSpec{ServiceAccountName: "dra-multi-network-node", Containers: []corev1.Container{{Name: "plugin", Image: "node:1"}}}}}}
	if err := c.Create(ctx, &ds); err != nil {
		t.Fatal(err)
	}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "capable", Labels: map[string]string{"unrelated": "kept"}}}
	if err := c.Create(ctx, &node); err != nil {
		t.Fatal(err)
	}
	node.Status = corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: "boot"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: namespace, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: ds.Name, UID: ds.UID, Controller: ptr.To(true)}}}, Spec: *ds.Spec.Template.Spec.DeepCopy()}
	pod.Spec.NodeName = node.Name
	if err := c.Create(ctx, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "plugin", Ready: true, Image: "node:1", ImageID: "sha256:build", ContainerID: "cri-o://instance", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
	report := topology.NodeCapabilities{Version: topology.ContractVersion, NodeUID: string(node.UID), BootID: "boot", PodName: pod.Name, PodUID: string(pod.UID), ImageID: "sha256:build", ContainerID: "cri-o://instance", Ready: true, Executor: topology.ExecutorContractDigest()}
	for _, locked := range revision.Spec.Plugins {
		report.PluginDigests = append(report.PluginDigests, locked.Digest)
		if locked.Spec.Kind == topology.AllocationKind {
			report.Profiles = append(report.Profiles, topology.HardwareProfile{Contract: topology.ProfileContractDigest(locked.Spec), Hardware: topology.Digest("test-hardware")})
		}
	}
	r := controller.CapabilityReconciler{Client: c, Namespace: namespace}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&node)}
	for _, tc := range []struct {
		name string
		edit func(*topology.NodeCapabilities, *corev1.Node, *corev1.Pod)
		want bool
	}{
		{"supported", func(*topology.NodeCapabilities, *corev1.Node, *corev1.Pod) {}, true},
		{"expired", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) {
			r.Updated = time.Now().Add(-2 * topology.CapabilityTTL)
		}, false},
		{"future timestamp", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) {
			r.Updated = time.Now().Add(time.Minute)
		}, false},
		{"wrong boot", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) { r.BootID = "old" }, false},
		{"wrong node", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) { r.NodeUID = "old" }, false},
		{"wrong Pod", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) { r.PodUID = "old" }, false},
		{"container restart", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) { r.ContainerID = "cri-o://old" }, false},
		{"different build", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) { r.ImageID = "sha256:old" }, false},
		{"unsupported executor", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) { r.Executor = topology.Digest("old") }, false},
		{"missing plugin", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) { r.PluginDigests = nil }, false},
		{"missing hardware profile", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) { r.Profiles = nil }, false},
		{"unreconciled", func(r *topology.NodeCapabilities, _ *corev1.Node, _ *corev1.Pod) { r.Ready = false }, false},
		{"unavailable node", func(_ *topology.NodeCapabilities, n *corev1.Node, _ *corev1.Pod) {
			n.Status.Conditions[0].Status = corev1.ConditionUnknown
		}, false},
		{"unready agent", func(_ *topology.NodeCapabilities, _ *corev1.Node, p *corev1.Pod) {
			p.Status.Conditions[0].Status = corev1.ConditionFalse
		}, false},
		{"unready container", func(_ *topology.NodeCapabilities, _ *corev1.Node, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].Ready = false
		}, false},
		{"wrong owner", func(_ *topology.NodeCapabilities, _ *corev1.Node, p *corev1.Pod) {
			p.OwnerReferences[0].UID = types.UID("old-daemonset")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, p, saved := *node.DeepCopy(), *pod.DeepCopy(), report
			saved.PluginDigests, saved.Profiles = slices.Clone(report.PluginDigests), slices.Clone(report.Profiles)
			saved.Updated = time.Now().UTC()
			tc.edit(&saved, &n, &p)
			var currentNode corev1.Node
			if err := c.Get(ctx, req.NamespacedName, &currentNode); err != nil {
				t.Fatal(err)
			}
			currentNode.Status = n.Status
			if err := c.Status().Update(ctx, &currentNode); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			currentNode.Annotations = map[string]string{topology.CapabilityAnnotation: string(raw)}
			if err := c.Update(ctx, &currentNode); err != nil {
				t.Fatal(err)
			}
			var currentPod corev1.Pod
			if err := c.Get(ctx, client.ObjectKeyFromObject(&pod), &currentPod); err != nil {
				t.Fatal(err)
			}
			currentPod.Status = p.Status
			if err := c.Status().Update(ctx, &currentPod); err != nil {
				t.Fatal(err)
			}
			currentPod.OwnerReferences = p.OwnerReferences
			if err := c.Update(ctx, &currentPod); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, req.NamespacedName, &currentNode); err != nil {
				t.Fatal(err)
			}
			if (currentNode.Labels[topology.RevisionCapabilityLabel(revision.Spec.Digest)] == "true") != tc.want || currentNode.Labels["unrelated"] != "kept" {
				t.Fatalf("wrong eligibility: %+v", currentNode.Labels)
			}
		})
	}
	// A retained revision whose source no longer exists is cleanup authority,
	// not eligibility for new workloads.
	var restoredPod corev1.Pod
	if err := c.Get(ctx, client.ObjectKeyFromObject(&pod), &restoredPod); err != nil {
		t.Fatal(err)
	}
	restoredPod.OwnerReferences = pod.OwnerReferences
	if err := c.Update(ctx, &restoredPod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, &node); err != nil {
		t.Fatal(err)
	}
	if node.Labels[topology.RevisionCapabilityLabel(revision.Spec.Digest)] != "true" {
		t.Fatal("live source failed to recover eligibility")
	}
	source.UID = "removed-source"
	plugins := map[string]api.NetworkPlugin{}
	for _, p := range topology.Builtins() {
		plugins[p.Name] = p
	}
	spec, err := topology.Compile(source, plugins)
	if err != nil {
		t.Fatal(err)
	}
	orphan := api.NetworkTopologyRevision{ObjectMeta: metav1.ObjectMeta{Name: "removed-source"}, Spec: spec}
	if err := c.Create(ctx, &orphan); err != nil {
		t.Fatal(err)
	}
	orphan.Status = revision.Status
	if err := c.Status().Update(ctx, &orphan); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, &node); err != nil {
		t.Fatal(err)
	}
	if node.Labels[topology.RevisionCapabilityLabel(spec.Digest)] != "" {
		t.Fatal("removed topology remained eligible")
	}
}

package controller_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/controller"
	"github.com/SchSeba/dra-multi-network/pkg/flags"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestRevisionGCRetainsUnknownAndLiveOwnership(t *testing.T) {
	data, err := os.ReadFile("../../examples/bond-vlan/topology.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var source api.NetworkTopology
	if err := yaml.UnmarshalStrict(data, &source); err != nil {
		t.Fatal(err)
	}
	source.UID = "source-uid"
	spec, err := topology.Compile(source, topology.PluginRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	revision := api.NetworkTopologyRevision{ObjectMeta: metav1.ObjectMeta{Name: "retired", UID: "revision-uid", Labels: map[string]string{controller.ManagedLabel: "true"}, Finalizers: []string{controller.RevisionFinalizer}}, Spec: spec}
	ref := topology.RevisionIdentity(revision)
	binding, err := json.Marshal(api.TopologyRootConfiguration{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "TopologyRootConfiguration"}, RevisionRef: ref, RootStep: "left"})
	if err != nil {
		t.Fatal(err)
	}
	config := resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: api.Driver, Parameters: runtime.RawExtension{Raw: binding}}}
	class := resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "root-class", UID: "class-uid", Labels: map[string]string{controller.ManagedLabel: "true"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "NetworkTopologyRevision", Name: revision.Name, UID: revision.UID}}}, Spec: resourceapi.DeviceClassSpec{Config: []resourceapi.DeviceClassConfiguration{{DeviceConfiguration: config}}}}
	ds := appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "driver", UID: "ds-uid"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: "dra-multi-network-node"}}}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "driver", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: ds.Name, UID: ds.UID, Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{NodeName: "node", ServiceAccountName: "dra-multi-network-node"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "plugin", Ready: true, ContainerID: "cri-o://instance", ImageID: "sha256:build", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: "boot"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	report := topology.NodeCapabilities{Version: topology.ContractVersion, NodeUID: string(node.UID), BootID: "boot", PodName: pod.Name, PodUID: string(pod.UID), ImageID: "sha256:build", ContainerID: "cri-o://instance", Updated: time.Now().UTC(), Ready: true, Executor: topology.ExecutorContractDigest(), ReservationsChecked: true, DrainedRevisions: []api.RevisionRef{ref}}
	claimSpec := resourceapi.ResourceClaimSpec{Devices: resourceapi.DeviceClaim{Requests: []resourceapi.DeviceRequest{{Name: "left", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: class.Name, Count: 1}}}}}
	for _, tc := range []struct {
		name    string
		edit    func(*corev1.Node, *corev1.Pod, *topology.NodeCapabilities) []client.Object
		collect bool
	}{
		{"verified drain", func(*corev1.Node, *corev1.Pod, *topology.NodeCapabilities) []client.Object { return nil }, true},
		{"same source name with another UID", func(*corev1.Node, *corev1.Pod, *topology.NodeCapabilities) []client.Object {
			s := source.DeepCopy()
			s.UID = "replacement"
			return []client.Object{s}
		}, true},
		{"live source", func(*corev1.Node, *corev1.Pod, *topology.NodeCapabilities) []client.Object {
			return []client.Object{source.DeepCopy()}
		}, false},
		{"unallocated claim", func(*corev1.Node, *corev1.Pod, *topology.NodeCapabilities) []client.Object {
			return []client.Object{&resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "test"}, Spec: claimSpec}}
		}, false},
		{"allocated claim binding", func(*corev1.Node, *corev1.Pod, *topology.NodeCapabilities) []client.Object {
			return []client.Object{&resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "allocated", Namespace: "test"}, Status: resourceapi.ResourceClaimStatus{Allocation: &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{Config: []resourceapi.DeviceAllocationConfiguration{{DeviceConfiguration: config}}}}}}}
		}, false},
		{"template", func(*corev1.Node, *corev1.Pod, *topology.NodeCapabilities) []client.Object {
			return []client.Object{&resourceapi.ResourceClaimTemplate{ObjectMeta: metav1.ObjectMeta{Name: "template", Namespace: "test"}, Spec: resourceapi.ResourceClaimTemplateSpec{Spec: claimSpec}}}
		}, false},
		{"cleanup pending status", func(*corev1.Node, *corev1.Pod, *topology.NodeCapabilities) []client.Object {
			return []client.Object{&api.NetworkAttachmentStatus{ObjectMeta: metav1.ObjectMeta{Name: "attachment", Namespace: "test"}, Spec: api.AttachmentSpec{RevisionRef: ref}, Status: api.AttachmentStatus{Phase: "CleanupPending"}}}
		}, false},
		{"local reservation", func(_ *corev1.Node, _ *corev1.Pod, r *topology.NodeCapabilities) []client.Object {
			r.DrainedRevisions = nil
			r.RetainedRevisions = []api.RevisionRef{ref}
			return nil
		}, false},
		{"missing acknowledgement", func(_ *corev1.Node, _ *corev1.Pod, r *topology.NodeCapabilities) []client.Object {
			r.DrainedRevisions = nil
			return nil
		}, false},
		{"another revision incarnation", func(_ *corev1.Node, _ *corev1.Pod, r *topology.NodeCapabilities) []client.Object {
			r.DrainedRevisions = []api.RevisionRef{ref}
			r.DrainedRevisions[0].UID = "old-uid"
			return nil
		}, false},
		{"expired report", func(_ *corev1.Node, _ *corev1.Pod, r *topology.NodeCapabilities) []client.Object {
			r.Updated = time.Now().Add(-2 * topology.CapabilityTTL)
			return nil
		}, false},
		{"unchecked reservations", func(_ *corev1.Node, _ *corev1.Pod, r *topology.NodeCapabilities) []client.Object {
			r.ReservationsChecked = false
			return nil
		}, false},
		{"different boot", func(_ *corev1.Node, _ *corev1.Pod, r *topology.NodeCapabilities) []client.Object {
			r.BootID = "previous"
			return nil
		}, false},
		{"unreachable node", func(n *corev1.Node, _ *corev1.Pod, _ *topology.NodeCapabilities) []client.Object {
			n.Status.Conditions[0].Status = corev1.ConditionUnknown
			return nil
		}, false},
		{"unready agent", func(_ *corev1.Node, p *corev1.Pod, _ *topology.NodeCapabilities) []client.Object {
			p.Status.Conditions[0].Status = corev1.ConditionFalse
			return nil
		}, false},
		{"foreign class ownership", func(*corev1.Node, *corev1.Pod, *topology.NodeCapabilities) []client.Object {
			c := class.DeepCopy()
			c.Name = "foreign"
			c.OwnerReferences = nil
			return []client.Object{c}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, p := node.DeepCopy(), pod.DeepCopy()
			r := report
			extra := tc.edit(n, p, &r)
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			n.Annotations = map[string]string{topology.CapabilityAnnotation: string(raw)}
			objects := []client.Object{revision.DeepCopy(), class.DeepCopy(), ds.DeepCopy(), n, p}
			objects = append(objects, extra...)
			c := fake.NewClientBuilder().WithScheme(flags.Scheme).WithObjects(objects...).Build()
			reconciler := controller.RetentionReconciler{Client: c, Namespace: "driver"}
			ctx := context.Background()
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&revision)}
			for range 4 {
				_, err = reconciler.Reconcile(ctx, req)
				if tc.collect && err != nil {
					t.Fatal(err)
				}
			}
			var current api.NetworkTopologyRevision
			err = c.Get(ctx, req.NamespacedName, &current)
			if apierrors.IsNotFound(err) != tc.collect {
				t.Fatalf("incorrect collection: %v / %+v", err, current.ObjectMeta)
			}
			var currentClass resourceapi.DeviceClass
			err = c.Get(ctx, client.ObjectKeyFromObject(&class), &currentClass)
			if apierrors.IsNotFound(err) != tc.collect {
				t.Fatalf("incorrect class collection: %v", err)
			}
		})
	}
}

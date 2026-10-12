// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package admission

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestAttachmentStatusDeletionDoesNotBlockNamespaceCollection(t *testing.T) {
	s := Server{Namespace: "dra-multi-network"}
	for _, operation := range []admissionv1.Operation{admissionv1.Create, admissionv1.Update, admissionv1.Delete} {
		req := &admissionv1.AdmissionRequest{Kind: metav1.GroupVersionKind{Kind: "NetworkAttachmentStatus"}, Operation: operation, UserInfo: authenticationv1.UserInfo{Username: "system:serviceaccount:kube-system:namespace-controller"}}
		err := s.validate(context.Background(), req)
		if (operation == admissionv1.Delete) != (err == nil) {
			t.Fatalf("operation %s: %v", operation, err)
		}
	}
}

func TestPluginDeletionRequiresControllerRetention(t *testing.T) {
	s := Server{Namespace: "driver"}
	plugin := topology.Builtins()[0]
	plugin.Finalizers = []string{topology.PluginRetentionFinalizer}
	encode := func(p api.NetworkPlugin) runtime.RawExtension {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return runtime.RawExtension{Raw: b}
	}
	req := admissionv1.AdmissionRequest{Kind: metav1.GroupVersionKind{Kind: "NetworkPlugin"}, Operation: admissionv1.Delete, OldObject: encode(plugin)}
	if err := s.validate(context.Background(), &req); err == nil || !strings.Contains(err.Error(), "E_BUILTIN_CONTRACT") {
		t.Fatalf("builtin removed: %v", err)
	}
	data, err := os.ReadFile("../../examples/root-effect-program/plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plugin = api.NetworkPlugin{}
	if err := yaml.UnmarshalStrict(data, &plugin); err != nil {
		t.Fatal(err)
	}
	plugin.Finalizers = nil
	req.OldObject = encode(plugin)
	if err := s.validate(context.Background(), &req); err == nil || !strings.Contains(err.Error(), "E_RETAINED_CONTRACT") {
		t.Fatalf("unreconciled descriptor removed: %v", err)
	}
	plugin.Finalizers = []string{topology.PluginRetentionFinalizer}
	req.OldObject = encode(plugin)
	if err := s.validate(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	plugin.Finalizers = nil
	req.Operation = admissionv1.Update
	req.Object = encode(plugin)
	if err := s.validate(context.Background(), &req); err == nil || !strings.Contains(err.Error(), "E_CONTRACT_FINALIZER_AUTHORITY") {
		t.Fatalf("retention bypass: %v", err)
	}
	req.UserInfo.Username = "system:serviceaccount:driver:dra-multi-network-controller"
	if err := s.validate(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
}

func TestUnclaimedCDIAnnotationsCannotInjectProjectDevices(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"cdi.k8s.io/devices": api.Driver + "/vf=another-claim-endpoint"}}}
	data, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	s := Server{}
	err = s.validate(context.Background(), &admissionv1.AdmissionRequest{Kind: metav1.GroupVersionKind{Kind: "Pod"}, Operation: admissionv1.Create, Object: runtime.RawExtension{Raw: data}})
	if err == nil || !strings.Contains(err.Error(), "E_DEVICE_ACCESS_ORIGIN") {
		t.Fatalf("unclaimed CDI injection accepted: %v", err)
	}
}

func TestBootstrapTolerationCannotBypassProjectRuntimeGates(t *testing.T) {
	s := Server{Namespace: "driver"}
	for _, suffix := range []string{"", "/pod", "/container.app"} {
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Annotations: map[string]string{
			api.Driver + "/bootstrap-nri-toleration" + suffix: "true",
			"required-plugins.noderesource.dev":               `["dra-multi-network"]`,
		}}}
		if err := s.validateRuntimeAnnotations("tenant", pod, false); err == nil || !strings.Contains(err.Error(), "E_BOOTSTRAP_AUTHORITY") {
			t.Fatalf("tenant toleration %s accepted: %v", suffix, err)
		}
		pod.Namespace = s.Namespace
		if err := s.validateRuntimeAnnotations("operator", pod, false); err != nil {
			t.Fatalf("operator bootstrap rejected: %v", err)
		}
		for _, user := range []string{"operator", "system:serviceaccount:driver:dra-multi-network-controller", "system:serviceaccount:driver:dra-multi-network-node"} {
			if err := s.validateRuntimeAnnotations(user, pod, true); err == nil || !strings.Contains(err.Error(), "E_BOOTSTRAP_AUTHORITY") {
				t.Fatalf("project toleration %s accepted for %s: %v", suffix, user, err)
			}
		}
	}
}

func TestClaimBindingsCannotBypassAdmission(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, resourceapi.AddToScheme, api.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ref := api.RevisionRef{Name: "revision", UID: "revision-uid", Digest: "digest"}
	revision := &api.NetworkTopologyRevision{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, UID: "revision-uid"}, Spec: api.RevisionSpec{SourceUID: "source-uid", Digest: ref.Digest, Topology: api.TopologySpec{Steps: []api.Step{{ID: "left", Kind: "Allocation"}, {ID: "right", Kind: "Allocation"}}}}, Status: api.TopologyStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}}}
	source := &api.NetworkTopology{ObjectMeta: metav1.ObjectMeta{Name: "source", UID: "source-uid"}}
	objects := []runtime.Object{revision, source, &resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "other-driver"}}}
	for _, root := range []string{"left", "right"} {
		binding := api.TopologyRootConfiguration{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "TopologyRootConfiguration"}, RevisionRef: ref, RootStep: root}
		data, err := json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: root}, Spec: resourceapi.DeviceClassSpec{Config: []resourceapi.DeviceClassConfiguration{{DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: api.Driver, Parameters: runtime.RawExtension{Raw: data}}}}}}})
	}
	s := Server{Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()}
	good := resourceapi.ResourceClaimSpec{Devices: resourceapi.DeviceClaim{Requests: []resourceapi.DeviceRequest{{Name: "primary", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "left", Count: 1}}, {Name: "backup", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "right", Count: 1}}, {Name: "unrelated", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "other-driver", Count: 2}}}}}
	cases := []struct {
		name, reason string
		change       func(*resourceapi.ResourceClaimSpec)
	}{
		{"valid plus another driver", "", func(*resourceapi.ResourceClaimSpec) {}},
		{"partial roots", "E_ROOT_COVERAGE", func(p *resourceapi.ResourceClaimSpec) { p.Devices.Requests = p.Devices.Requests[:1] }},
		{"duplicate root", "E_ROOT_COVERAGE", func(p *resourceapi.ResourceClaimSpec) { p.Devices.Requests[1].Exactly.DeviceClassName = "left" }},
		{"firstAvailable", "E_REQUEST_PROFILE", func(p *resourceapi.ResourceClaimSpec) {
			p.Devices.Requests[0].Exactly = nil
			p.Devices.Requests[0].FirstAvailable = []resourceapi.DeviceSubRequest{{Name: "choice", DeviceClassName: "left", Count: 1}}
		}},
		{"root override", "E_BINDING_ORIGIN", func(p *resourceapi.ResourceClaimSpec) {
			p.Devices.Config = []resourceapi.DeviceClaimConfiguration{{DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: api.Driver, Parameters: runtime.RawExtension{Raw: []byte(`{}`)}}}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := good.DeepCopy()
			tc.change(p)
			err := s.claimSpec(context.Background(), *p, true)
			if tc.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s, got %v", tc.reason, err)
			}
		})
	}
	template := resourceapi.ResourceClaimTemplate{ObjectMeta: metav1.ObjectMeta{Name: "network", Namespace: "test"}, Spec: resourceapi.ResourceClaimTemplateSpec{Spec: good}}
	if err := s.Client.Create(context.Background(), &template); err != nil {
		t.Fatal(err)
	}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "test", UID: "pod"}, Spec: corev1.PodSpec{ResourceClaims: []corev1.PodResourceClaim{{Name: "network", ResourceClaimTemplateName: &template.Name}}, Containers: []corev1.Container{{Name: "app", Image: "pause"}}, Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "region", Operator: corev1.NodeSelectorOpIn, Values: []string{"west"}}}}, {MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"preferred-node"}}}}}}}}}}
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	req := &admissionv1.AdmissionRequest{Kind: metav1.GroupVersionKind{Kind: "Pod"}, Operation: admissionv1.Create, Object: runtime.RawExtension{Raw: raw}}
	patch, err := s.mutate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jsonpatch.DecodePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	mutated, err := decoded.Apply(raw)
	if err != nil {
		t.Fatal(err)
	}
	req.Object.Raw = mutated
	if err := s.validate(context.Background(), req); err != nil {
		t.Fatalf("injected requirements rejected: %v", err)
	}
	var guarded corev1.Pod
	if err := json.Unmarshal(mutated, &guarded); err != nil {
		t.Fatal(err)
	}
	terms := guarded.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 2 || terms[0].MatchExpressions[0].Key != "region" || terms[1].MatchFields[0].Values[0] != "preferred-node" || !hasRevisionAffinity(guarded, ref.Digest) {
		t.Fatal("mutation broadened existing placement or missed an OR alternative")
	}
	for _, edit := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.Spec.Affinity = nil },
		func(p *corev1.Pod) {
			p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms, pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0])
		},
	} {
		attempt := guarded.DeepCopy()
		edit(attempt)
		changed, err := json.Marshal(attempt)
		if err != nil {
			t.Fatal(err)
		}
		req.Operation, req.Object.Raw, req.OldObject.Raw = admissionv1.Update, changed, mutated
		if err := s.validate(context.Background(), req); err == nil || !strings.Contains(err.Error(), "E_REVISION_AFFINITY") {
			t.Fatalf("removing revision protection accepted: %v", err)
		}
	}
	// The API supplies oldObject for existing pre-upgrade Pods; new clients
	// cannot obtain that exception by omitting affinity on creation.
	legacy := guarded.DeepCopy()
	legacy.Spec.Affinity = nil
	legacyRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	req.Operation, req.Object.Raw, req.OldObject.Raw = admissionv1.Update, legacyRaw, legacyRaw
	if err := s.validate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Operation = admissionv1.Create
	if err := s.validate(context.Background(), req); err == nil {
		t.Fatal("new Pod claimed upgrade exception")
	}
	// An explicit empty affinity term originally matches no nodes.
	empty := corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{}}}}}}}
	requireRevisionAffinity(&empty, ref.Digest)
	if len(empty.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions) != 0 {
		t.Fatal("empty affinity term became schedulable")
	}
	if err := s.Client.Create(context.Background(), &guarded); err != nil {
		t.Fatal(err)
	}
	bareNode := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "unverified"}}
	if err := s.Client.Create(context.Background(), &bareNode); err != nil {
		t.Fatal(err)
	}
	binding := corev1.Binding{ObjectMeta: metav1.ObjectMeta{Name: guarded.Name, Namespace: guarded.Namespace, UID: guarded.UID}, Target: corev1.ObjectReference{Kind: "Node", Name: bareNode.Name}}
	for _, tc := range []struct {
		name, reason string
		edit         func(*corev1.Binding)
	}{
		{"unverified node", "E_NODE_CAPABILITY", func(*corev1.Binding) {}},
		{"name reuse", "E_BINDING_POD_IDENTITY", func(b *corev1.Binding) { b.UID = "old-pod" }},
		{"runtime removal", "E_REQUIRED_PLUGIN", func(b *corev1.Binding) { b.Annotations = map[string]string{"required-plugins.noderesource.dev": "[]"} }},
		{"CDI injection", "E_DEVICE_ACCESS_ORIGIN", func(b *corev1.Binding) {
			b.Annotations = map[string]string{"cdi.k8s.io/devices": api.Driver + "/vf=unclaimed"}
		}},
		{"bootstrap bypass", "E_BOOTSTRAP_AUTHORITY", func(b *corev1.Binding) {
			b.Annotations = map[string]string{api.Driver + "/bootstrap-nri-toleration": "true"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := binding.DeepCopy()
			tc.edit(attempt)
			data, err := json.Marshal(attempt)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{guarded.Name, ""} {
				request := &admissionv1.AdmissionRequest{Kind: metav1.GroupVersionKind{Kind: "Binding"}, Operation: admissionv1.Create, Name: name, Namespace: guarded.Namespace, Object: runtime.RawExtension{Raw: data}}
				if err := s.validate(context.Background(), request); err == nil || !strings.Contains(err.Error(), tc.reason) {
					t.Fatalf("binding bypass accepted: %v", err)
				}
			}
		})
	}
	if err := s.Client.Delete(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	claim := resourceapi.ResourceClaim{Spec: good}
	claimRaw, _ := json.Marshal(claim)
	templateRaw, _ := json.Marshal(template)
	bindingRaw, _ := json.Marshal(binding)
	for _, tc := range []struct {
		kind string
		raw  []byte
	}{
		{"ResourceClaim", claimRaw}, {"ResourceClaimTemplate", templateRaw}, {"Pod", mutated}, {"Binding", bindingRaw},
	} {
		request := &admissionv1.AdmissionRequest{Kind: metav1.GroupVersionKind{Kind: tc.kind}, Operation: admissionv1.Create, Name: guarded.Name, Namespace: guarded.Namespace, Object: runtime.RawExtension{Raw: tc.raw}}
		if err := s.validate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "E_TOPOLOGY_RETIRED") {
			t.Fatalf("new %s admitted after source deletion: %v", tc.kind, err)
		}
		if tc.kind == "Pod" {
			if _, err := s.mutate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "E_TOPOLOGY_RETIRED") {
				t.Fatalf("Pod mutation admitted retired source: %v", err)
			}
		}
		if tc.kind != "Binding" {
			request.Operation, request.OldObject.Raw = admissionv1.Update, tc.raw
			if err := s.validate(context.Background(), request); err != nil {
				t.Fatalf("existing %s update blocked after source deletion: %v", tc.kind, err)
			}
		}
	}
	// Reusing the name cannot revive a revision bound to the previous UID.
	replacement := &api.NetworkTopology{ObjectMeta: metav1.ObjectMeta{Name: source.Name, UID: "replacement-uid"}}
	if err := s.Client.Create(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if err := s.requireActiveRevision(context.Background(), revision); err == nil {
		t.Fatal("replacement source revived a retained revision")
	}
	if err := s.Client.Delete(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	deleting := &api.NetworkTopology{ObjectMeta: metav1.ObjectMeta{Name: source.Name, UID: source.UID, Finalizers: []string{"test-retain"}}}
	if err := s.Client.Create(context.Background(), deleting); err != nil {
		t.Fatal(err)
	}
	if err := s.Client.Delete(context.Background(), deleting); err != nil {
		t.Fatal(err)
	}
	if err := s.requireActiveRevision(context.Background(), revision); err == nil {
		t.Fatal("deleting source remained active")
	}
}

func TestCapabilityMetadataAuthority(t *testing.T) {
	s := Server{Namespace: "driver"}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node"}}
	controllerUser, agentUser := "system:serviceaccount:driver:dra-multi-network-controller", "system:serviceaccount:driver:dra-multi-network-node"
	identity := func(username string) authenticationv1.UserInfo {
		return authenticationv1.UserInfo{Username: username, Extra: map[string]authenticationv1.ExtraValue{"authentication.kubernetes.io/node-name": {"node"}, "authentication.kubernetes.io/node-uid": {"node"}, "authentication.kubernetes.io/pod-name": {"agent"}, "authentication.kubernetes.io/pod-uid": {"pod"}}}
	}
	for _, tc := range []struct {
		user, key string
		want      bool
	}{
		{"tenant", topology.RevisionCapabilityLabel("revision"), false},
		{agentUser, topology.RevisionCapabilityLabel("revision"), false},
		{controllerUser, topology.RevisionCapabilityLabel("revision"), true},
		{"tenant", api.Driver + "/ready", false},
		{agentUser, api.Driver + "/ready", true},
		{"tenant", "unrelated", true},
	} {
		changed := *node.DeepCopy()
		changed.Labels = map[string]string{tc.key: "true"}
		if err := s.validateCapabilityMetadata(identity(tc.user), node, changed); (err == nil) != tc.want {
			t.Fatalf("user %s key %s: %v", tc.user, tc.key, err)
		}
		if err := s.validateCapabilityMetadata(identity(tc.user), changed, node); (err == nil) != tc.want {
			t.Fatalf("removal by %s key %s: %v", tc.user, tc.key, err)
		}
	}
	report := topology.NodeCapabilities{Version: topology.ContractVersion, NodeUID: "node", BootID: "boot", PodName: "agent", PodUID: "pod", ImageID: "build", ContainerID: "container", Updated: time.Now(), Ready: true, Executor: topology.ExecutorContractDigest()}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	changed := *node.DeepCopy()
	changed.Annotations = map[string]string{topology.CapabilityAnnotation: string(raw)}
	for _, user := range []string{"tenant", controllerUser, agentUser} {
		if err := s.validateCapabilityMetadata(identity(user), node, changed); (err == nil) != (user == agentUser) {
			t.Fatalf("report authority %s: %v", user, err)
		}
	}
	for _, key := range []string{"authentication.kubernetes.io/node-name", "authentication.kubernetes.io/node-uid", "authentication.kubernetes.io/pod-name", "authentication.kubernetes.io/pod-uid"} {
		foreign := identity(agentUser)
		foreign.Extra[key] = authenticationv1.ExtraValue{"another"}
		if err := s.validateCapabilityMetadata(foreign, node, changed); err == nil {
			t.Fatalf("foreign token claim accepted: %s", key)
		}
	}
	if err := s.validateCapabilityMetadata(authenticationv1.UserInfo{Username: agentUser}, node, changed); err == nil {
		t.Fatal("unbound agent token accepted")
	}
	changed.UID = "another-node"
	if err := s.validateCapabilityMetadata(identity(agentUser), node, changed); err == nil {
		t.Fatal("report for another node accepted")
	}
	if _, err := topology.ParseCapabilities(string(raw)+`{"unknown":true}`, time.Now()); err == nil {
		t.Fatal("non-strict report accepted")
	}
	if _, err := topology.ParseCapabilities(strings.Repeat(" ", topology.MaxCapabilityBytes+1), time.Now()); err == nil {
		t.Fatal("unbounded report accepted")
	}
}

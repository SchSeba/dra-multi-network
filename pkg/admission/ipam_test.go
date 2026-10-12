// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package admission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestIPAMClaimRequiresEachRootAndOneAddressUnit(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, resourceapi.AddToScheme, api.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ref := api.RevisionRef{Name: "revision", UID: "revision-uid", Digest: "digest"}
	revision := &api.NetworkTopologyRevision{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, UID: "revision-uid"}, Spec: api.RevisionSpec{Digest: ref.Digest, Topology: api.TopologySpec{Steps: []api.Step{{ID: "endpoint", Kind: "Allocation", PluginRef: api.PluginRef{Name: "macvlan-cni"}}, {ID: "address", Kind: "Allocation", PluginRef: api.PluginRef{Name: "ipam"}}}}, Plugins: []api.LockedPlugin{{Name: "ipam", Spec: topology.PluginRegistry(nil)["ipam"].Spec}}}, Status: api.TopologyStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}}}
	objects := []runtime.Object{revision}
	for _, root := range []string{"endpoint", "address"} {
		binding := api.TopologyRootConfiguration{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "TopologyRootConfiguration"}, RevisionRef: ref, RootStep: root}
		raw, _ := json.Marshal(binding)
		objects = append(objects, &resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: root}, Spec: resourceapi.DeviceClassSpec{Config: []resourceapi.DeviceClassConfiguration{{DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: api.Driver, Parameters: runtime.RawExtension{Raw: raw}}}}}}})
	}
	server := Server{Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()}
	good := resourceapi.ResourceClaimSpec{Devices: resourceapi.DeviceClaim{Requests: []resourceapi.DeviceRequest{{Name: "endpoint", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "endpoint", Count: 1}}, {Name: "address", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "address", Count: 1, Capacity: &resourceapi.CapacityRequirements{Requests: map[resourceapi.QualifiedName]resource.Quantity{resourceapi.QualifiedName(topology.AddressCapacity): resource.MustParse("1")}}}}}}}
	if _, err := server.claimRevision(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*resourceapi.ResourceClaimSpec){
		func(c *resourceapi.ResourceClaimSpec) { c.Devices.Requests = c.Devices.Requests[:1] },
		func(c *resourceapi.ResourceClaimSpec) { c.Devices.Requests[1].Exactly.Capacity = nil },
		func(c *resourceapi.ResourceClaimSpec) {
			c.Devices.Requests[1].Exactly.Capacity.Requests[resourceapi.QualifiedName(topology.AddressCapacity)] = resource.MustParse("2")
		},
		func(c *resourceapi.ResourceClaimSpec) {
			c.Devices.Config = []resourceapi.DeviceClaimConfiguration{{DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: topology.IPAMDriver, Parameters: runtime.RawExtension{Raw: []byte(`{}`)}}}}}
		},
	} {
		claim := good.DeepCopy()
		change(claim)
		if _, err := server.claimRevision(context.Background(), *claim); err == nil {
			t.Fatal("incomplete/forged IPAM claim accepted")
		}
	}
}
func TestIPAMLedgerCannotBeResetByWorkloadUser(t *testing.T) {
	cm := corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ipam-ledger-identity", Namespace: "driver"}, Data: map[string]string{"leases": "{}"}}
	raw, _ := json.Marshal(cm)
	req := &admissionv1.AdmissionRequest{Kind: metav1.GroupVersionKind{Kind: "ConfigMap"}, Namespace: "driver", Operation: admissionv1.Update, UserInfo: authenticationv1.UserInfo{Username: "workload-user"}, Object: runtime.RawExtension{Raw: raw}, OldObject: runtime.RawExtension{Raw: raw}}
	err := (&Server{Namespace: "driver"}).validate(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "E_IPAM_LEDGER_AUTHORITY") {
		t.Fatal("workload modified backend uniqueness state", err)
	}
}

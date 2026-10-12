// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"context"
	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/journal"
	sdk "github.com/SchSeba/dra-multi-network/pkg/pluginapi"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

func TestEswitchPolicyDrainAndDeletionBeforeMutation(t *testing.T) {
	p := api.DeviceExposurePolicy{ObjectMeta: metav1.ObjectMeta{Name: "mode", UID: "policy"}, Spec: api.PolicySpec{Action: ManageEswitch, Selector: api.DeviceSelector{Kind: "PF", PCIAddresses: []string{"0000:29:00.0"}}, Management: &api.EswitchManagement{Profile: effects.EswitchProfile, Mode: "switchdev", AllowedModes: []string{"legacy", "switchdev"}, EffectPermissions: []string{sdk.EswitchConfigure}}}}
	if err := ValidatePolicy(p, topology.PluginRegistry(nil)); err != nil {
		t.Fatal(err)
	}
	bad := *p.DeepCopy()
	bad.Spec.Selector.PCIAddresses = append(bad.Spec.Selector.PCIAddresses, "0000:30:00.0")
	if err := ValidatePolicy(bad, topology.PluginRegistry(nil)); err == nil {
		t.Fatal("multi-PF grant accepted")
	}
	bad = *p.DeepCopy()
	bad.Spec.Management.AllowedModes = []string{"switchdev"}
	if err := ValidatePolicy(bad, topology.PluginRegistry(nil)); err == nil {
		t.Fatal("missing restoration grant")
	}
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node"}}
	a := Agent{node: node, boot: "boot", store: store, client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.DeviceExposurePolicy{}).WithObjects(&node, &p).Build()}
	check := func(active map[string]bool, phase string) {
		t.Helper()
		if _, err := a.reconcileEswitch(context.Background(), []api.DeviceExposurePolicy{p}, active); err != nil {
			t.Fatal(err)
		}
		var r eswitchManagementRecord
		found, err := store.Get("eswitch/0000:29:00.0", &r)
		if err != nil || !found || r.Phase != phase || r.Native != nil || !a.eswitchBlocked[r.PCI] {
			t.Fatal("unsafe management", r, err)
		}
	}
	check(map[string]bool{"allocated": true}, "PendingDrain")
	check(nil, "PendingCordon")
	node.Spec.Unschedulable = true
	if err := a.client.Update(context.Background(), &node); err != nil {
		t.Fatal(err)
	}
	a.inventory = map[string]Inventory{"0000:29:00.0": {PCI: "0000:29:00.0", Kind: PFKind}}
	check(nil, pendingEswitchFence)
	if _, err := a.reconcileEswitch(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	var r eswitchManagementRecord
	_, err = store.Get("eswitch/0000:29:00.0", &r)
	if err != nil || r.Phase != eswitchReleased || len(a.eswitchBlocked) != 0 {
		t.Fatal("pending deletion retained a fence", r, err)
	}
}

// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func TestRoCEProfileAndFourHooksRequireWholeHCANamespaceThenScopedAccess(t *testing.T) {
	data, err := os.ReadFile("../../examples/roce/topology.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.YAMLToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	var network api.NetworkTopology
	if err := topology.Decode(raw, &network); err != nil {
		t.Fatal(err)
	}
	registry := topology.PluginRegistry(nil)
	for _, name := range []string{"host-cni-roce", "sriov-cni-roce"} {
		network.Spec.Steps[0].PluginRef.Name = name
		revision, err := topology.Compile(network, registry)
		if err != nil {
			t.Fatal(err)
		}
		prepare, err := topology.PlanPrepare(revision)
		if err != nil || len(prepare.Effects) != 0 {
			t.Fatalf("RoCE preparation must be read-only: %+v %v", prepare, err)
		}
		connect, err := topology.PlanConnect(revision)
		if err != nil || len(connect.Effects) != 2 || connect.Effects[0].Kind != topology.RDMANamespace || connect.Effects[1].Kind != topology.DeviceAccess || connect.Effects[1].Parameters.AccessBackend != "nri" || connect.Exports["root.rdma"].From != "effects.root-access.device" {
			t.Fatalf("incorrect scoped access plan: %+v %v", connect, err)
		}
	}
	network.Spec.Steps[0].PluginRef.Name = "host-cni-roce"
	profile := registry["host-cni-roce"]
	custom := *profile.DeepCopy()
	custom.Name = "custom-roce"
	custom.Spec.Implementation = api.Implementation{Type: topology.EffectProgramImplementation, Version: topology.AllocationEffectsVersion}
	custom.Spec.AllocationProfileRef = &api.AllocationProfileRef{Name: profile.Name, Digest: topology.Digest(profile.Spec)}
	// These four hooks preserve the native certificate and cleanup authority.
	custom.Spec.Hooks = map[string]ext.JSON{
		topology.PrepareHook:   {Raw: []byte(`{"apiVersion":"effects.dra-multi-network.schseba.github.io/v1alpha1","effects":[],"exports":{}}`)},
		topology.ConnectHook:   {Raw: []byte(`{"apiVersion":"effects.dra-multi-network.schseba.github.io/v1alpha1","effects":[{"id":"namespace","kind":"linux/rdma-namespace/v1","inputs":{"device":{"from":"call.allocation"}},"requires":[{"kind":"allocation-and-owned-inputs"}],"postconditions":[{"kind":"native-state-match"}],"recovery":{"strategy":"ObserveAndReconcile"},"parameters":{},"parameterBindings":{"ifName":{"from":"call.config.ifName"},"mtu":{"from":"call.config.mtu"},"up":{"from":"call.config.up"},"rdmaProfile":{"from":"call.config.rdmaProfile"}}},{"id":"access","kind":"runtime/device-access/v1","inputs":{"device":{"from":"effects.namespace.device"}},"requires":[{"kind":"allocation-and-owned-inputs"}],"postconditions":[{"kind":"native-state-match"}],"recovery":{"strategy":"ObserveAndReconcile"},"parameters":{},"parameterBindings":{"mode":{"from":"call.config.mode"},"accessBackend":{"from":"call.config.accessBackend"}}}],"exports":{"rdma":{"from":"effects.access.device"}}}`)},
		topology.RemoveHook:    {Raw: []byte(`{"apiVersion":"effects.dra-multi-network.schseba.github.io/v1alpha1","restoreRecordedHook":"ConnectNetwork"}`)},
		topology.UnprepareHook: {Raw: []byte(`{"apiVersion":"effects.dra-multi-network.schseba.github.io/v1alpha1","restoreRecordedHook":"NodePrepare"}`)},
	}
	if err := topology.ValidatePlugin(custom); err != nil {
		t.Fatal(err)
	}
	registry[custom.Name] = custom
	network.Spec.Steps[0].PluginRef.Name = custom.Name
	if _, err := topology.Compile(network, registry); err != nil {
		t.Fatal(err)
	}
	custom.Spec.Hooks[topology.ConnectHook] = ext.JSON{Raw: []byte(strings.ReplaceAll(string(custom.Spec.Hooks[topology.ConnectHook].Raw), "effects.access.device", "effects.namespace.device"))}
	registry[custom.Name] = custom
	if _, err := topology.Compile(network, registry); err == nil {
		t.Fatal("unacknowledged namespace output bypassed device-access effect")
	}
	network.Spec.Steps[0].PluginRef.Name = "host-cni-roce"
	for _, change := range []func(*api.NetworkTopology){
		func(n *api.NetworkTopology) { n.Spec.EffectBudget.Allow = []string{topology.RDMANamespace} },
		func(n *api.NetworkTopology) {
			n.Spec.Steps = append(n.Spec.Steps, n.Spec.Steps[0])
			n.Spec.Steps[1].ID = "second"
			n.Spec.Steps[1].Config.Raw = []byte(strings.ReplaceAll(string(n.Spec.Steps[1].Config.Raw), "roce0", "roce1"))
		},
		func(n *api.NetworkTopology) {
			n.Spec.Steps[0].Config.Raw = []byte(strings.ReplaceAll(string(n.Spec.Steps[0].Config.Raw), "roce0", "eth0"))
		},
		func(n *api.NetworkTopology) {
			n.Spec.Steps[0].Config.Raw = []byte(strings.ReplaceAll(string(n.Spec.Steps[0].Config.Raw), `"rdmaAccess":true`, `"rdmaAccess":false`))
		},
		func(n *api.NetworkTopology) {
			n.Spec.Steps[0].Config.Raw = []byte(strings.ReplaceAll(string(n.Spec.Steps[0].Config.Raw), `"nri"`, `"cdi"`))
		},
	} {
		candidate := network.DeepCopy()
		change(candidate)
		if _, err := topology.Compile(*candidate, registry); err == nil {
			data, _ := json.Marshal(candidate)
			t.Fatalf("accepted invalid profile/topology: %s", data)
		}
	}
}

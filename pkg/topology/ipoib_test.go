// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology_test

import (
	"os"
	"strings"
	"testing"

	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestIBProfilesPreserveTypedPortsAndScopedFourHookPrograms(t *testing.T) {
	registry := topology.PluginRegistry(nil)
	for _, filename := range []string{"topology.yaml", "verbs-topology.yaml"} {
		data, err := os.ReadFile("../../examples/ipoib/" + filename)
		if err != nil {
			t.Fatal(err)
		}
		var network api.NetworkTopology
		if err := yaml.UnmarshalStrict(data, &network); err != nil {
			t.Fatal(err)
		}
		for _, handler := range []string{topology.HostPlugin, topology.SRIOVPlugin} {
			name := handler + "-ipoib"
			if filename == "verbs-topology.yaml" {
				name = handler + "-ib-verbs"
			}
			network.Spec.Steps[0].PluginRef.Name = name
			r, err := topology.Compile(network, registry)
			if err != nil {
				t.Fatal(err)
			}
			prepare, err := topology.PlanPrepare(r)
			if err != nil || len(prepare.Effects) != 0 {
				t.Fatal("IB preparation mutated hardware", err)
			}
			connect, err := topology.PlanConnect(r)
			if err != nil || len(connect.Effects) != 2 || connect.Effects[0].Kind != topology.RDMANamespace || connect.Effects[1].Kind != topology.DeviceAccess {
				t.Fatal("incorrect IB plan", connect, err)
			}
			profile := registry[name]
			if profile.Spec.Outputs[topology.RDMAPort].LinkType != topology.InfiniBand {
				t.Fatal("lost typed IB output")
			}
			if filename == "topology.yaml" {
				if profile.Spec.Outputs[topology.InterfacePort].LinkType != topology.InfiniBand || connect.Exports["root.interface"].From != "effects.root-rdma.interface" || !connect.Effects[0].Parameters.MADAccess {
					t.Fatal("lost IPoIB/MAD configuration", connect)
				}
			} else if len(connect.Exports) != 1 || connect.Effects[0].Parameters.IfName != "" {
				t.Fatal("verbs-only profile silently manages an interface")
			}
		}
		bad := network.DeepCopy()
		bad.Spec.Steps[0].Config.Raw = []byte(strings.ReplaceAll(string(bad.Spec.Steps[0].Config.Raw), "infiniband", "ethernet"))
		if _, err := topology.Compile(*bad, registry); err == nil {
			t.Fatal("IB allocation accepted an Ethernet link type")
		}
		if filename == "verbs-topology.yaml" {
			continue
		}
		// A programmable allocation uses the same exact native profile and cleanup
		// authority; it cannot hide the IB port or bypass scoped device access.
		profile := registry[network.Spec.Steps[0].PluginRef.Name]
		custom := *profile.DeepCopy()
		custom.Name = "custom-ipoib"
		custom.Spec.Implementation = api.Implementation{Type: topology.EffectProgramImplementation, Version: topology.AllocationEffectsVersion}
		custom.Spec.AllocationProfileRef = &api.AllocationProfileRef{Name: profile.Name, Digest: topology.Digest(profile.Spec)}
		custom.Spec.Hooks = map[string]ext.JSON{
			topology.PrepareHook:   {Raw: []byte(`{"apiVersion":"effects.dra-multi-network.schseba.github.io/v1alpha1","effects":[],"exports":{}}`)},
			topology.ConnectHook:   {Raw: []byte(`{"apiVersion":"effects.dra-multi-network.schseba.github.io/v1alpha1","effects":[{"id":"namespace","kind":"linux/rdma-namespace/v1","inputs":{"device":{"from":"call.allocation"}},"requires":[{"kind":"allocation-and-owned-inputs"}],"postconditions":[{"kind":"native-state-match"}],"recovery":{"strategy":"ObserveAndReconcile"},"parameters":{},"parameterBindings":{"ifName":{"from":"call.config.ifName"},"mtu":{"from":"call.config.mtu"},"up":{"from":"call.config.up"},"rdmaProfile":{"from":"call.config.rdmaProfile"},"madAccess":{"from":"call.config.madAccess"}}},{"id":"access","kind":"runtime/device-access/v1","inputs":{"device":{"from":"effects.namespace.device"}},"requires":[{"kind":"allocation-and-owned-inputs"}],"postconditions":[{"kind":"native-state-match"}],"recovery":{"strategy":"ObserveAndReconcile"},"parameters":{},"parameterBindings":{"mode":{"from":"call.config.mode"},"accessBackend":{"from":"call.config.accessBackend"}}}],"exports":{"rdma":{"from":"effects.access.device"},"interface":{"from":"effects.namespace.interface"}}}`)},
			topology.RemoveHook:    {Raw: []byte(`{"apiVersion":"effects.dra-multi-network.schseba.github.io/v1alpha1","restoreRecordedHook":"ConnectNetwork"}`)},
			topology.UnprepareHook: {Raw: []byte(`{"apiVersion":"effects.dra-multi-network.schseba.github.io/v1alpha1","restoreRecordedHook":"NodePrepare"}`)},
		}
		registry[custom.Name] = custom
		network.Spec.Steps[0].PluginRef.Name = custom.Name
		if _, err := topology.Compile(network, registry); err != nil {
			t.Fatal(err)
		}
		custom.Spec.Hooks[topology.ConnectHook] = ext.JSON{Raw: []byte(strings.ReplaceAll(string(custom.Spec.Hooks[topology.ConnectHook].Raw), "effects.namespace.interface", "effects.access.device"))}
		registry[custom.Name] = custom
		if _, err := topology.Compile(network, registry); err == nil {
			t.Fatal("RDMA device masqueraded as the typed IPoIB interface")
		}
	}
}

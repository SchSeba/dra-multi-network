// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology_test

import (
	"os"
	"strings"
	"testing"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func TestSwitchdevSeparatesEndpointAndHostRepresentor(t *testing.T) {
	data, err := os.ReadFile("../../test/integration/switchdev/fixtures/topology.yaml")
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
	registry := topology.PluginRegistry(nil)
	r, err := topology.Compile(source, registry)
	if err != nil {
		t.Fatal(err)
	}
	p, err := topology.PlanConnect(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Effects) != 4 || p.Effects[0].Kind != topology.RepresentorMap || p.Effects[3].Kind != topology.BridgeConfigure || p.Exports["vf.representor"].From != "effects.vf-representor.representor" || p.Exports["vf.interface"].From != "effects.vf-configure.interface" {
		t.Fatalf("mixed endpoint/representor authority: %+v", p)
	}
	for _, tc := range []struct {
		name   string
		edit   func(*api.NetworkTopology)
		reason string
	}{
		{"endpoint used as representor", func(v *api.NetworkTopology) {
			v.Spec.Steps[1].Inputs[topology.RepresentorPort] = ext.JSON{Raw: []byte(`{"from":"vf.interface"}`)}
		}, "E_PORT_TYPE"},
		{"two masters", func(v *api.NetworkTopology) {
			next := v.Spec.Steps[1]
			next.ID = "other"
			v.Spec.Steps = append(v.Spec.Steps, next)
		}, "E_MULTIPLE_MASTERS"},
		{"master chain", func(v *api.NetworkTopology) {
			next := v.Spec.Steps[1]
			next.ID = "other"
			next.Inputs = map[string]ext.JSON{topology.RepresentorPort: {Raw: []byte(`{"from":"fabric.representor"}`)}}
			v.Spec.Steps = append(v.Spec.Steps, next)
		}, "E_MULTIPLE_MASTERS"},
		{"VLAN collision", func(v *api.NetworkTopology) {
			v.Spec.Steps[1].Config.Raw = []byte(`{"bridge":"fabric0","mtu":1500,"up":true,"vlanId":100,"trunk":[100]}`)
		}, "E_REPRESENTOR_VLAN_CONFIG"},
		{"missing budget", func(v *api.NetworkTopology) {
			v.Spec.EffectBudget.Allow = []string{topology.Move, topology.Configure, topology.BridgeConfigure}
		}, "E_EFFECT_DENIED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *source.DeepCopy()
			tc.edit(&candidate)
			_, err := topology.Compile(candidate, registry)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s: %v", tc.reason, err)
			}
		})
	}
	// The same allocation also supports VFIO; its representor remains host-side.
	source.Spec.Steps[0].PluginRef.Name = "sriov-cni-vfio-switchdev"
	source.Spec.Steps[0].Config.Raw = []byte(`{"mode":"vfio","linkType":"ethernet","representor":true,"driver":"vfio-pci","accessBackend":"cdi"}`)
	source.Spec.EffectBudget.Allow = append(source.Spec.EffectBudget.Allow, topology.DriverBind, topology.DeviceAccess)
	r, err = topology.Compile(source, registry)
	if err != nil {
		t.Fatal(err)
	}
	p, err = topology.PlanConnect(r)
	if err != nil || len(p.Effects) != 2 || p.Exports["vf.vfio"].From != "call.prepared.vf.vfio" {
		t.Fatalf("VFIO representor split: %+v %v", p, err)
	}
	source.Spec.Steps[0].PluginRef.Name = "sriov-cni-roce-switchdev"
	source.Spec.Steps[0].Config.Raw = []byte(`{"mode":"rdma","linkType":"ethernet","rdmaAccess":true,"representor":true,"rdmaProfile":"roce-single-port-v1","accessBackend":"nri","ifName":"vf0","mtu":1500,"up":true}`)
	source.Spec.EffectBudget.Allow = append(source.Spec.EffectBudget.Allow, topology.RDMANamespace)
	r, err = topology.Compile(source, registry)
	if err != nil {
		t.Fatal(err)
	}
	p, err = topology.PlanConnect(r)
	if err != nil || len(p.Effects) != 4 || p.Exports["vf.rdma"].From != "effects.vf-access.device" {
		t.Fatalf("RoCE representor split: %+v %v", p, err)
	}
	for _, profile := range []string{"sriov-cni-switchdev", "sriov-cni-vfio-switchdev", "sriov-cni-roce-switchdev"} {
		candidate := *source.DeepCopy()
		candidate.Spec.Steps[1].PluginRef.Name = topology.OVSPlugin
		candidate.Spec.EffectBudget.Allow = append(candidate.Spec.EffectBudget.Allow, topology.OVSConfigure)
		candidate.Spec.Steps[0].PluginRef.Name = profile
		if profile == "sriov-cni-switchdev" {
			candidate.Spec.Steps[0].Config.Raw = []byte(`{"mode":"kernel","linkType":"ethernet","rdmaAccess":false,"representor":true,"ifName":"vf0","mtu":1500,"up":true}`)
		}
		if profile == "sriov-cni-vfio-switchdev" {
			candidate.Spec.Steps[0].Config.Raw = []byte(`{"mode":"vfio","linkType":"ethernet","representor":true,"driver":"vfio-pci","accessBackend":"cdi"}`)
		}
		r, err := topology.Compile(candidate, registry)
		if err != nil {
			t.Fatal(profile, err)
		}
		p, err := topology.PlanConnect(r)
		if err != nil || p.Effects[len(p.Effects)-1].Kind != topology.OVSConfigure {
			t.Fatal(profile, p, err)
		}
		if err := topology.PlanCleanup(r, topology.RemoveHook, "recorded-execution"); err != nil {
			t.Fatal(err)
		}
		candidate.Spec.Steps[1].Config.Raw = []byte(`{"bridge":"fabric0","mtu":1500,"up":true,"vlanId":0}`)
		if _, err := topology.Compile(candidate, registry); err == nil || !strings.Contains(err.Error(), "E_OVS_VLAN_REQUIRED") {
			t.Fatal("unrestricted OVS trunk accepted", err)
		}
	}
}

func TestSharedUplinkRequiresSeparateEffectAndTypedRepresentor(t *testing.T) {
	data, err := os.ReadFile("../../examples/switchdev/uplink-topology.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var source api.NetworkTopology
	if err := yaml.UnmarshalStrict(data, &source); err != nil {
		t.Fatal(err)
	}
	r, err := topology.Compile(source, topology.PluginRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	p, err := topology.PlanConnect(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Effects) != 5 || p.Effects[3].Kind != topology.UplinkConfigure || p.Effects[4].Kind != topology.BridgeConfigure || p.Effects[4].Inputs[topology.RepresentorPort].From != "effects.fabric-uplink.representor" || p.Effects[4].Parameters.Uplink != "" {
		t.Fatal("uplink authority mixed with representor mutation", p)
	}
	source.Spec.EffectBudget.Allow = source.Spec.EffectBudget.Allow[:4]
	if _, err := topology.Compile(source, topology.PluginRegistry(nil)); err == nil {
		t.Fatal("uplink without effect budget")
	}
	source.Spec.EffectBudget.Allow = append(source.Spec.EffectBudget.Allow, topology.UplinkConfigure)
	source.Spec.Steps[1].PluginRef.Name = topology.AcceleratedBridgePlugin
	if _, err := topology.Compile(source, topology.PluginRegistry(nil)); err == nil {
		t.Fatal("ordinary bridge plugin acquired hidden uplink authority")
	}
}

package topology_test

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestKernelTopology(t *testing.T) {
	data, err := os.ReadFile("../../examples/bond-vlan/topology.yaml")
	if err != nil {
		t.Fatal(err)
	}
	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	plugins := map[string]api.NetworkPlugin{}
	for _, p := range topology.Builtins() {
		plugins[p.Name] = p
	}
	cases := []struct{ name, old, new, reason string }{
		{name: "valid"},
		{"unknown config", `"vlanId":100`, `"vlanId":100,"misspelled":true`, "E_CONFIG_SCHEMA"},
		{"VLAN range", `"vlanId":100`, `"vlanId":4095`, "E_CONFIG_SCHEMA"},
		{"port type", `uplink.interface`, `uplink.device`, "E_PORT_TYPE"},
		{"duplicate slave", `right.interface`, `left.interface`, "E_MULTIPLE_MASTERS"},
		{"duplicate name", `tenant100`, `bond0`, "E_NAME_COLLISION"},
		{"duplicate VLAN tag", `"vlanId":200`, `"vlanId":100`, "E_VLAN_DUPLICATE"},
		{"missing false", `,"rdmaAccess":false`, "", "E_CONFIG_SCHEMA"},
		{"denied effect", `linux/create-vlan/v1`, `linux/unknown/v1`, "E_EFFECT_DENIED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := jsonData
			if tc.old != "" {
				raw = []byte(strings.ReplaceAll(string(raw), tc.old, tc.new))
			}
			var topologyObject api.NetworkTopology
			if err := topology.Decode(raw, &topologyObject); err != nil {
				t.Fatal(err)
			}
			revision, err := topology.Compile(topologyObject, plugins)
			if tc.reason != "" {
				if err == nil || !strings.Contains(err.Error(), tc.reason) {
					t.Fatalf("want %s, got %v", tc.reason, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := topology.VerifyRevision(revision); err != nil {
				t.Fatal(err)
			}
			for _, hook := range []string{topology.RemoveHook, topology.UnprepareHook} {
				if err := topology.PlanCleanup(revision, hook, "saved-execution"); err != nil {
					t.Fatal(err)
				}
				if err := topology.PlanCleanup(revision, hook, ""); err == nil {
					t.Fatal("cleanup accepted missing recorded execution")
				}
				corrupt := revision
				corrupt.Digest = "changed"
				if err := topology.PlanCleanup(corrupt, hook, "saved-execution"); err == nil {
					t.Fatal("cleanup accepted a changed saved contract")
				}
			}
			program, err := effects.ConnectNetwork(revision)
			if err != nil {
				t.Fatal(err)
			}
			if len(program.Effects) != 14 || len(program.Exports) != 6 {
				t.Fatalf("unexpected program: %+v", program)
			}
			program.Effects[0].Inputs = map[string]effects.Ref{"parent": {From: "effects.future.interface"}}
			if err := effects.Validate(program, revision.Topology.EffectBudget.Allow); err == nil {
				t.Fatal("forward effect reference accepted")
			}
		})
	}
	var value map[string]any
	if err := topology.Decode([]byte(`{"mode":"kernel","mode":"vfio"}`), &value); err == nil {
		t.Fatal("duplicate JSON field accepted")
	}
}

func TestAdministratorEffectProgram(t *testing.T) {
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := yaml.YAMLToJSON(data)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	pluginRaw := read("../../examples/effect-program/plugin.yaml")
	topologyRaw := read("../../examples/effect-program/topology.yaml")
	var plugin api.NetworkPlugin
	if err := topology.Decode(pluginRaw, &plugin); err != nil {
		t.Fatal(err)
	}
	plugins := map[string]api.NetworkPlugin{}
	for _, p := range topology.Builtins() {
		plugins[p.Name] = p
	}
	plugins[plugin.Name] = plugin
	var object api.NetworkTopology
	if err := topology.Decode(topologyRaw, &object); err != nil {
		t.Fatal(err)
	}
	revision, err := topology.Compile(object, plugins)
	if err != nil {
		t.Fatal(err)
	}
	if err := topology.VerifyRevision(revision); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{topology.RemoveHook, topology.UnprepareHook} {
		if err := topology.PlanCleanup(revision, hook, "saved-execution"); err != nil {
			t.Fatal(err)
		}
	}
	program, err := effects.ConnectNetwork(revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(program.Effects) != 10 || program.Effects[8].Parameters.VlanID != 100 || program.Effects[8].Inputs["parent"].From != "effects.uplink-configure.interface" {
		t.Fatalf("incorrect bound template: %+v", program)
	}
	for _, tc := range []struct{ name, old, new, reason string }{
		{"wrong binding type", `call.config.tag`, `call.config.ifName`, "E_TEMPLATE_BINDING_TYPE"},
		{"unknown opcode", `linux/create-vlan/v1`, `linux/shell/v1`, "E_TEMPLATE_OPCODE"},
		{"forward reference", `effects.create.interface`, `effects.configure.interface`, "E_EFFECT_REFERENCE"},
		{"unowned configure", `effects.create.interface`, `call.inputs.parent`, "E_TEMPLATE_GRANT"},
		{"literal binding collision", `"parameters":{}`, `"parameters":{"mtu":0}`, "E_CONFIG_SCHEMA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var invalid api.NetworkPlugin
			if err := topology.Decode([]byte(strings.ReplaceAll(string(pluginRaw), tc.old, tc.new)), &invalid); err != nil {
				t.Fatal(err)
			}
			err := topology.ValidatePlugin(invalid)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s, got %v", tc.reason, err)
			}
		})
	}
	for i := range object.Spec.Steps {
		if object.Spec.Steps[i].ID == "tenant" {
			object.Spec.Steps[i].Config.Raw = []byte(`{"ifName":"tenant100","mtu":1600,"up":true,"tag":100}`)
		}
	}
	if _, err := topology.Compile(object, plugins); err == nil || !strings.Contains(err.Error(), "E_MTU") {
		t.Fatalf("custom plugin bypassed composition validation: %v", err)
	}
}

func TestAdministratorLinkConfig(t *testing.T) {
	plugins := topology.PluginRegistry(nil)
	var plugin api.NetworkPlugin
	var object api.NetworkTopology
	for name, target := range map[string]any{"plugin": &plugin, "topology": &object} {
		data, err := os.ReadFile("../../examples/link-config/" + name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := yaml.YAMLToJSON(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := topology.Decode(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	plugins[plugin.Name] = plugin
	revision, err := topology.Compile(object, plugins)
	if err != nil {
		t.Fatal(err)
	}
	p, err := effects.ConnectNetwork(revision)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Effects[len(p.Effects)-1].Parameters
	if c.MAC != "02:42:ac:11:00:02" || c.Alias == nil || *c.Alias != "tenant100/app:blue" {
		t.Fatal("MAC/label did not bind from the locked descriptor")
	}
	for _, mac := range []string{"00:00:00:00:00:00", "01:42:ac:11:00:02"} {
		invalid := *object.DeepCopy()
		invalid.Spec.Steps[1].Config.Raw = []byte(strings.ReplaceAll(string(invalid.Spec.Steps[1].Config.Raw), "02:42:ac:11:00:02", mac))
		if _, err := topology.Compile(invalid, plugins); err == nil || !strings.Contains(err.Error(), "E_LINK_MAC") {
			t.Fatalf("unsafe MAC reached a revision: %v", err)
		}
	}
}

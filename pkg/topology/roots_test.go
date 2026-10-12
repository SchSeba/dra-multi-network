// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	"os"
	"strings"
	"testing"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
	"sigs.k8s.io/yaml"
)

func TestProgrammableDeviceProfileExamples(t *testing.T) {
	read := func(path string) [][]byte {
		t.Helper()
		data, err := os.ReadFile("../../examples/root-effect-program/" + path)
		if err != nil {
			t.Fatal(err)
		}
		reader := kyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
		var documents [][]byte
		for {
			data, err = reader.Read()
			if err == io.EOF {
				return documents
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := yaml.YAMLToJSON(data)
			if err != nil {
				t.Fatal(err)
			}
			documents = append(documents, raw)
		}
	}
	plugins := topology.PluginRegistry(nil)
	for _, raw := range read("device-plugins.yaml") {
		var p api.NetworkPlugin
		if err := topology.Decode(raw, &p); err != nil {
			t.Fatal(err)
		}
		if err := topology.ValidatePlugin(p); err != nil {
			t.Fatal(err)
		}
		plugins[p.Name] = p
	}
	counts := map[string]int{}
	for _, file := range []string{"device-vfio.yaml", "device-shared.yaml"} {
		for _, raw := range read(file) {
			var kind struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(raw, &kind); err != nil {
				t.Fatal(err)
			}
			if kind.Kind != "NetworkTopology" {
				continue
			}
			var source api.NetworkTopology
			if err := topology.Decode(raw, &source); err != nil {
				t.Fatal(err)
			}
			revision, err := topology.Compile(source, plugins)
			if err != nil {
				t.Fatalf("%s: %v", source.Name, err)
			}
			for _, hook := range []string{topology.RemoveHook, topology.UnprepareHook} {
				if err := topology.PlanCleanup(revision, hook, "saved-execution"); err != nil {
					t.Fatalf("%s %s: %v", source.Name, hook, err)
				}
			}
			prepare, err := effects.PrepareDevices(revision)
			if err != nil {
				t.Fatal(err)
			}
			connect, err := effects.ConnectNetwork(revision)
			if err != nil {
				t.Fatal(err)
			}
			if source.Name == "vfio-container-scope" {
				if len(prepare.Effects) != 4 || len(prepare.Exports) != 2 || len(connect.Effects) != 0 || len(connect.Exports) != 2 {
					t.Fatalf("invalid programmable VFIO lifecycle: %+v / %+v", prepare, connect)
				}
			} else if len(prepare.Effects) != 0 || len(connect.Effects) != 2 || len(connect.Exports) != 1 {
				t.Fatalf("invalid shared/kernel lifecycle: %s", source.Name)
			}
			counts[file]++
		}
	}
	if counts["device-vfio.yaml"] != 1 || counts["device-shared.yaml"] != 6 {
		t.Fatalf("missing examples: %v", counts)
	}
}

func TestAllocationEffectProgram(t *testing.T) {
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
	pluginRaw := read("../../examples/root-effect-program/plugin.yaml")
	topologyRaw := read("../../examples/root-effect-program/topology.yaml")
	for _, tc := range []struct {
		name, old, replacement, reason string
		changeTopology                 bool
	}{
		{name: "valid"},
		{"foreign allocation", `call.allocation`, `call.allocation.other`, "E_TEMPLATE_REFERENCE", false},
		{"forward reference", `effects.attach.interface`, `effects.ready.interface`, "E_TEMPLATE_REFERENCE", false},
		{"wrong output port", `effects.ready.interface`, `effects.ready.device`, "E_TEMPLATE_OUTPUT_CONFIG", false},
		{"caller cleanup targets", `"restoreRecordedHook":"ConnectNetwork"`, `"restoreRecordedHook":"ConnectNetwork","targets":["eth1"]`, "unknown field", false},
		{"wrong cleanup hook", `"restoreRecordedHook":"ConnectNetwork"`, `"restoreRecordedHook":"NodePrepare"`, "E_TEMPLATE_CLEANUP", false},
		{"changed allocation profile", `b385151d20cbddf1dd8864fa0424b62827d146c28d4b20fb13477c775ddab82d`, strings.Repeat("0", 64), "E_ALLOCATION_PROFILE", false},
		{"widened ownership", `"ownership":"Exclusive"`, `"ownership":"Created"`, "E_ALLOCATION_PROFILE", false},
		{"prepare host mutation", `"kind":"linux/driver-bind/v1"`, `"kind":"linux/link-move/v1"`, "E_TEMPLATE_SCOPE", false},
		{"prepare binding type", `call.config.selectedDriver`, `call.config.up`, "E_TEMPLATE_BINDING_TYPE", false},
		{"driver differs from grant", `"selectedDriver":"igbvf"`, `"selectedDriver":"vfio-pci"`, "E_TEMPLATE_OUTPUT_CONFIG", true},
		{name: "missing prepared driver", reason: "E_TEMPLATE_PREPARATION_COVERAGE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, source := pluginRaw, topologyRaw
			if tc.old != "" {
				if tc.changeTopology {
					source = []byte(strings.ReplaceAll(string(source), tc.old, tc.replacement))
				} else {
					raw = []byte(strings.ReplaceAll(string(raw), tc.old, tc.replacement))
				}
			}
			var plugin api.NetworkPlugin
			var object api.NetworkTopology
			if err := topology.Decode(raw, &plugin); err != nil {
				t.Fatal(err)
			}
			if tc.name == "missing prepared driver" {
				data, err := json.Marshal(map[string]any{"apiVersion": topology.EffectAPIVersion, "effects": []any{}, "exports": map[string]any{}})
				if err != nil {
					t.Fatal(err)
				}
				plugin.Spec.Hooks[topology.PrepareHook] = ext.JSON{Raw: data}
			}
			if err := topology.Decode(source, &object); err != nil {
				t.Fatal(err)
			}
			plugins := topology.PluginRegistry([]api.NetworkPlugin{plugin})
			revision, err := topology.Compile(object, plugins)
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
			prepare, err := effects.PrepareDevices(revision)
			if err != nil {
				t.Fatal(err)
			}
			if len(prepare.Effects) != 2 || prepare.Effects[0].ID != "left-prepare-driver" || prepare.Effects[1].ID != "right-prepare-driver" {
				t.Fatalf("incorrect preparation: %+v", prepare)
			}
			connect, err := effects.ConnectNetwork(revision)
			if err != nil {
				t.Fatal(err)
			}
			if len(connect.Effects) != 14 || len(connect.Exports) != 6 || connect.Effects[0].ID != "left-attach" {
				t.Fatalf("incorrect connection: %+v", connect)
			}
		})
	}
	t.Run("bound effect ID collision", func(t *testing.T) {
		var plugin api.NetworkPlugin
		var object api.NetworkTopology
		if err := topology.Decode(pluginRaw, &plugin); err != nil {
			t.Fatal(err)
		}
		if err := topology.Decode(topologyRaw, &object); err != nil {
			t.Fatal(err)
		}
		second := *plugin.DeepCopy()
		second.Name = "second-programmable-sriov"
		replaceHook := func(p *api.NetworkPlugin, id string) {
			hook := p.Spec.Hooks[topology.ConnectHook]
			hook.Raw = []byte(strings.ReplaceAll(strings.ReplaceAll(string(hook.Raw), `"id":"attach"`, `"id":"`+id+`"`), "effects.attach.interface", "effects."+id+".interface"))
			p.Spec.Hooks[topology.ConnectHook] = hook
		}
		replaceHook(&plugin, "a-b")
		replaceHook(&second, "b")
		object.Spec.Steps[1].ID = "left-a"
		object.Spec.Steps[1].PluginRef.Name = second.Name
		for i := range object.Spec.Steps {
			for name, raw := range object.Spec.Steps[i].Inputs {
				raw.Raw = []byte(strings.ReplaceAll(string(raw.Raw), "right.interface", "left-a.interface"))
				object.Spec.Steps[i].Inputs[name] = raw
			}
		}
		_, err := topology.Compile(object, topology.PluginRegistry([]api.NetworkPlugin{plugin, second}))
		if err == nil || !strings.Contains(err.Error(), "E_EFFECT_ID") {
			t.Fatalf("bound effect collision accepted before publication: %v", err)
		}
	})

}

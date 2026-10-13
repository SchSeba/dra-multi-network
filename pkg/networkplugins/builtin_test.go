// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package networkplugins

import (
	"encoding/json"
	"go/build"
	"testing"

	sdk "github.com/SchSeba/dra-multi-network/pkg/pluginapi"
)

func TestPluginPackagesHaveNoFrameworkDependencies(t *testing.T) {
	const contract = "github.com/SchSeba/dra-multi-network/pkg/pluginapi"
	for _, dir := range []string{".", "../pluginapi"} {
		pkg, err := build.Default.ImportDir(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, dependency := range pkg.Imports {
			if dependency == contract && dir == "." {
				continue
			}
			dep, err := build.Default.Import(dependency, "", build.FindOnly)
			if err != nil || !dep.Goroot {
				t.Fatalf("%s imports %s: plugins may depend only on the shared contract and standard library", dir, dependency)
			}
		}
	}
}

func TestTransportContractAndFrameworkAuthorityFence(t *testing.T) {
	call := sdk.Request{APIVersion: sdk.CallAPIVersion, ID: "left", Plugin: sriovPlugin, Version: "v1", Config: sdk.Config{Mode: "kernel", IfName: "net1", MTU: 1500, Up: true}, Allocation: sdk.EffectRef{From: "call.allocation.left"}}
	for _, hook := range []string{sdk.NodePrepare, sdk.ConnectNetwork, sdk.RemoveNetwork, sdk.NodeUnprepare} {
		call.Hook, call.RecordedExecution = hook, "saved-operation-uid"
		encoded, err := json.Marshal(call)
		if err != nil {
			t.Fatal(err)
		}
		var decoded sdk.Request
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		result, err := Run(decoded)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err = json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var response sdk.Result
		if err := json.Unmarshal(encoded, &response); err != nil {
			t.Fatal(err)
		}
		if err := sdk.ValidateResult(call, response); err != nil {
			t.Fatal(err)
		}
		if hook == sdk.ConnectNetwork {
			if len(response.Program.Effects) != 2 || response.Program.Effects[0].Inputs[sdk.InterfacePort] != call.Allocation || response.Program.Exports[sdk.InterfacePort].From != "effects.left-configure.interface" {
				t.Fatal("transport changed the locked realization")
			}
			response.Program.Effects[0].Inputs[sdk.InterfacePort] = sdk.EffectRef{From: "call.allocation.other"}
			if err := sdk.ValidateResult(call, response); err == nil {
				t.Fatal("plugin accessed another DRA root")
			}
		}
		if hook == sdk.RemoveNetwork || hook == sdk.NodeUnprepare {
			response.Program.Effects = []sdk.NetworkEffect{{ID: "fresh-delete-target"}}
			if err := sdk.ValidateResult(call, response); err == nil {
				t.Fatal("cleanup supplied new mutation targets")
			}
		}
	}
}

func TestAllBuiltinPluginsUseSerializableHookContract(t *testing.T) {
	for _, plugin := range []string{hostPlugin, sriovPlugin, macvlanPlugin, ipvlanPlugin, "bond-cni", "vlan-cni", "tuning-cni", "accelerated-bridge-cni", "ovs-cni", "ipam", "address-cni"} {
		for _, hook := range []string{sdk.NodePrepare, sdk.ConnectNetwork, sdk.RemoveNetwork, sdk.NodeUnprepare} {
			call := sdk.Request{APIVersion: sdk.CallAPIVersion, ID: "step", Plugin: plugin, Version: "v1", Hook: hook,
				Config: sdk.Config{Mode: "kernel", IfName: "net1", MTU: 1500, Up: true, Bridge: "fabric0", VlanID: 100}, Allocation: sdk.EffectRef{From: "call.allocation.step"},
				Inputs: map[string][]sdk.EffectRef{"members": {{From: "granted.left"}, {From: "granted.right"}}, "parent": {{From: "granted.parent"}}, "interface": {{From: "granted.interface"}}, "lease": {{From: "granted.lease"}}, sdk.RepresentorPort: {{From: "granted.representor"}}}, RecordedExecution: "recorded-execution"}
			if plugin == "tuning-cni" {
				call.Config = sdk.Config{IPv4Forward: new(true)}
			}
			data, err := json.Marshal(call)
			if err != nil {
				t.Fatal(err)
			}
			var decoded sdk.Request
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			result, err := Run(decoded)
			if err != nil {
				t.Fatal(plugin, hook, err)
			}
			data, err = json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var received sdk.Result
			if err := json.Unmarshal(data, &received); err != nil {
				t.Fatal(err)
			}
			if err := sdk.ValidateResult(call, received); err != nil {
				t.Fatal(plugin, hook, err)
			}
		}
	}
}

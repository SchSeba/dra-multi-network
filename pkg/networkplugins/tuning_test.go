// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package networkplugins

import (
	"encoding/json"
	"testing"

	sdk "github.com/SchSeba/dra-multi-network/pkg/pluginapi"
)

func TestTuningTransportAndAuthority(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		call := sdk.Request{APIVersion: sdk.CallAPIVersion, ID: "tune", Plugin: "tuning-cni", Version: "v1", Hook: sdk.ConnectNetwork, Config: sdk.Config{IPv4Forward: &enabled}, Inputs: map[string][]sdk.EffectRef{sdk.InterfacePort: {{From: "granted.vlan"}}}}
		data, err := json.Marshal(call)
		if err != nil {
			t.Fatal(err)
		}
		var received sdk.Request
		if err := json.Unmarshal(data, &received); err != nil {
			t.Fatal(err)
		}
		result, err := Run(received)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Program.Effects) != 1 || result.Program.Effects[0].Kind != sdk.IPv4Forwarding || result.Program.Effects[0].Parameters.IPv4Forward == nil || *result.Program.Effects[0].Parameters.IPv4Forward != enabled || result.Program.Effects[0].Inputs[sdk.InterfacePort].From != "granted.vlan" || result.Program.Exports["configured"].From != "effects.tune-forwarding.interface" {
			t.Fatalf("transport changed forwarding config or grant: %+v", result)
		}
		result.Program.Effects[0].Inputs[sdk.InterfacePort] = sdk.EffectRef{From: "granted.foreign"}
		if err := sdk.ValidateResult(received, result); err == nil {
			t.Fatal("plugin escaped its interface grant")
		}
		call.Config.IPv4Forward = nil
		if _, err := Run(call); err == nil {
			t.Fatal("accepted missing forwarding value")
		}
		call.Config.IPv4Forward = &enabled
		for _, refs := range [][]sdk.EffectRef{nil, {{From: "granted.vlan"}, {From: "granted.other"}}} {
			call.Inputs[sdk.InterfacePort] = refs
			if _, err := Run(call); err == nil {
				t.Fatal("accepted invalid interface cardinality")
			}
		}
	}
}

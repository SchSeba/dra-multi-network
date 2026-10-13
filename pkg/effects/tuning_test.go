// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"testing"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestForwardingScopeAndPayload(t *testing.T) {
	owned := Handle{Type: topology.InterfaceType, Scope: SandboxScope, Alias: "dra-mn:claim:vlan"}
	if err := forwardingGrant(owned); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"shared", "host", "representor", "unowned"} {
		h := owned
		switch field {
		case "shared":
			h.Shared = true
		case "host":
			h.Scope = HostScope
		case "representor":
			h.Type = topology.RepresentorType
		case "unowned":
			h.Alias = "foreign"
		}
		if err := forwardingGrant(h); err == nil {
			t.Fatalf("accepted %s forwarding target", field)
		}
	}
	effect := Effect{ID: "tune", Kind: topology.IPv4Forwarding, Parameters: topology.Config{IPv4Forward: new(false)}, Inputs: map[string]Ref{InterfacePort: {From: "call.allocation.vlan"}}, Requires: []Predicate{{Kind: GrantedInputs}}, Postconditions: []Predicate{{Kind: NativeStateMatch}}, Recovery: Recovery{Strategy: ObserveAndReconcile}}
	p := Program{APIVersion: APIVersion, Effects: []Effect{effect}}
	if err := Validate(p, []string{topology.IPv4Forwarding}); err != nil {
		t.Fatal("explicit false rejected", err)
	}
	for _, field := range []string{"missing", "extra config", "extra input", "wrong opcode"} {
		invalid := effect
		switch field {
		case "missing":
			invalid.Parameters.IPv4Forward = nil
		case "extra config":
			invalid.Parameters.IfName = "eth0"
		case "extra input":
			invalid.Inputs = map[string]Ref{InterfacePort: {From: "call.allocation.vlan"}, "parent": {From: "call.allocation.other"}}
		case "wrong opcode":
			invalid.Kind = Configure
		}
		p.Effects[0] = invalid
		if err := Validate(p, []string{topology.IPv4Forwarding, Configure}); err == nil {
			t.Fatalf("accepted %s payload", field)
		}
	}
	n := &Native{}
	if err := n.restoreIPv4Forwarding(Record{Output: owned}); err == nil {
		t.Fatal("restored without a recorded before-image")
	}
}

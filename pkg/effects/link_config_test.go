// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"strings"
	"testing"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestLinkConfigAuthority(t *testing.T) {
	label := "tenant100/app:blue"
	if err := topology.ValidateLinkConfig(topology.Config{MAC: "02:42:ac:11:00:02", Alias: &label}); err != nil {
		t.Fatal(err)
	}
	for _, mac := range []string{"00:00:00:00:00:00", "01:42:ac:11:00:02", "ff:ff:ff:ff:ff:ff", "02:42:AC:11:00:02", "02:42:ac:11:00:02:00:01", "invalid"} {
		if err := topology.ValidateLinkConfig(topology.Config{MAC: mac}); err == nil {
			t.Fatalf("accepted MAC %q", mac)
		}
	}
	for _, label := range []string{"tenant;dra-mn:foreign", "tenant\nother", "tenant\x00other", string(make([]byte, 129))} {
		if err := topology.ValidateLinkConfig(topology.Config{Alias: &label}); err == nil {
			t.Fatalf("accepted label %q", label)
		}
	}
	identity := "dra-mn:operation:create"
	if ownershipAlias(labeledAlias(identity, label)) != identity || labeledAlias(identity, "") != identity || ownershipAlias("external;owner") != "external;owner" {
		t.Fatal("label replaced ownership identity")
	}
	p := Program{APIVersion: APIVersion, Effects: []Effect{{ID: "create", Kind: CreateVLAN, Parameters: topology.Config{MAC: "02:42:ac:11:00:02"}}}}
	if err := Validate(p, []string{CreateVLAN}); err == nil {
		t.Fatal("MAC escaped link-configure opcode")
	}
	for _, kind := range []string{CreateBond, CreateIPvlan} {
		p.Effects = []Effect{
			{ID: "create", Kind: kind, Requires: []Predicate{{Kind: GrantedInputs}}, Postconditions: []Predicate{{Kind: NativeStateMatch}}, Recovery: Recovery{Strategy: ObserveAndReconcile}},
			{ID: "configure", Kind: Configure, Inputs: map[string]Ref{InterfacePort: {From: "effects.create.interface"}}, Parameters: topology.Config{MAC: "02:42:ac:11:00:02"}},
		}
		if err := Validate(p, []string{kind, Configure}); err == nil || !strings.Contains(err.Error(), "E_LINK_MAC_PROFILE") {
			t.Fatalf("independent MAC profile check missing for %s: %v", kind, err)
		}
	}
	for _, h := range []Handle{{Shared: true}, {ParentPCI: "0000:29:00.0"}, {ParentPCI: "0000:29:00.0", AllowedVFSettings: []string{"trust"}}} {
		if err := linkMACGrant(h, topology.Config{MAC: "02:42:ac:11:00:02"}); err == nil {
			t.Fatal("MAC change bypassed shared-parent/VF policy authority")
		}
	}
	if err := linkMACGrant(Handle{ParentPCI: "0000:29:00.0", AllowedVFSettings: []string{"mac"}}, topology.Config{MAC: "02:42:ac:11:00:02"}); err != nil {
		t.Fatal(err)
	}
	if err := linkMACGrant(Handle{PCIAddress: "0000:29:00.0", ParentPCI: "0000:29:00.0"}, topology.Config{MAC: "02:42:ac:11:00:02"}); err != nil {
		t.Fatal("PF identity incorrectly required a VF setting grant", err)
	}
}

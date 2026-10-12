// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"testing"

	"github.com/SchSeba/dra-multi-network/pkg/ipam"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestAddressGrantCannotMutateSharedOrForeignInterface(t *testing.T) {
	inputs := map[string]Handle{InterfacePort: {Type: topology.InterfaceType, Scope: SandboxScope, Alias: "dra-mn:attachment:link"}, topology.LeasePort: {Type: topology.AddressLeaseType, Address: "192.0.2.2/29", Lease: &ipam.Lease{Address: "192.0.2.2/29"}}}
	if _, err := addressGrant(inputs); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]Handle){
		func(in map[string]Handle) { h := in[InterfacePort]; h.Shared = true; in[InterfacePort] = h },
		func(in map[string]Handle) { h := in[InterfacePort]; h.Scope = HostScope; in[InterfacePort] = h },
		func(in map[string]Handle) { h := in[InterfacePort]; h.Alias = "foreign"; in[InterfacePort] = h },
		func(in map[string]Handle) {
			h := in[topology.LeasePort]
			h.Address = "192.0.2.3/29"
			in[topology.LeasePort] = h
		},
	} {
		in := map[string]Handle{}
		for key, h := range inputs {
			in[key] = h
		}
		change(in)
		if _, err := addressGrant(in); err == nil {
			t.Fatal("foreign address mutation permitted")
		}
	}
}

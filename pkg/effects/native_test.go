// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestNativeBondVLANLifecycle(t *testing.T) {
	if os.Getenv("DRA_NATIVE_TEST") != "1" {
		t.Skip("requires root and Linux namespace/mount capabilities; DRA_NATIVE_TEST=1")
	}
	dir := t.TempDir()
	sandbox, stage := filepath.Join(dir, "sandbox"), filepath.Join(dir, "stage")
	for _, path := range []string{sandbox, stage} {
		if err := CreateStagingNamespace(path); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := Unpin(path); err != nil {
				t.Error(err)
			}
		})
	}

	if err := PinNamespace(sandbox, sandbox); err != nil {
		t.Fatal(err)
	}
	if err := PinNamespace(stage, sandbox); err == nil {
		t.Fatal("different namespace adopted through existing pin")
	}
	n, err := OpenNative(sandbox, stage)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	hostForwarding, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		t.Fatal(err)
	}
	p := Program{APIVersion: APIVersion, Exports: map[string]Ref{"interface": {From: "effects.forwarding.interface"}}}
	add := func(id, kind string, c topology.Config, in map[string]Ref) {
		p.Effects = append(p.Effects, Effect{ID: id, Kind: kind, Parameters: c, Inputs: in, Requires: []Predicate{{Kind: "allocation-and-owned-inputs"}}, Postconditions: []Predicate{{Kind: "native-state-match"}}, Recovery: Recovery{Strategy: "ObserveAndReconcile"}})
	}
	add("bond", CreateBond, topology.Config{IfName: "bond0", MTU: 1500, Mode: "active-backup", MonitorIntervalMs: 100, FailOverMac: "active"}, nil)
	add("bond-state", Configure, topology.Config{IfName: "bond0", MTU: 1500, Up: true}, map[string]Ref{InterfacePort: {From: "effects.bond.interface"}})
	add("vlan", CreateVLAN, topology.Config{IfName: "tenant100", MTU: 1500, VlanID: 100}, map[string]Ref{"parent": {From: "effects.bond-state.interface"}})
	label := "tenant100/app:blue"
	add("vlan-state", Configure, topology.Config{IfName: "tenant100", MTU: 1500, Up: true, MAC: "02:42:ac:11:00:02", Alias: &label}, map[string]Ref{InterfacePort: {From: "effects.vlan.interface"}})
	add("forwarding", topology.IPv4Forwarding, topology.Config{IPv4Forward: new(true)}, map[string]Ref{InterfacePort: {From: "effects.vlan-state.interface"}})
	if err := Validate(p, []string{CreateBond, Configure, CreateVLAN, topology.IPv4Forwarding}); err != nil {
		t.Fatal(err)
	}
	ex := Execution{ID: "native-integration", Program: p}
	saves := 0
	save := func() error { saves++; return nil }
	if err := n.Preflight(ex); err != nil {
		t.Fatal(err)
	}
	if err := n.Execute(&ex, save); err != nil {
		t.Fatal(err)
	}
	if err := n.Verify(ex); err != nil {
		t.Fatal(err)
	}
	// Recovery uses saved handles, not a fresh interface-name guess. The user
	// label must not hide the link from earlier effects or reverse cleanup.
	data, err := json.Marshal(ex)
	if err != nil {
		t.Fatal(err)
	}
	var recovered Execution
	if err := json.Unmarshal(data, &recovered); err != nil {
		t.Fatal(err)
	}
	if err := n.Execute(&recovered, save); err != nil {
		t.Fatal(err)
	}
	if err := n.Verify(recovered); err != nil {
		t.Fatal(err)
	}
	h := recovered.Outputs[InterfacePort]
	if _, err := n.ipv4Forwarding(h, new(false)); err != nil {
		t.Fatal(err)
	}
	if err := n.Verify(recovered); err == nil {
		t.Fatal("forwarding drift passed readiness")
	}
	if err := n.Execute(&recovered, save); err != nil {
		t.Fatal(err)
	}
	// A failed journal checkpoint must leave the sysctl untouched.
	intent := p.Effects[len(p.Effects)-1]
	intent.Inputs = map[string]Ref{InterfacePort: {From: "call.allocation.vlan"}}
	intent.Parameters.IPv4Forward = new(false)
	failed := Execution{ID: "failed-checkpoint", Roots: map[string]Handle{"vlan": h}, Program: Program{APIVersion: APIVersion, Effects: []Effect{intent}}}
	checkpointErr := errors.New("checkpoint failed")
	if err := n.Execute(&failed, func() error { return checkpointErr }); !errors.Is(err, checkpointErr) {
		t.Fatalf("unexpected checkpoint failure: %v", err)
	}
	if actual, err := n.ipv4Forwarding(h, nil); err != nil || !actual {
		t.Fatalf("mutated before checkpoint: %v", err)
	}
	failed.Records = nil // Reloading after a failed checkpoint has no saved intent.
	if err := n.Execute(&failed, save); err != nil {
		t.Fatal(err)
	}
	if actual, err := n.ipv4Forwarding(h, nil); err != nil || actual {
		t.Fatalf("explicit false did not disable forwarding: %v", err)
	}
	if err := n.Restore(&failed, save); err != nil {
		t.Fatal(err)
	}
	if err := n.Verify(recovered); err != nil {
		t.Fatal("before-image was not restored", err)
	}
	if data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err != nil || string(data) != string(hostForwarding) {
		t.Fatal("sandbox forwarding changed the host namespace", err)
	}
	configured, err := n.link(recovered.Outputs[InterfacePort])
	if err != nil {
		t.Fatal(err)
	}
	if configured.Attrs().HardwareAddr.String() != "02:42:ac:11:00:02" || configured.Attrs().Alias != labeledAlias(recovered.Outputs[InterfacePort].Alias, label) {
		t.Fatal("MAC/label postconditions missing")
	}
	duplicate := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "foreign-copy", Alias: labeledAlias(recovered.Outputs[InterfacePort].Alias, "another-label")}}
	if err := n.sandbox.LinkAdd(duplicate); err != nil {
		t.Fatal(err)
	}
	// LinkAdd does not reliably serialize ifalias in this netlink version.
	if err := n.sandbox.LinkSetAlias(duplicate, duplicate.Attrs().Alias); err != nil {
		t.Fatal(err)
	}
	if _, err := n.link(recovered.Outputs[InterfacePort]); err == nil {
		t.Fatal("adopted a duplicated ownership identity")
	}
	if err := n.sandbox.LinkDel(duplicate); err != nil {
		t.Fatal(err)
	}
	if err := n.sandbox.LinkSetAlias(configured, "external-owner"); err != nil {
		t.Fatal(err)
	}
	if err := n.Verify(recovered); err == nil {
		t.Fatal("adopted a link after its ownership identity changed")
	}
	if err := n.sandbox.LinkSetAlias(configured, labeledAlias(recovered.Outputs[InterfacePort].Alias, label)); err != nil {
		t.Fatal(err)
	}
	forwardingIndex := len(recovered.Records) - 1
	forwarding := Execution{Records: []Record{recovered.Records[forwardingIndex]}}
	if forwarding.Records[0].IPv4ForwardBefore == nil {
		t.Fatal("forwarding before-image lost during JSON recovery")
	}
	if err := n.Restore(&forwarding, save); err != nil {
		t.Fatal(err)
	}
	if actual, err := n.ipv4Forwarding(h, nil); err != nil || actual != *forwarding.Records[0].IPv4ForwardBefore {
		t.Fatal("forwarding before-image was not restored", err)
	}
	configureIndex := forwardingIndex - 1
	last := Execution{Records: []Record{recovered.Records[configureIndex]}}
	if err := n.Restore(&last, save); err != nil {
		t.Fatal(err)
	}
	configured, err = n.link(recovered.Outputs[InterfacePort])
	if err != nil || state(configured) != *last.Records[0].Before {
		t.Fatalf("configure did not restore its exact MAC/alias/MTU/state before-image: %v", err)
	}
	ex = recovered
	ex.Records[configureIndex] = last.Records[0]
	ex.Records[forwardingIndex] = forwarding.Records[0]
	if saves < 10 {
		t.Fatalf("missing durable checkpoints: %d", saves)
	}
	if err := n.Restore(&ex, save); err != nil {
		t.Fatal(err)
	}
	if err := n.Restore(&ex, save); err != nil {
		t.Fatal(err)
	}
	links, err := n.sandbox.LinkList()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Attrs().Name != "lo" {
		t.Fatalf("leaked links: %v", links)
	}
}

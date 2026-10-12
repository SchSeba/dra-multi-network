// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"encoding/json"
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
	p := Program{APIVersion: APIVersion, Exports: map[string]Ref{"interface": {From: "effects.vlan-state.interface"}}}
	add := func(id, kind string, c topology.Config, in map[string]Ref) {
		p.Effects = append(p.Effects, Effect{ID: id, Kind: kind, Parameters: c, Inputs: in, Requires: []Predicate{{Kind: "allocation-and-owned-inputs"}}, Postconditions: []Predicate{{Kind: "native-state-match"}}, Recovery: Recovery{Strategy: "ObserveAndReconcile"}})
	}
	add("bond", CreateBond, topology.Config{IfName: "bond0", MTU: 1500, Mode: "active-backup", MonitorIntervalMs: 100, FailOverMac: "active"}, nil)
	add("bond-state", Configure, topology.Config{IfName: "bond0", MTU: 1500, Up: true}, map[string]Ref{InterfacePort: {From: "effects.bond.interface"}})
	add("vlan", CreateVLAN, topology.Config{IfName: "tenant100", MTU: 1500, VlanID: 100}, map[string]Ref{"parent": {From: "effects.bond-state.interface"}})
	label := "tenant100/app:blue"
	add("vlan-state", Configure, topology.Config{IfName: "tenant100", MTU: 1500, Up: true, MAC: "02:42:ac:11:00:02", Alias: &label}, map[string]Ref{InterfacePort: {From: "effects.vlan.interface"}})
	if err := Validate(p, []string{CreateBond, Configure, CreateVLAN}); err != nil {
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
	last := Execution{Records: []Record{recovered.Records[len(recovered.Records)-1]}}
	if err := n.Restore(&last, save); err != nil {
		t.Fatal(err)
	}
	configured, err = n.link(recovered.Outputs[InterfacePort])
	if err != nil || state(configured) != *last.Records[0].Before {
		t.Fatalf("configure did not restore its exact MAC/alias/MTU/state before-image: %v", err)
	}
	ex = recovered
	ex.Records[len(ex.Records)-1] = last.Records[0]
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

// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"net"
	"os"
	"slices"
	"testing"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"k8s.io/utils/ptr"
)

func mustMAC(t *testing.T, address string) net.HardwareAddr {
	t.Helper()
	mac, err := net.ParseMAC(address)
	if err != nil {
		t.Fatal(err)
	}
	return mac
}

func TestBridgeVLANRangeAndForeignState(t *testing.T) {
	v, err := normalizePortVLANs([]*nl.BridgeVlanInfo{{Vid: 100, Flags: nl.BRIDGE_VLAN_INFO_RANGE_BEGIN}, {Vid: 102, Flags: nl.BRIDGE_VLAN_INFO_RANGE_END}, {Vid: 200, Flags: nl.BRIDGE_VLAN_INFO_PVID | nl.BRIDGE_VLAN_INFO_UNTAGGED}})
	if err != nil || !slices.Equal(v, []PortVLAN{{ID: 100}, {ID: 101}, {ID: 102}, {ID: 200, Flags: nl.BRIDGE_VLAN_INFO_PVID | nl.BRIDGE_VLAN_INFO_UNTAGGED}}) {
		t.Fatalf("range/PVID normalization: %v %v", v, err)
	}
	if _, err := normalizePortVLANs([]*nl.BridgeVlanInfo{{Vid: 100, Flags: nl.BRIDGE_VLAN_INFO_RANGE_END}}); err == nil {
		t.Fatal("unpaired range accepted")
	}
	r := Record{Effect: Effect{Parameters: topology.Config{MTU: 1500}}, Output: Handle{IfIndex: 10, Alias: "dra-mn:owned"}, Bridge: &BridgePortState{Before: LinkState{Name: "rep", MTU: 9000, MAC: "02:00:00:00:00:01"}, BridgeIndex: 20, DefaultPVID: 1, TargetVLANs: []PortVLAN{{ID: 100}}}}
	l := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "rep", Index: 10, Alias: r.Output.Alias, MTU: 1500, HardwareAddr: mustMAC(t, r.Bridge.Before.MAC), MasterIndex: 20}}
	if err := bridgePortGuard(r, l, []PortVLAN{{ID: 999}}); err == nil {
		t.Fatal("foreign VLAN adopted")
	}
	l.Attrs().MTU = 1600
	if err := bridgePortGuard(r, l, nil); err == nil {
		t.Fatal("foreign MTU adopted")
	}
}

func TestNativeApprovedBridgePortLifecycle(t *testing.T) {
	if os.Getenv("DRA_NATIVE_TEST") != "1" {
		t.Skip("requires root/native netlink; no runtime binary is launched")
	}
	n, err := OpenHostNative()
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	for _, name := range []string{"dra-abtest-br", "dra-abtest-rep"} {
		if _, err := n.host.LinkByName(name); err == nil {
			t.Fatalf("test target %s already exists", name)
		}
	}
	b := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "dra-abtest-br", MTU: 1500}, VlanFiltering: ptr.To(true), VlanDefaultPVID: ptr.To(uint16(1))}
	if err := n.host.LinkAdd(b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := n.host.LinkDel(b); err != nil {
			t.Error(err)
		}
	})
	if err := n.host.LinkSetHardwareAddr(b, mustMAC(t, "02:42:00:00:00:fe")); err != nil {
		t.Fatal(err)
	}
	if err := n.host.LinkSetAlias(b, "test/fabric"); err != nil {
		t.Fatal(err)
	}
	if err := n.host.LinkSetUp(b); err != nil {
		t.Fatal(err)
	}
	l := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "dra-abtest-rep", MTU: 9000}}
	if err := n.host.LinkAdd(l); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := n.host.LinkDel(l); err != nil {
			t.Error(err)
		}
	})
	if err := n.host.LinkSetUp(l); err != nil {
		t.Fatal(err)
	}
	live, err := n.host.LinkByName(l.Name)
	if err != nil {
		t.Fatal(err)
	}
	input := Handle{Type: topology.RepresentorType, Scope: HostScope, Name: l.Name, IfIndex: live.Attrs().Index, AllowedHostBridges: []api.HostBridgeGrant{{Name: b.Name, Alias: "test/fabric", VLANs: []int{100, 200, 300}}}}
	c := topology.Config{Bridge: b.Name, MTU: 1500, Up: true, VlanID: 100, Trunk: []int{200, 300}}
	before, err := n.captureBridgePort(input, live, c)
	if err != nil {
		t.Fatal(err)
	}
	r := Record{Effect: Effect{Kind: topology.BridgeConfigure, Parameters: c}, Output: input, Bridge: before, State: "Applying"}
	r.Output.Alias = "dra-mn:native-bridge-test"
	save := func() error { return nil }
	if err := n.applyBridgePort(&r, live, save); err != nil {
		t.Fatal(err)
	}
	live, err = n.host.LinkByIndex(input.IfIndex)
	if err != nil {
		t.Fatal(err)
	}
	// Lost acknowledgement: persisted Applying intent observes the existing
	// owned port/table and safely completes without adopting a foreign port.
	if err := n.applyBridgePort(&r, live, save); err != nil {
		t.Fatal(err)
	}
	r.State = Applied
	if err := n.host.BridgeVlanAdd(live, 999, false, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := n.restoreBridgePort(&r, live, save); err == nil {
		t.Fatal("cleanup overwrote an external VLAN")
	}
	if err := n.host.BridgeVlanDel(live, 999, false, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := n.restoreBridgePort(&r, live, save); err != nil {
		t.Fatal(err)
	}
	live, err = n.host.LinkByIndex(input.IfIndex)
	if err != nil {
		t.Fatal(err)
	}
	if state(live) != before.Before {
		t.Fatalf("before-image not restored: %+v / %+v", state(live), before.Before)
	}
	if err := n.restoreBridgePort(&r, live, save); err != nil {
		t.Fatal(err)
	}
	if _, err := n.mapRepresentor(input); err == nil {
		t.Fatal("dummy interface became a representor grant")
	}
}

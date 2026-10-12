// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"github.com/SchSeba/dra-multi-network/pkg/journal"
	"github.com/vishvananda/netlink"
	"k8s.io/utils/ptr"
	"os"
	"testing"
)

func TestNativeSharedUplinkVLANOwnership(t *testing.T) {
	if os.Getenv("DRA_NATIVE_TEST") != "1" {
		t.Skip("requires root/native netlink; no runtime binary is launched")
	}
	n, err := OpenHostNative()
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	n.UplinkStore = store
	for _, name := range []string{"dra-ultest-br", "dra-ultest-up", "dra-ultest-port"} {
		if _, err := n.host.LinkByName(name); err == nil {
			t.Fatal("test target exists", name)
		}
	}
	b := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "dra-ultest-br", MTU: 1500}, VlanFiltering: ptr.To(true), VlanDefaultPVID: ptr.To(uint16(1))}
	if err := n.host.LinkAdd(b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := n.host.LinkDel(b); err != nil {
			t.Error(err)
		}
	})
	if err := n.host.LinkSetHardwareAddr(b, mustMAC(t, "02:42:00:00:00:fd")); err != nil {
		t.Fatal(err)
	}
	if err := n.host.LinkSetUp(b); err != nil {
		t.Fatal(err)
	}
	var ports []netlink.Link
	for _, name := range []string{"dra-ultest-up", "dra-ultest-port"} {
		l := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name, MTU: 1500}}
		if err := n.host.LinkAdd(l); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := n.host.LinkDel(l); err != nil {
				t.Error(err)
			}
		})
		if err := n.host.LinkSetMaster(l, b); err != nil {
			t.Fatal(err)
		}
		if err := n.host.LinkSetUp(l); err != nil {
			t.Fatal(err)
		}
		live, err := n.host.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, live)
	}
	bLive, err := n.host.LinkByName(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	up, foreign := ports[0], ports[1]
	capture := func(id uint16) UplinkLease {
		t.Helper()
		x, err := n.captureUplinkSlots(bLive, up, "temporary-linux-check", []PortVLAN{{ID: id}})
		if err != nil {
			t.Fatal(err)
		}
		return x[0]
	}
	a := capture(100)
	if err := n.acquireUplink(a, "dra-mn:one"); err != nil {
		t.Fatal(err)
	}
	// A second attachment must inherit the original empty before-image.
	second := capture(100)
	if second.Before != nil {
		t.Fatal("owned VLAN adopted as administrator baseline")
	}
	if err := n.acquireUplink(second, "dra-mn:two"); err != nil {
		t.Fatal(err)
	}
	pending, _, err := n.uplinkRecord(a)
	if err != nil {
		t.Fatal(err)
	}
	pending.Phase = Applying
	if err := store.Put(a.Key, pending); err != nil {
		t.Fatal(err)
	}
	if err := n.acquireUplink(a, "dra-mn:one"); err != nil {
		t.Fatal("lost acquire ack", err)
	}
	if err := n.releaseUplink(a, "dra-mn:one"); err != nil {
		t.Fatal(err)
	}
	if actual, err := n.uplinkVID(a); err != nil || actual == nil {
		t.Fatal("first owner removed shared VLAN", err)
	}
	if err := n.host.BridgeVlanAdd(foreign, 100, false, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := n.releaseUplink(second, "dra-mn:two"); err == nil {
		t.Fatal("foreign dependency removed")
	}
	if err := n.host.BridgeVlanDel(foreign, 100, false, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := n.releaseUplink(second, "dra-mn:two"); err != nil {
		t.Fatal(err)
	}
	if actual, err := n.uplinkVID(a); err != nil || actual != nil {
		t.Fatal("last owner did not remove owned VLAN", err)
	}
	if err := n.releaseUplink(second, "dra-mn:two"); err != nil {
		t.Fatal("lost release ack", err)
	}
	// Preserve an existing administrator VID and unrelated default PVID.
	if err := n.host.BridgeVlanAdd(up, 200, false, false, false, false); err != nil {
		t.Fatal(err)
	}
	admin := capture(200)
	if admin.Before == nil {
		t.Fatal("admin before-image absent")
	}
	if err := n.acquireUplink(admin, "dra-mn:admin"); err != nil {
		t.Fatal(err)
	}
	if err := n.releaseUplink(admin, "dra-mn:admin"); err != nil {
		t.Fatal(err)
	}
	if actual, err := n.uplinkVID(admin); err != nil || actual == nil {
		t.Fatal("administrator VLAN removed", err)
	}
	// Foreign flags fence release; a saved Removing intent observes a lost delete ack.
	lost := capture(300)
	if err := n.acquireUplink(lost, "dra-mn:lost"); err != nil {
		t.Fatal(err)
	}
	if err := n.host.BridgeVlanAdd(up, 300, false, true, false, false); err != nil {
		t.Fatal(err)
	}
	if err := n.releaseUplink(lost, "dra-mn:lost"); err == nil {
		t.Fatal("foreign flags ignored")
	}
	if err := n.host.BridgeVlanAdd(up, 300, false, false, false, false); err != nil {
		t.Fatal(err)
	}
	saved, _, err := n.uplinkRecord(lost)
	if err != nil {
		t.Fatal(err)
	}
	saved.Phase = uplinkRemoving
	if err := store.Put(lost.Key, saved); err != nil {
		t.Fatal(err)
	}
	if err := n.host.BridgeVlanDel(up, 300, false, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := n.releaseUplink(lost, "dra-mn:lost"); err != nil {
		t.Fatal("lost delete ack", err)
	}
	vlans, err := portVLANs(n.host, up.Attrs().Index)
	if err != nil || len(vlans) != 2 || vlans[0].ID != 1 || vlans[1].ID != 200 {
		t.Fatal("foreign/admin VLAN table changed", vlans, err)
	}
}

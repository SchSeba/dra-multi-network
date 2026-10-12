// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"k8s.io/utils/ptr"
)

func representorFixture(t *testing.T) (string, *netlink.DevlinkPort) {
	t.Helper()
	root := t.TempDir()
	for _, pci := range []string{"0000:29:00.0", "0000:29:10.0", "0000:30:00.0"} {
		if err := os.MkdirAll(filepath.Join(root, "bus/pci/devices", pci), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{{"0000:29:00.0", "0000:29:10.0/physfn"}, {"0000:29:10.0", "0000:29:00.0/virtfn0"}} {
		if err := os.Symlink(filepath.Join(root, "bus/pci/devices", pair[0]), filepath.Join(root, "bus/pci/devices", pair[1])); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"orange", "uplink-a", "uplink-b"} {
		base := filepath.Join(root, "class/net", name)
		if err := os.MkdirAll(base, 0700); err != nil {
			t.Fatal(err)
		}
		index := map[string]string{"orange": "200\n", "uplink-a": "101\n", "uplink-b": "102\n"}[name]
		for field, value := range map[string]string{"type": "1\n", "ifindex": index} {
			if err := os.WriteFile(filepath.Join(base, field), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, pci := range map[string]string{"uplink-a": "0000:29:00.0", "uplink-b": "0000:30:00.0"} {
		if err := os.Symlink(filepath.Join(root, "bus/pci/devices", pci), filepath.Join(root, "class/net", name, "device")); err != nil {
			t.Fatal(err)
		}
	}
	return root, &netlink.DevlinkPort{BusName: "pci", DeviceName: "0000:29:00.0", PortIndex: 75, PortType: nl.DEVLINK_PORT_TYPE_ETH, PortFlavour: nl.DEVLINK_PORT_FLAVOUR_PCI_VF, ControllerNumber: ptr.To(uint32(0)), PfNumber: ptr.To(uint16(0)), VfNumber: ptr.To(uint16(0)), External: ptr.To(false), NetdeviceName: "orange", NetdevIfIndex: 200}
}

func TestRepresentorMappingRequiresLocalKernelAndBidirectionalPCIIdentity(t *testing.T) {
	root, port := representorFixture(t)
	identity, err := RepresentorForVF(root, []*netlink.DevlinkPort{port}, "0000:29:10.0", "0000:29:00.0", 0)
	if err != nil || identity == nil || identity.Name != "orange" || identity.VFPCI != "0000:29:10.0" || identity.PFNumber != 0 || identity.PortIndex != 75 || identity.IfIndex != 200 {
		t.Fatalf("explicit mapping without a representor device symlink or name convention: %+v %v", identity, err)
	}
	for name, change := range map[string]func(*netlink.DevlinkPort){
		"remote controller":  func(p *netlink.DevlinkPort) { p.ControllerNumber = ptr.To(uint32(1)) },
		"external":           func(p *netlink.DevlinkPort) { p.External = ptr.To(true) },
		"missing controller": func(p *netlink.DevlinkPort) { p.ControllerNumber = nil },
		"missing external":   func(p *netlink.DevlinkPort) { p.External = nil },
		"missing PF":         func(p *netlink.DevlinkPort) { p.PfNumber = nil },
		"other local PF":     func(p *netlink.DevlinkPort) { p.PfNumber = ptr.To(uint16(1)) },
		"missing VF":         func(p *netlink.DevlinkPort) { p.VfNumber = nil },
		"wrong VF":           func(p *netlink.DevlinkPort) { p.VfNumber = ptr.To(uint16(1)) },
		"other PF":           func(p *netlink.DevlinkPort) { p.DeviceName = "0000:30:00.0" },
		"SF":                 func(p *netlink.DevlinkPort) { p.PortFlavour = nl.DEVLINK_PORT_FLAVOUR_PCI_SF },
		"IB":                 func(p *netlink.DevlinkPort) { p.PortType = nl.DEVLINK_PORT_TYPE_IB },
		"missing netdevice":  func(p *netlink.DevlinkPort) { p.NetdeviceName = "" },
		"unsafe netdevice":   func(p *netlink.DevlinkPort) { p.NetdeviceName = "../orange" },
	} {
		t.Run(name, func(t *testing.T) {
			other := *port
			change(&other)
			identity, err := RepresentorForVF(root, []*netlink.DevlinkPort{&other}, "0000:29:10.0", "0000:29:00.0", 0)
			if identity != nil || err != nil {
				t.Fatalf("uncertified port became an allocation mapping: %+v %v", identity, err)
			}
		})
	}
	duplicate := *port
	duplicate.PortIndex++
	if _, err := RepresentorForVF(root, []*netlink.DevlinkPort{port, &duplicate}, "0000:29:10.0", "0000:29:00.0", 0); err == nil {
		t.Fatal("ambiguous VF mapping accepted")
	}
	duplicate.VfNumber = ptr.To(uint16(1))
	if _, err := RepresentorForVF(root, []*netlink.DevlinkPort{port, &duplicate}, "0000:29:10.0", "0000:29:00.0", 0); err == nil {
		t.Fatal("one host representor assigned to distinct VFs")
	}
	wrong := *port
	wrong.NetdevIfIndex++
	if _, err := RepresentorForVF(root, []*netlink.DevlinkPort{&wrong}, "0000:29:10.0", "0000:29:00.0", 0); err == nil {
		t.Fatal("stale netdevice index accepted")
	}
	// Retain the BDF basename while redirecting to an unrelated object. The
	// shared endpoint/representor association helper must reject this as well.
	foreign := filepath.Join(root, "foreign/0000:29:10.0")
	if err := os.MkdirAll(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bus/pci/devices/0000:29:00.0/virtfn0")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, link); err != nil {
		t.Fatal(err)
	}
	if _, err := RepresentorForVF(root, []*netlink.DevlinkPort{port}, "0000:29:10.0", "0000:29:00.0", 0); err == nil {
		t.Fatal("matching basename substituted for canonical VF identity")
	}
}

func TestProtectedRepresentorRetainsAllObservedControllerPFs(t *testing.T) {
	root, rep := representorFixture(t)
	ports := []*netlink.DevlinkPort{rep}
	for i, name := range []string{"uplink-a", "uplink-b"} {
		pf := *rep
		pf.PortFlavour, pf.NetdeviceName, pf.PfNumber = nl.DEVLINK_PORT_FLAVOUR_PCI_PF, name, ptr.To(uint16(i))
		pf.NetdevIfIndex = uint32(101 + i)
		pf.VfNumber = nil
		ports = append(ports, &pf)
	}
	owners, err := DevlinkPortOwners(root, ports, "orange", 200)
	slices.Sort(owners)
	if err != nil || !slices.Equal(owners, []string{"0000:29:00.0", "0000:30:00.0"}) {
		t.Fatalf("management representor did not retain its complete observed controller scope: %v %v", owners, err)
	}
	if _, err := DevlinkPortOwners(root, ports, "orange", 201); err == nil {
		t.Fatal("stale protected port identity silently ignored")
	}
	if owners, err := DevlinkPortOwners(root, ports, "unrelated", 201); err != nil || len(owners) != 0 {
		t.Fatalf("unrelated netdevice acquired controller authority: %v %v", owners, err)
	}
	foreign := *rep
	foreign.BusName = "auxiliary"
	if _, err := DevlinkPortOwners(root, []*netlink.DevlinkPort{&foreign}, "orange", 200); err == nil {
		t.Fatal("unknown protected controller scope silently ignored")
	}
}

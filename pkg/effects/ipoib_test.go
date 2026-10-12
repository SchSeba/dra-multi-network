// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func ipoibFixture(t *testing.T) (string, *netlink.IPoIB, RDMADevice) {
	t.Helper()
	root := rdmaFixture(t)
	base := filepath.Join(root, "class/net/ib0")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "devices/pci0000:00/0000:29:00.0/auxiliary/mlx5_core.sf.4"), filepath.Join(base, "device")); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{
		"class/net/ib0/type": "32", "class/net/ib0/dev_port": "0", "class/net/ib0/mode": "datagram", "class/net/ib0/pkey": "0xffff",
		"class/infiniband/mlx5_0/ports/1/gids/0": "fe80:0000:0000:0000:1122:3344:5566:7788", "class/infiniband/mlx5_0/ports/1/active_mtu": "4: 2048",
	} {
		file := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	address, err := hex.DecodeString("00001234fe800000000000001122334455667788")
	if err != nil {
		t.Fatal(err)
	}
	link := &netlink.IPoIB{LinkAttrs: netlink.LinkAttrs{Name: "ib0", Index: 17, HardwareAddr: address, MTU: 2044}, Pkey: 0xffff, Mode: netlink.IPOIB_MODE_DATAGRAM}
	d := RDMADevice{Index: 7, Name: "mlx5_0", PCI: "0000:29:00.0", NodeGUID: "node", SystemImageGUID: "system", Namespace: &NamespaceIdentity{Device: 1, Inode: 2}, RelatedPCI: []string{"0000:29:00.0"}, NetDevices: []string{"ib0"}, Ports: []RDMAPort{{Number: 1, LinkLayer: topology.InfiniBand, PKeys: map[uint32]uint16{0: 0xffff}}}}
	return root, link, d
}

func TestIPoIBBeforeImageAndRecreatedIdentity(t *testing.T) {
	root, link, d := ipoibFixture(t)
	before, err := readIPoIB(root, link, d)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(RDMAState{Before: d, Profile: topology.IPoIBSinglePort, IPoIB: before})
	if err != nil {
		t.Fatal(err)
	}
	var saved RDMAState
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if !sameIPoIBState(*saved.IPoIB, *before) {
		t.Fatal("journal lost IB mode/port/P_Key/GID state")
	}
	link.Index, link.HardwareAddr[3] = 24, 0x56
	recreated, err := readIPoIB(root, link, d)
	if err != nil || !sameIPoIBState(*recreated, *before) {
		t.Fatal("kernel-recreated QPN/ifindex rejected as a new physical endpoint", err)
	}
	for _, mtu := range []int{255, 2045, 9000} {
		if before.validateMTU(mtu) == nil {
			t.Fatal("accepted invalid datagram MTU", mtu)
		}
	}
	connected := *before
	connected.Mode = netlink.IPOIB_MODE_CONNECTED
	if err := connected.validateMTU(65520); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*netlink.IPoIB){
		func(l *netlink.IPoIB) { l.ParentIndex = 2 }, func(l *netlink.IPoIB) { l.MasterIndex = 2 }, func(l *netlink.IPoIB) { l.HardwareAddr = l.HardwareAddr[:6] },
		func(l *netlink.IPoIB) { l.HardwareAddr[19] ^= 1 }, func(l *netlink.IPoIB) { l.Pkey = 0x8001 }, func(l *netlink.IPoIB) { l.Mode = 7 }, func(l *netlink.IPoIB) { l.Umcast = 2 },
	} {
		root, link, d := ipoibFixture(t)
		change(link)
		if _, err := readIPoIB(root, link, d); err == nil {
			t.Fatal("accepted a partial/foreign IB interface")
		}
	}
	for _, path := range []string{"class/net/ib0/dev_port", "class/net/ib0/type", "class/net/ib0/pkey", "class/infiniband/mlx5_0/ports/1/active_mtu"} {
		root, link, d := ipoibFixture(t)
		if err := os.WriteFile(filepath.Join(root, path), []byte("invalid"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readIPoIB(root, link, d); err == nil {
			t.Fatal("unknown IB baseline became verified", path)
		}
	}
}

func TestIBGrantsDistinguishVerbsIPoIBAndMAD(t *testing.T) {
	_, _, d := ipoibFixture(t)
	h := Handle{Type: topology.RDMAType, Scope: HostScope, Name: "ib0", PCIAddress: d.PCI, RDMA: &d, AllowedRDMAProfiles: []string{topology.IPoIBSinglePort}}
	c := topology.Config{RDMAProfile: topology.IPoIBSinglePort}
	if err := validateRDMAConfigGrant(h, c); err != nil {
		t.Fatal(err)
	}
	c.MADAccess = true
	if validateRDMAConfigGrant(h, c) == nil {
		t.Fatal("ungranted UMAD became available")
	}
	h.AllowedMADAccess = true
	if err := validateRDMAConfigGrant(h, c); err != nil {
		t.Fatal(err)
	}
	c.RDMAProfile = topology.IBVerbsSinglePort
	h.AllowedRDMAProfiles = []string{topology.IBVerbsSinglePort}
	if validateRDMAConfigGrant(h, c) == nil {
		t.Fatal("verbs-only grant discarded an existing IPoIB owner")
	}
	d.NetDevices, h.Name = nil, ""
	if err := validateRDMAConfigGrant(h, c); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Handle){func(h *Handle) { h.Shared = true }, func(h *Handle) { h.AllowedRDMAProfiles = nil }, func(h *Handle) { h.RDMA.Ports = append(h.RDMA.Ports, h.RDMA.Ports[0]) }, func(h *Handle) { h.RDMA.RelatedPCI = append(h.RDMA.RelatedPCI, "0000:30:00.0") }} {
		copy := h
		device := *h.RDMA
		copy.RDMA = &device
		change(&copy)
		if validateRDMAConfigGrant(copy, c) == nil {
			t.Fatal("partial HCA scope accepted")
		}
	}
	if validateRoCEGrant(h, topology.RoCESinglePort) == nil {
		t.Fatal("IB handle became an Ethernet handoff")
	}
	if verified, err := verifiedRDMAChar(RDMAChar{Name: "../../null", Major: 1, Minor: 3}); err == nil {
		t.Fatal("unscoped path accepted", verified)
	}
}

func TestIPoIBLostAcknowledgementDoesNotLoseOrInventOwnership(t *testing.T) {
	h := Handle{Alias: "dra-mn:attachment:rdma:kernel", IfIndex: 17}
	if err := verifyIPoIBOwner("", 17, h, false); err != nil {
		t.Fatal("saved intent before alias assignment could not recover", err)
	}
	if err := verifyIPoIBOwner(h.Alias, 17, h, true); err != nil {
		t.Fatal(err)
	}
	for _, observation := range []struct {
		alias     string
		index     int
		committed bool
	}{{"", 17, true}, {h.Alias, 18, false}, {"dra-mn:another:rdma:kernel", 17, false}, {"external", 17, true}} {
		if verifyIPoIBOwner(observation.alias, observation.index, h, observation.committed) == nil {
			t.Fatal("unrecorded interface owner/incarnation became cleanup authority", observation)
		}
	}
	d := RDMADevice{Ports: []RDMAPort{{Number: 1, LinkLayer: topology.InfiniBand}}, CharDevices: []RDMAChar{{Class: rdmaMADClass, Name: "issm0", Port: 1}, {Class: rdmaMADClass, Name: "umad0", Port: 1}}}
	char, err := rdmaMADChar(d)
	if err != nil || char.Name != "umad0" {
		t.Fatal("UMAD grant included subnet-manager control", char, err)
	}
	for _, chars := range [][]RDMAChar{d.CharDevices[:1], {d.CharDevices[1], d.CharDevices[1]}, {{Class: rdmaMADClass, Name: "umad0", Port: 2}}, {{Class: rdmaMADClass, Name: "umad../../issm0", Port: 1}}} {
		d.CharDevices = chars
		if _, err := rdmaMADChar(d); err == nil {
			t.Fatal("foreign or unknown MAD scope accepted")
		}
	}
}

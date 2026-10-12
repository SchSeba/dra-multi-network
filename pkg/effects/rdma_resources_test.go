// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func ibResourceFixture() (RDMADevice, *netlink.RdmaResource) {
	d := RDMADevice{Index: 7, Name: "mlx5_0", PCI: "0000:29:00.0", RelatedPCI: []string{"0000:29:00.0"}, NetDevices: []string{"ib0"}, Ports: []RDMAPort{{Number: 1, LinkLayer: "infiniband"}}}
	r := &netlink.RdmaResource{Index: d.Index, Name: d.Name, RdmaResourceSummaryEntries: map[string]uint64{"qp": 1, "cq": 1, "pd": 1, "mr": 0, "ctx": 0, "cm_id": 0, "srq": 0}}
	return d, r
}

func ownerMessage(kind rdmaResourceKind, d RDMADevice, change func(*nl.RtAttr, []*nl.RtAttr)) []byte {
	entry := nl.NewRtAttr(kind.Entry|unix.NLA_F_NESTED, nil)
	attrs := []*nl.RtAttr{
		nl.NewRtAttrChild(entry, kind.ID, nl.Uint32Attr(17)),
		nl.NewRtAttrChild(entry, nl.RDMA_NLDEV_ATTR_PORT_INDEX, nl.Uint32Attr(1)),
		nl.NewRtAttrChild(entry, rdmaResourceKernelName, []byte("ib_ipoib\x00")),
	}
	if change != nil {
		change(entry, attrs)
	}
	table := nl.NewRtAttr(kind.Table|unix.NLA_F_NESTED, entry.Serialize())
	message := nl.NewRtAttr(nl.RDMA_NLDEV_ATTR_DEV_INDEX, nl.Uint32Attr(d.Index)).Serialize()
	message = append(message, nl.NewRtAttr(nl.RDMA_NLDEV_ATTR_DEV_NAME, []byte(d.Name+"\x00")).Serialize()...)
	return append(message, table.Serialize()...)
}

func TestIBKernelResourceOwnershipAndCounters(t *testing.T) {
	d, r := ibResourceFixture()
	counts, err := ibResourceCounts([]*netlink.RdmaResource{r}, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range ibResourceKinds {
		owners, err := parseRDMAResourceOwners(ownerMessage(kind, d, nil), kind, d)
		if err != nil || len(owners) != 1 {
			t.Fatalf("%s: %v", kind.Name, err)
		}
		if err := verifyIBResourceOwners(kind, owners, 1, d); err != nil {
			t.Fatal(err)
		}
		for _, change := range []func(*rdmaResourceOwner){
			func(o *rdmaResourceOwner) { o.User = true },
			func(o *rdmaResourceOwner) { o.KernelName = "" },
			func(o *rdmaResourceOwner) { o.KernelName = "nvme_rdma" },
			func(o *rdmaResourceOwner) { o.Port = 2 },
		} {
			bad := append([]rdmaResourceOwner(nil), owners...)
			change(&bad[0])
			if err := verifyIBResourceOwners(kind, bad, 1, d); err == nil {
				t.Fatalf("accepted unauthorized %s owner", kind.Name)
			}
		}
		if err := verifyIBResourceOwners(kind, append(owners, owners...), 2, d); err == nil {
			t.Fatal("accepted duplicate resource IDs")
		}
		if err := verifyIBResourceOwners(kind, owners, 2, d); err == nil {
			t.Fatal("incomplete dump became a complete kernel owner set")
		}
		withoutLink := d
		withoutLink.NetDevices = nil
		if err := verifyIBResourceOwners(kind, owners, 1, withoutLink); err == nil {
			t.Fatal("unobserved IPoIB client became an owned dependency")
		}
		owners[0].KernelName = "ib_mad"
		if err := verifyIBResourceOwners(kind, owners, 1, withoutLink); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"ctx", "cm_id", "srq", "future_resource"} {
		bad := *r
		bad.RdmaResourceSummaryEntries = maps.Clone(counts)
		bad.RdmaResourceSummaryEntries[key] = 1
		if _, err := ibResourceCounts([]*netlink.RdmaResource{&bad}, d); err == nil {
			t.Fatalf("accepted unverified nonzero %s", key)
		}
	}
	for _, resources := range [][]*netlink.RdmaResource{nil, {nil}, {r, r}, {{Index: r.Index, Name: r.Name}, r}} {
		if _, err := ibResourceCounts(resources, d); err == nil {
			t.Fatal("accepted unknown or duplicated device summary")
		}
	}
	for _, change := range []func(*RDMADevice){
		func(d *RDMADevice) { d.Index++ },
		func(d *RDMADevice) { d.PCI = "unverified" },
		func(d *RDMADevice) { d.Ports = append(d.Ports, d.Ports[0]) },
		func(d *RDMADevice) { d.RelatedPCI = append(d.RelatedPCI, "0000:30:00.0") },
		func(d *RDMADevice) { d.NetDevices = append(d.NetDevices, "ib0.8001") },
	} {
		bad := d
		change(&bad)
		if _, err := ibResourceCounts([]*netlink.RdmaResource{r}, bad); err == nil {
			t.Fatal("partial hardware scope became an exclusive IB grant")
		}
	}
}

func TestIBResourceDecoderRejectsMalformedOrForeignDump(t *testing.T) {
	d, _ := ibResourceFixture()
	kind := ibResourceKinds[0]
	for _, change := range []func(*nl.RtAttr, []*nl.RtAttr){
		func(a *nl.RtAttr, _ []*nl.RtAttr) { nl.NewRtAttrChild(a, kind.ID, nl.Uint32Attr(19)) },
		func(a *nl.RtAttr, _ []*nl.RtAttr) { nl.NewRtAttrChild(a, rdmaResourcePID, []byte{1}) },
		func(_ *nl.RtAttr, attrs []*nl.RtAttr) { attrs[2].Data = []byte("not-terminated") },
		func(_ *nl.RtAttr, attrs []*nl.RtAttr) { attrs[2].Data = []byte("ib_ipoib\x00hidden\x00") },
		func(_ *nl.RtAttr, attrs []*nl.RtAttr) { attrs[0].Type |= unix.NLA_F_NET_BYTEORDER },
	} {
		if _, err := parseRDMAResourceOwners(ownerMessage(kind, d, change), kind, d); err == nil {
			t.Fatal("accepted malformed ownership evidence")
		}
	}
	for _, data := range [][]byte{{1, 2, 3}, {0, 0, 0, 0}, ownerMessage(kind, RDMADevice{Index: 8, Name: d.Name}, nil), ownerMessage(kind, RDMADevice{Index: d.Index, Name: "other"}, nil)} {
		if _, err := parseRDMAResourceOwners(data, kind, d); err == nil {
			t.Fatal("accepted malformed/foreign device identity")
		}
	}
	owners, err := parseRDMAResourceOwners(ownerMessage(kind, d, func(a *nl.RtAttr, _ []*nl.RtAttr) { nl.NewRtAttrChild(a, rdmaResourcePID, nl.Uint32Attr(0)) }), kind, d)
	if err != nil || len(owners) != 1 || !owners[0].User {
		t.Fatal("PID zero bypassed the user-resource marker", err)
	}
	if err := verifyIBResourceOwners(kind, owners, 1, d); err == nil {
		t.Fatal("kernel-looking resource with a PID became trusted")
	}
	// nldev omits PORT_INDEX for kernel QPs whose port is not assigned yet.
	// Their owner is still checked against the complete single-port IB scope.
	owners, err = parseRDMAResourceOwners(ownerMessage(kind, d, func(_ *nl.RtAttr, attrs []*nl.RtAttr) { attrs[1].Type = 0 }), kind, d)
	if err != nil || len(owners) != 1 || owners[0].Port != 0 {
		t.Fatal("unassigned kernel QP port was not decoded", err)
	}
	if err := verifyIBResourceOwners(kind, owners, 1, d); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRDMAResourceSocketNamespaces(t *testing.T) {
	if os.Getenv("DRA_NATIVE_TEST") != "1" {
		t.Skip("requires root and namespace capabilities; DRA_NATIVE_TEST=1")
	}
	host, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	path := filepath.Join(t.TempDir(), "resource-ns")
	if err := CreateStagingNamespace(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Unpin(path); err != nil {
			t.Error(err)
		}
	})
	sandbox, err := netns.GetFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close()
	var cookies []uint64
	for _, ns := range []netns.NsHandle{host, sandbox} {
		socket, err := rdmaResourceSocket(ns)
		if err != nil {
			t.Fatal(err)
		}
		cookie, err := unix.GetsockoptUint64(socket.GetFd(), unix.SOL_SOCKET, unix.SO_NETNS_COOKIE)
		if err != nil {
			socket.Close()
			t.Fatal(err)
		}
		cookies = append(cookies, cookie)
		if err := socket.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = rdmaResourceOwners(ns, ibResourceKinds[0], RDMADevice{Index: ^uint32(0), Name: "missing_hca"})
		if err == nil {
			t.Fatal("missing hardware became verified empty ownership")
		}
		t.Log("Missing HCA safely rejected:", strings.TrimSpace(err.Error()))
	}
	if cookies[0] == cookies[1] {
		t.Fatal("resource queries used the same namespace socket")
	}
}

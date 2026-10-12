// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func roceGrant(t *testing.T) Handle {
	t.Helper()
	devices, err := discoverRDMA(rdmaFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	d := devices[0]
	d.Index, d.Namespace = 7, &NamespaceIdentity{Device: 4, Inode: 5}
	d.Ports, d.NetDevices, d.RelatedPCI = d.Ports[:1], []string{"eth0"}, []string{d.PCI}
	return Handle{Type: topology.RDMAType, Scope: HostScope, PCIAddress: d.PCI, Name: "eth0", RDMA: &d, AllowedRDMAProfiles: []string{topology.RoCESinglePort}, AllowedDrivers: []string{"mlx5_core"}}
}

func TestRDMAExclusiveGrantAndCompleteFunction(t *testing.T) {
	h := roceGrant(t)
	if err := validateRoCEGrant(h, topology.RoCESinglePort); err != nil {
		t.Fatal(err)
	}
	if err := completeRoCEFunction([]RDMADevice{*h.RDMA}, *h.RDMA); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Handle){
		func(h *Handle) { h.AllowedRDMAProfiles = nil },
		func(h *Handle) { h.Shared = true },
		func(h *Handle) { h.Type = topology.VFIOType },
		func(h *Handle) { h.RDMA.Namespace = nil },
		func(h *Handle) { h.RDMA.Ports = append(h.RDMA.Ports, h.RDMA.Ports[0]) },
		func(h *Handle) { h.RDMA.Ports[0].LinkLayer = "infiniband" },
		func(h *Handle) { h.RDMA.RelatedPCI = append(h.RDMA.RelatedPCI, "0000:30:00.0") },
		func(h *Handle) { h.RDMA.NetDevices = append(h.RDMA.NetDevices, "eth1") },
		func(h *Handle) { h.Name = "foreign0" },
	} {
		h := roceGrant(t)
		change(&h)
		if err := validateRoCEGrant(h, topology.RoCESinglePort); err == nil {
			t.Fatalf("accepted an incomplete or ungranted HCA: %+v", h)
		}
	}
	other := *h.RDMA
	other.Name, other.Index, other.PCI = "other_hca", 9, "0000:30:00.0"
	other.RelatedPCI = []string{h.PCIAddress, other.PCI}
	if err := completeRoCEFunction([]RDMADevice{*h.RDMA, other}, *h.RDMA); err == nil {
		t.Fatal("accepted an overlapping cross-function HCA")
	}
	if err := completeRoCEFunction(nil, *h.RDMA); err == nil {
		t.Fatal("accepted a missing HCA")
	}
	changed := *h.RDMA
	changed.Index++
	if sameRDMAIdentity(changed, *h.RDMA) {
		t.Fatal("accepted a new HCA index")
	}
	changed = *h.RDMA
	changed.Ports = append([]RDMAPort(nil), changed.Ports...)
	changed.Ports[0].PKeys = map[uint32]uint16{0: 0x1234}
	if sameRDMAIdentity(changed, *h.RDMA) {
		t.Fatal("accepted a changed partition table")
	}
}

func TestRDMAQuiescenceDoesNotTreatUnknownAsIdle(t *testing.T) {
	d := *roceGrant(t).RDMA
	r := &netlink.RdmaResource{Index: d.Index, Name: d.Name, RdmaResourceSummaryEntries: map[string]uint64{"qp": 0, "cq": 0, "mr": 0, "pd": 0, "cm_id": 0, "ctx": 0}}
	if err := rdmaQuiescent([]*netlink.RdmaResource{r}, d); err != nil {
		t.Fatal(err)
	}
	for _, resources := range [][]*netlink.RdmaResource{nil, {nil}, {r, r}, {{Index: d.Index + 1, Name: d.Name, RdmaResourceSummaryEntries: r.RdmaResourceSummaryEntries}}} {
		if err := rdmaQuiescent(resources, d); err == nil {
			t.Fatal("accepted missing or ambiguous resource tracking")
		}
	}
	for _, key := range []string{"qp", "cq", "mr", "pd", "cm_id", "ctx"} {
		r.RdmaResourceSummaryEntries[key] = 1
		if err := rdmaQuiescent([]*netlink.RdmaResource{r}, d); err == nil {
			t.Fatalf("accepted live %s", key)
		}
		delete(r.RdmaResourceSummaryEntries, key)
		if err := rdmaQuiescent([]*netlink.RdmaResource{r}, d); err == nil {
			t.Fatalf("accepted missing %s tracking", key)
		}
		r.RdmaResourceSummaryEntries[key] = 0
	}
	r.RdmaResourceSummaryEntries["new_resource"] = 1
	if err := rdmaQuiescent([]*netlink.RdmaResource{r}, d); err == nil {
		t.Fatal("ignored a live resource added by the kernel")
	}
}

func TestRDMAOpenCharacterFDPreventsMovementWithoutContext(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "123/fd")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", filepath.Join(dir, "4")); err != nil {
		t.Fatal(err)
	}
	d := RDMADevice{CharDevices: []RDMAChar{{Major: 1, Minor: 3}}}
	if err := rdmaDeviceFDsIdle(root, d); err == nil || !strings.Contains(err.Error(), "E_RDMA_ACTIVE") {
		t.Fatalf("ignored an open private device before its first context ioctl: %v", err)
	}
	d.CharDevices[0].Minor = 5
	if err := rdmaDeviceFDsIdle(root, d); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "4")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "124"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := rdmaDeviceFDsIdle(root, d); err == nil {
		t.Fatal("an unreadable descriptor directory became an empty user set")
	}
}

func TestRDMAInvalidParametersNeverEnterMutation(t *testing.T) {
	n := &Native{}
	for _, c := range []topology.Config{{IfName: "secondary0", MTU: 0}, {IfName: "../../net", MTU: 1500}, {IfName: "secondary0", MTU: 9001}} {
		r := Record{Effect: Effect{Parameters: c}, RDMA: &RDMAState{Kernel: Record{Before: &LinkState{}}}}
		if err := n.applyRDMA(&r, func() error { t.Fatal("invalid parameters reached mutation"); return nil }); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
}

func TestRDMATransitionRequiresOwnedCrossNamespaceNetdevice(t *testing.T) {
	root := rdmaFixture(t)
	if err := os.RemoveAll(filepath.Join(root, "class/net/eth0")); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverRDMA(root); err == nil {
		t.Fatal("unowned missing interface was adopted")
	}
	devices, err := discoverRDMAWithNetdevices(root, map[string]string{"eth0": "0000:29:00.0"})
	if err != nil || len(devices) != 1 || !slices.Contains(devices[0].RelatedPCI, "0000:29:00.0") {
		t.Fatalf("recorded cross-namespace interface was lost: %+v %v", devices, err)
	}
	devices, err = discoverRDMAWithNetdevices(root, map[string]string{"eth0": "0000:31:00.0"})
	if err != nil || !slices.Contains(devices[0].RelatedPCI, "0000:31:00.0") {
		t.Fatalf("cross-function dependency was hidden: %+v %v", devices, err)
	}
	if _, err := discoverRDMAWithNetdevices(root, map[string]string{"eth0": "invented"}); err == nil {
		t.Fatal("invalid physical ownership was adopted")
	}
}

// Opt-in hardware check; VM acceptance does not require RDMA hardware.
func TestNativeRoCENamespaceLifecycle(t *testing.T) {
	testNativeRDMANamespaceLifecycle(t, "DRA_RDMA_PCI", topology.RoCESinglePort)
}

func TestNativeIBNamespaceLifecycle(t *testing.T) {
	testNativeRDMANamespaceLifecycle(t, "DRA_IB_PCI", "")
}

func testNativeRDMANamespaceLifecycle(t *testing.T, variable, profile string) {
	t.Helper()
	if os.Getenv("DRA_NATIVE_TEST") != "1" || os.Getenv(variable) == "" {
		t.Skip("requires a drained dedicated HCA; DRA_NATIVE_TEST=1 " + variable + "=<BDF>")
	}
	pci := os.Getenv(variable)
	if !pciAddress.MatchString(pci) {
		t.Fatal("invalid PCI address")
	}
	devices, err := DiscoverRDMA()
	if err != nil {
		t.Fatal(err)
	}
	var root Handle
	for i := range devices {
		if devices[i].PCI == pci {
			if profile == "" {
				profile = topology.IBVerbsSinglePort
				if len(devices[i].NetDevices) == 1 {
					profile = topology.IPoIBSinglePort
				}
			}
			if root.RDMA != nil || len(devices[i].NetDevices) > 1 || profile != topology.IBVerbsSinglePort && len(devices[i].NetDevices) != 1 {
				t.Fatal("function does not satisfy a complete single-HCA/single-interface profile")
			}
			root = Handle{Type: topology.RDMAType, Scope: HostScope, PCIAddress: pci, RDMA: &devices[i], AllowedRDMAProfiles: []string{profile}, AllowedMADAccess: topology.IBProfile(profile) && os.Getenv("DRA_IB_MAD") == "1"}
			if len(devices[i].NetDevices) == 1 {
				root.Name = devices[i].NetDevices[0]
			}
		}
	}
	if root.RDMA == nil {
		t.Fatal("requested physical HCA absent")
	}
	driver, err := pciDriver(pci)
	if err != nil {
		t.Fatal(err)
	}
	root.AllowedDrivers = []string{driver}
	dir, err := os.MkdirTemp("", "dra-mn-roce-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	sandbox, stage := filepath.Join(dir, "sandbox"), filepath.Join(dir, "stage")
	for _, path := range []string{sandbox, stage} {
		if err := CreateStagingNamespace(path); err != nil {
			t.Fatal(err)
		}
	}
	n, err := OpenNative(sandbox, stage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	config := topology.Config{RDMAProfile: profile, MADAccess: root.AllowedMADAccess}
	if profile != topology.IBVerbsSinglePort {
		config.IfName, config.MTU, config.Up = "secondary0", 1500, true
	}
	if profile == topology.IPoIBSinglePort {
		before, err := captureIPoIB(n.hostNS, n.host, *root.RDMA)
		if err != nil {
			t.Fatal(err)
		}
		config.IfName, config.MTU = "ipoib0", before.Link.MTU
	}
	p := Program{APIVersion: APIVersion, Effects: []Effect{
		{ID: "rdma", Kind: topology.RDMANamespace, Parameters: config, Inputs: map[string]Ref{topology.DevicePort: {From: "call.allocation.root"}}, Requires: []Predicate{{Kind: GrantedInputs}}, Postconditions: []Predicate{{Kind: NativeStateMatch}}, Recovery: Recovery{Strategy: ObserveAndReconcile}},
		{ID: "access", Kind: topology.DeviceAccess, Parameters: topology.Config{Mode: topology.RDMAMode, AccessBackend: "nri"}, Inputs: map[string]Ref{topology.DevicePort: {From: "effects.rdma.device"}}, Requires: []Predicate{{Kind: GrantedInputs}}, Postconditions: []Predicate{{Kind: NativeStateMatch}}, Recovery: Recovery{Strategy: ObserveAndReconcile}},
	}, Exports: map[string]Ref{"root.rdma": {From: "effects.access.device"}}}
	if profile != topology.IBVerbsSinglePort {
		p.Exports["root.interface"] = Ref{From: "effects.rdma.interface"}
	}
	if err := Validate(p, []string{topology.RDMANamespace, topology.DeviceAccess}); err != nil {
		t.Fatal(err)
	}
	ex := Execution{ID: "native-rdma", Program: p, Roots: map[string]Handle{"root": root}}
	var committed []byte
	save := func() error {
		var err error
		committed, err = json.Marshal(ex)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "record.json"), committed, 0600)
	}
	t.Cleanup(func() {
		if err := n.Restore(&ex, save); err != nil {
			t.Errorf("cleanup failed; retain namespaces and recorded state in %s: %v", dir, err)
			return
		}
		for _, path := range []string{sandbox, stage} {
			if err := Unpin(path); err != nil {
				t.Error(err)
				return
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if err := n.Preflight(ex); err != nil {
		t.Fatal(err)
	}
	if err := n.Execute(&ex, save); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(committed, &ex); err != nil {
		t.Fatal(err)
	}
	if err := n.Execute(&ex, save); err != nil {
		t.Fatal(err)
	}
	if err := n.Verify(ex); err != nil {
		t.Fatal(err)
	}
	count := 2
	if root.AllowedMADAccess {
		count++
	}
	if ex.Outputs["root.rdma"].Type != topology.RDMAType || len(ex.Outputs["root.rdma"].Devices) != count || profile != topology.IBVerbsSinglePort && ex.Outputs["root.interface"].Type != topology.InterfaceType {
		t.Fatalf("incomplete typed outputs: %+v", ex.Outputs)
	}
	fd, err := os.OpenFile(ex.Outputs["root.rdma"].Devices[0].Path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	busy := noRDMAUsers(n.sandboxNS, *ex.Outputs["root.rdma"].RDMA)
	if err := fd.Close(); err != nil {
		t.Fatal(err)
	}
	if busy == nil || !strings.Contains(busy.Error(), "E_RDMA_ACTIVE") {
		t.Fatal("open HCA FD did not block movement", busy)
	}
	for range 2 {
		if err := n.Restore(&ex, save); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRDMACommonCMNodeRequiresKernelAndCharacterIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "class/misc/rdma_cm")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"1:3", "1:5", "bad", "0:3"} {
		if err := os.WriteFile(filepath.Join(path, "dev"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		node, err := rdmaCMNode(root, "/dev/null")
		if value == "1:3" {
			if err != nil || node.Major != 1 || node.Minor != 3 || node.Inode == 0 {
				t.Fatalf("kernel character identity rejected: %+v %v", node, err)
			}
		} else if err == nil {
			t.Fatalf("accepted conflicting kernel identity %s", value)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "dev"), []byte("1:3"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rdmaCMNode(root, filepath.Join(path, "dev")); err == nil {
		t.Fatal("ordinary file accepted as CM node")
	}
}

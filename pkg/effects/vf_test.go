// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestConfiguredVFProgramsAndFieldGrants(t *testing.T) {
	plugins := map[string]api.NetworkPlugin{}
	for _, p := range topology.Builtins() {
		plugins[p.Name] = p
	}
	topo := api.NetworkTopology{Spec: api.TopologySpec{EffectBudget: api.EffectBudget{Allow: []string{topology.VFConfigure, Move, Configure}}, Steps: []api.Step{{ID: "endpoint", Kind: "Allocation", PluginRef: api.PluginRef{Name: "sriov-cni-configured"}, Config: ext.JSON{Raw: []byte(`{"mode":"kernel","linkType":"ethernet","rdmaAccess":false,"ifName":"secondary0","mtu":1500,"up":true,"vf":{"spoofCheck":false,"trust":true}}`)}}}}}
	r, err := topology.Compile(topo, plugins)
	if err != nil {
		t.Fatal(err)
	}
	if err := topology.VerifyRevision(r); err != nil {
		t.Fatal(err)
	}
	prepare, err := PrepareDevices(r)
	if err != nil || len(prepare.Effects) != 1 || prepare.Effects[0].Kind != topology.VFConfigure || prepare.Effects[0].Parameters.VF.SpoofCheck == nil || *prepare.Effects[0].Parameters.VF.SpoofCheck {
		t.Fatalf("explicit false lost: %+v, %v", prepare, err)
	}
	connect, err := ConnectNetwork(r)
	if err != nil || len(connect.Effects) != 2 || connect.Effects[0].Kind != Move {
		t.Fatalf("configured kernel root cannot connect: %+v, %v", connect, err)
	}
	topo.Spec.EffectBudget.Allow = []string{topology.VFConfigure, topology.DriverBind, topology.DeviceAccess}
	topo.Spec.Steps[0].PluginRef.Name = "sriov-cni-vfio-configured"
	topo.Spec.Steps[0].Config.Raw = []byte(`{"mode":"vfio","linkType":"ethernet","driver":"vfio-pci","accessBackend":"cdi","vf":{"trust":true}}`)
	r, err = topology.Compile(topo, plugins)
	if err != nil {
		t.Fatal(err)
	}
	prepare, err = PrepareDevices(r)
	if err != nil || len(prepare.Effects) != 3 || prepare.Effects[1].Inputs[topology.DevicePort].From != "effects.endpoint-vf-configure.device" {
		t.Fatalf("VFIO ordering: %+v, %v", prepare, err)
	}
	// Authorization must fail before any sysfs/netlink access.
	native := Native{}
	if _, err := native.captureVF(Handle{AllowedVFSettings: []string{"trust"}}, &topology.VFSettings{Trust: ptr.To(true), SpoofCheck: ptr.To(false)}); err == nil || !strings.Contains(err.Error(), "E_VF_SETTING_DENIED: spoofCheck") {
		t.Fatalf("undelegated field accepted: %v", err)
	}
	for _, bad := range []topology.VFSettings{{MAC: "01:00:00:00:00:01"}, {VlanQoS: ptr.To(1)}, {LinkState: "reset"}, {MinTxRate: ptr.To(uint32(10)), MaxTxRate: ptr.To(uint32(5))}} {
		if err := topology.ValidateVFSettings(bad); err == nil {
			t.Fatalf("invalid setting accepted: %+v", bad)
		}
	}
}

func TestVFRecoveryPreservesUntouchedFields(t *testing.T) {
	before := VFValues{MAC: "02:00:00:00:00:01", SpoofCheck: true, VlanProtocol: 0x8100, MinTxRate: 5, MaxTxRate: 10}
	target := vfTarget(before, topology.VFSettings{Trust: ptr.To(true), SpoofCheck: ptr.To(false), MaxTxRate: ptr.To(uint32(20))})
	if target.MinTxRate != before.MinTxRate || target.MAC != before.MAC || !target.Trust || target.SpoofCheck {
		t.Fatalf("untouched state lost: %+v", target)
	}
	partial := before
	partial.Trust = true // first action applied, acknowledgement/remainder lost
	if err := vfMayReconcile(partial, before, target); err != nil {
		t.Fatal(err)
	}
	partial.MAC = "02:00:00:00:00:02"
	if err := vfMayReconcile(partial, before, target); err == nil {
		t.Fatal("external MAC change accepted during recovery")
	}
}

func TestVFGrantVerifiesBothPhysicalParentAndIndex(t *testing.T) {
	sysRoot := t.TempDir()
	h := Handle{Type: topology.InterfaceType, Scope: HostScope, PCIAddress: "0000:29:11.0", ParentPCI: "0000:29:00.0", VFID: 4}
	parent := filepath.Join(sysRoot, "bus/pci/devices", h.ParentPCI)
	vf := filepath.Join(sysRoot, "bus/pci/devices", h.PCIAddress)
	for _, path := range []string{filepath.Join(parent, "net/eth0"), vf} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(parent, filepath.Join(vf, "physfn")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vf, filepath.Join(parent, "virtfn4")); err != nil {
		t.Fatal(err)
	}
	if name, err := vfParent(sysRoot, h); err != nil || name != "eth0" {
		t.Fatalf("valid VF grant: %s, %v", name, err)
	}
	function := h
	function.Type = topology.PCIFunctionType
	function.ParentPCI, function.VFID = "", 0
	observed, err := pciVFHandle(sysRoot, function)
	if err != nil || observed == nil || observed.ParentPCI != h.ParentPCI || observed.VFID != 4 || len(observed.AllowedVFSettings) != 0 {
		t.Fatalf("read-only PCI snapshot did not derive the exact VF without write grants: %+v %v", observed, err)
	}
	function.ParentPCI, function.VFID = h.ParentPCI, 3
	if _, err := pciVFHandle(sysRoot, function); err == nil {
		t.Fatal("PCI snapshot accepted a caller's wrong VF index")
	}
	function.VFID = 4
	function.Shared = true
	if _, err := pciVFHandle(sysRoot, function); err == nil {
		t.Fatal("shared PCI scope acquired an administrative snapshot")
	}
	function = Handle{Type: topology.PCIFunctionType, Scope: HostScope, PCIAddress: h.ParentPCI}
	if observed, err := pciVFHandle(sysRoot, function); err != nil || observed != nil {
		t.Fatalf("PF invented VF state: %+v %v", observed, err)
	}
	if err := os.Symlink(vf, filepath.Join(parent, "virtfn5")); err != nil {
		t.Fatal(err)
	}
	function.PCIAddress = h.PCIAddress
	if _, err := pciVFHandle(sysRoot, function); err == nil {
		t.Fatal("ambiguous VF-to-index mapping accepted")
	}
	if err := os.Remove(filepath.Join(parent, "virtfn5")); err != nil {
		t.Fatal(err)
	}
	h.VFID = 3
	if _, err := vfParent(sysRoot, h); err == nil {
		t.Fatal("caller manufactured VF index")
	}
	h.VFID = 4
	h.Shared = true
	if _, err := vfParent(sysRoot, h); err == nil {
		t.Fatal("shared allocation may configure its parent")
	}
}

// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestKernelProbeRestorationAcceptsOnlyTheAcknowledgedMAC(t *testing.T) {
	before := PCIState{Driver: "igbvf", Link: &LinkState{MAC: "02:00:00:00:00:01"}, VF: &PCIVFState{Before: VFState{Values: VFValues{MAC: "02:00:00:00:00:01", SpoofCheck: true}}}}
	probe := "02:00:00:00:00:02"
	current := before.VF.Before.Values
	current.MAC = probe
	link := LinkState{MAC: probe}
	ack := KernelRestoreState{Bound: true, ProbeMAC: probe}
	if got, err := (&KernelRestoreState{}).vfProbeMAC(before, current, link, "igb", true); err != nil || got != probe {
		t.Fatalf("actual acknowledged probe rejected: %s %v", got, err)
	}
	if _, err := ack.vfProbeMAC(before, current, link, "igb", false); err != nil {
		t.Fatal(err)
	}
	if _, err := (KernelRestoreState{}).vfProbeMAC(before, current, link, "igb", false); err == nil {
		t.Fatal("unrecorded probe authorized MAC restoration")
	}
	for _, change := range []string{"different MAC", "trust", "foreign link MAC", "external alias", "unsupported driver"} {
		t.Run(change, func(t *testing.T) {
			v, l, driver := current, link, "igb"
			switch change {
			case "different MAC":
				v.MAC, l.MAC = "02:00:00:00:00:03", "02:00:00:00:00:03"
			case "trust":
				v.Trust = true
			case "foreign link MAC":
				l.MAC = "02:00:00:00:00:03"
			case "external alias":
				l.Alias = "another-owner"
			case "unsupported driver":
				driver = "other"
			}
			if _, err := ack.vfProbeMAC(before, v, l, driver, false); err == nil {
				t.Fatal("external/unapproved state authorized restoration")
			}
		})
	}
	current.MAC, link.MAC = before.Link.MAC, before.Link.MAC
	if _, err := ack.vfProbeMAC(before, current, link, "igb", false); err != nil {
		t.Fatal("acknowledged restoration retry rejected", err)
	}
}

func TestVFIOUsesPreparedDeviceAndCannotFeedEthernetOperations(t *testing.T) {
	plugins := map[string]api.NetworkPlugin{}
	for _, p := range topology.Builtins() {
		plugins[p.Name] = p
	}
	topo := api.NetworkTopology{Spec: api.TopologySpec{EffectBudget: api.EffectBudget{Allow: []string{topology.DriverBind, topology.DeviceAccess}}, Steps: []api.Step{{ID: "endpoint", Kind: "Allocation", PluginRef: api.PluginRef{Name: "sriov-cni-vfio"}, Config: ext.JSON{Raw: []byte(`{"mode":"vfio","linkType":"ethernet","driver":"vfio-pci","accessBackend":"cdi"}`)}}}}}
	r, err := topology.Compile(topo, plugins)
	if err != nil {
		t.Fatal(err)
	}
	if err := topology.VerifyRevision(r); err != nil {
		t.Fatal(err)
	}
	prepare, err := PrepareDevices(r)
	if err != nil || len(prepare.Effects) != 2 || prepare.Exports["endpoint.vfio"].From != "effects.endpoint-access.device" {
		t.Fatalf("preparation: %+v, %v", prepare, err)
	}
	connect, err := ConnectNetwork(r)
	if err != nil || len(connect.Effects) != 0 || connect.Exports["endpoint.vfio"].From != "call.prepared.endpoint.vfio" {
		t.Fatalf("attachment: %+v, %v", connect, err)
	}
	topo.Spec.EffectBudget.Allow = append(topo.Spec.EffectBudget.Allow, CreateVLAN, Configure)
	topo.Spec.Steps = append(topo.Spec.Steps, api.Step{ID: "tenant", Kind: "Operation", PluginRef: api.PluginRef{Name: "vlan-cni"}, Config: ext.JSON{Raw: []byte(`{"ifName":"tenant100","mtu":1500,"up":true,"vlanId":100}`)}, Inputs: map[string]ext.JSON{"parent": {Raw: []byte(`{"from":"endpoint.vfio"}`)}}})
	if _, err := topology.Compile(topo, plugins); err == nil || !strings.Contains(err.Error(), "E_PORT_TYPE") {
		t.Fatalf("VFIO VLAN parent accepted: %v", err)
	}
}

func TestKernelBindingUsesPreparationWithoutCDI(t *testing.T) {
	plugins := map[string]api.NetworkPlugin{}
	for _, p := range topology.Builtins() {
		plugins[p.Name] = p
	}
	topo := api.NetworkTopology{Spec: api.TopologySpec{EffectBudget: api.EffectBudget{Allow: []string{topology.DriverBind, Move, Configure}}, Steps: []api.Step{{ID: "endpoint", Kind: topology.AllocationKind, PluginRef: api.PluginRef{Name: "sriov-cni-rebind"}, Config: ext.JSON{Raw: []byte(`{"mode":"kernel","linkType":"ethernet","rdmaAccess":false,"driver":"igbvf","ifName":"secondary0","mtu":1500,"up":true}`)}}}}}
	r, err := topology.Compile(topo, plugins)
	if err != nil {
		t.Fatal(err)
	}
	if err := topology.VerifyRevision(r); err != nil {
		t.Fatal(err)
	}
	prepare, err := PrepareDevices(r)
	if err != nil || len(prepare.Effects) != 1 || prepare.Effects[0].Parameters.Driver != "igbvf" || len(prepare.Exports) != 0 {
		t.Fatalf("preparation: %+v, %v", prepare, err)
	}
	connect, err := ConnectNetwork(r)
	if err != nil || len(connect.Effects) != 2 || connect.Effects[0].Kind != Move {
		t.Fatalf("attachment: %+v, %v", connect, err)
	}
	topo.Spec.Steps[0].Config.Raw = []byte(strings.ReplaceAll(string(topo.Spec.Steps[0].Config.Raw), "igbvf", topology.VFIODriver))
	if _, err := topology.Compile(topo, plugins); err == nil || !strings.Contains(err.Error(), "E_DRIVER_CONTRACT") {
		t.Fatalf("VFIO accepted as a kernel driver: %v", err)
	}
	var native Native
	h := Handle{Type: topology.PCIFunctionType, Scope: HostScope, AllowedDrivers: []string{"igbvf"}}
	for _, driver := range []string{topology.VFIODriver, "../../drivers_probe"} {
		if _, err := native.capturePCI(h, driver); err == nil || !strings.Contains(err.Error(), "E_DRIVER_GRANT") {
			t.Fatalf("unauthorized target reached Linux: %q: %v", driver, err)
		}
	}
}

// Opt-in manual hardware acceptance on an idle secondary PCI function. It
// checks actual kernel→VFIO→kernel binding and reverse restoration, not mocks.
func TestNativeKernelDriverRoundTrip(t *testing.T) {
	pci, kernel := os.Getenv("DRA_REBIND_TEST_PCI"), os.Getenv("DRA_REBIND_TEST_DRIVER")
	if pci == "" {
		t.Skip("requires an explicitly selected idle secondary PCI function")
	}
	if !pciAddress.MatchString(pci) || !driverName.MatchString(kernel) || kernel == topology.VFIODriver {
		t.Fatal("invalid hardware acceptance parameters")
	}
	native, err := OpenHostNative()
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	links, err := os.ReadDir(filepath.Join("/sys/bus/pci/devices", pci, "net"))
	if err != nil || len(links) != 1 {
		t.Fatalf("expected one original kernel interface: %v", err)
	}
	h := Handle{Type: topology.PCIFunctionType, Scope: HostScope, Name: links[0].Name(), PCIAddress: pci, AllowedDrivers: []string{kernel, topology.VFIODriver}}
	before, err := native.capturePCI(h, topology.VFIODriver)
	if err != nil || before.Driver != kernel {
		t.Fatalf("original kernel binding: %+v, %v", before, err)
	}
	if _, err := os.Readlink(filepath.Join("/sys/bus/pci/devices", pci, "physfn")); err == nil && (before.VF == nil || before.VF.Before.Values.MAC == "" || before.VF.Before.Link == nil) {
		t.Fatalf("binding missing the allocated VF's administrative/link before-image: %+v", before)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if before.VF != nil {
		changed := *before.VF
		changed.Before.Values.Trust = !changed.Before.Values.Trust
		if err := native.verifyPCIVF(pci, &changed); err == nil || !strings.Contains(err.Error(), "E_VF_ADMIN_CHANGED") {
			t.Fatalf("administrative state divergence was accepted: %v", err)
		}
	}
	plan := func(id, driver string) Execution {
		return Execution{ID: id, Roots: map[string]Handle{"endpoint": h}, Program: Program{APIVersion: APIVersion, Exports: map[string]Ref{}, Effects: []Effect{{ID: "bind", Kind: topology.DriverBind, Parameters: topology.Config{Driver: driver}, Inputs: map[string]Ref{topology.DevicePort: {From: "call.allocation.endpoint"}}, Requires: []Predicate{{Kind: GrantedInputs}}, Postconditions: []Predicate{{Kind: NativeStateMatch}}, Recovery: Recovery{Strategy: ObserveAndReconcile}}}}}
	}
	vfio, rebound := plan("vfio-roundtrip", topology.VFIODriver), plan("kernel-roundtrip", kernel)
	save := func() error { return nil }
	defer func() {
		for _, execution := range []*Execution{&rebound, &vfio} {
			if err := native.Restore(execution, save); err != nil {
				t.Errorf("restore %s: %v", execution.ID, err)
			}
		}
	}()
	for _, execution := range []*Execution{&vfio, &rebound} {
		if err := Validate(execution.Program, []string{topology.DriverBind}); err != nil {
			t.Fatal(err)
		}
		if err := native.Preflight(*execution); err != nil {
			t.Fatal(err)
		}
		if err := native.Execute(execution, save); err != nil {
			t.Fatal(err)
		}
		if err := native.Verify(*execution); err != nil {
			t.Fatal(err)
		}
	}
	if rebound.Records[0].Output.Name == "" {
		t.Fatal("kernel netdevice was not rediscovered")
	}
	// Reload the recorded target to exercise restoration after a process restart.
	data, err := json.Marshal(rebound)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &rebound); err != nil {
		t.Fatal(err)
	}
	if err := native.Restore(&rebound, save); err != nil {
		t.Fatal(err)
	}
	if driver, err := pciDriver(pci); err != nil || driver != topology.VFIODriver {
		t.Fatalf("intermediate original VFIO binding was not restored: %q, %v", driver, err)
	}
	if err := native.Restore(&vfio, save); err != nil {
		t.Fatal(err)
	}
	actual, err := native.capturePCI(h, topology.VFIODriver)
	if err != nil || !reflect.DeepEqual(before, actual) {
		t.Fatalf("original driver/override/link state not restored: want %+v, got %+v, %v", before, actual, err)
	}
}

func TestIOMMUMustOwnTheCompleteFunctionGroup(t *testing.T) {
	root := t.TempDir()
	pci := "0000:29:11.0"
	device := filepath.Join(root, "bus/pci/devices", pci)
	group := filepath.Join(root, "kernel/iommu_groups/30")
	for _, dir := range []string{device, filepath.Join(group, "devices")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(group, filepath.Join(device, "iommu_group")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(device, filepath.Join(group, "devices", pci)); err != nil {
		t.Fatal(err)
	}
	if actual, err := singleFunctionIOMMU(root, pci); err != nil || actual != "30" {
		t.Fatalf("single group: %q, %v", actual, err)
	}
	if err := os.WriteFile(filepath.Join(device, "sriov_numvfs"), []byte("2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := singleFunctionIOMMU(root, pci); err == nil || !strings.Contains(err.Error(), "E_PF_VFS_PROFILE") {
		t.Fatalf("PF binding with enabled VFs accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(device, "sriov_numvfs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(device, filepath.Join(group, "devices", "0000:29:10.6")); err != nil {
		t.Fatal(err)
	}
	if _, err := singleFunctionIOMMU(root, pci); err == nil || !strings.Contains(err.Error(), "E_IOMMU_SCOPE") {
		t.Fatalf("partial group accepted: %v", err)
	}
	if _, err := singleFunctionIOMMU(root, "../../kernel"); err == nil {
		t.Fatal("malformed PCI identity accepted")
	}
}

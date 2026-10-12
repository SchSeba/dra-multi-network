// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/journal"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestResetRequiresRecordedOutcomeAndNeverReplaysUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failedSave   int
		syscallFails bool
		wantWrites   int
	}{
		{name: "acknowledged", wantWrites: 1},
		{name: "intent commit fails", failedSave: 1, wantWrites: 0},
		{name: "syscall fails ambiguously", syscallFails: true, wantWrites: 1},
		{name: "outcome commit lost", failedSave: 2, wantWrites: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := ResetState{}
			persisted := ResetState{}
			commits, writes := 0, 0
			save := func() error {
				commits++
				if commits == tc.failedSave {
					return errors.New("storage unavailable")
				}
				data, _ := json.Marshal(state)
				return json.Unmarshal(data, &persisted)
			}
			reset := func() error {
				writes++
				if !persisted.Attempted || persisted.Completed {
					t.Fatal("syscall without durable intent")
				}
				if tc.syscallFails {
					return errors.New("lost syscall outcome")
				}
				return nil
			}
			err := issueReset(&state, save, reset)
			if writes != tc.wantWrites {
				t.Fatalf("reset writes=%d, want %d", writes, tc.wantWrites)
			}
			if tc.failedSave == 0 && !tc.syscallFails {
				if err != nil {
					t.Fatal(err)
				}
				state = persisted
				if err := issueReset(&state, save, reset); err != nil || writes != 1 {
					t.Fatalf("completed reset replayed: %d %v", writes, err)
				}
			} else {
				if err == nil {
					t.Fatal("failure suppressed")
				}
				if persisted.Attempted {
					state = persisted
					if err := issueReset(&state, save, reset); err == nil || !strings.Contains(err.Error(), "E_RESET_UNCERTAIN") || writes != tc.wantWrites {
						t.Fatalf("uncertain reset replayed: %d %v", writes, err)
					}
					record := Record{PCI: &PCIState{}, Reset: &state}
					var native Native
					if err := native.restoreReset(&record, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "E_RESET_UNCERTAIN") {
						t.Fatalf("cleanup released uncertain reset: %v", err)
					}
				}
			}
		})
	}
}

func TestResetScopeIsOneFunctionWithNoDependentHCA(t *testing.T) {
	root := t.TempDir()
	pci := "0000:29:11.0"
	function := filepath.Join(root, "bus/pci/devices", pci)
	group := filepath.Join(root, "kernel/iommu_groups/30")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(function, 0700))
	must(os.MkdirAll(filepath.Join(group, "devices"), 0700))
	must(os.Symlink(group, filepath.Join(function, "iommu_group")))
	must(os.Symlink(function, filepath.Join(group, "devices", pci)))
	must(os.WriteFile(filepath.Join(function, "reset_method"), []byte("flr pm bus\n"), 0600))
	must(os.WriteFile(filepath.Join(function, "reset"), nil, 0600))
	scope, err := resetScope(root, pci, topology.FLR)
	if err != nil || scope.Group != "30" || scope.OriginalMethods != "flr pm bus" {
		t.Fatalf("invalid scope: %+v %v", scope, err)
	}
	if _, err := resetScope(root, pci, "bus"); err == nil || !strings.Contains(err.Error(), "E_RESET_METHOD") {
		t.Fatal("bus reset accepted without complete-domain contract")
	}
	must(os.Symlink(function, filepath.Join(group, "devices", "0000:29:11.1")))
	if _, err := resetScope(root, pci, topology.FLR); err == nil || !strings.Contains(err.Error(), "E_IOMMU_SCOPE") {
		t.Fatal("unallocated IOMMU sibling accepted")
	}
	must(os.Remove(filepath.Join(group, "devices", "0000:29:11.1")))
	must(os.WriteFile(filepath.Join(function, "sriov_numvfs"), []byte("1"), 0600))
	if _, err := resetScope(root, pci, topology.FLR); err == nil || !strings.Contains(err.Error(), "E_PF_VFS_PROFILE") {
		t.Fatal("PF reset accepted with enabled VFs")
	}
	must(os.WriteFile(filepath.Join(function, "sriov_numvfs"), []byte("0"), 0600))
	must(os.MkdirAll(filepath.Join(root, "class/infiniband/hca"), 0700))
	must(os.Symlink(function, filepath.Join(root, "class/infiniband/hca/device")))
	if _, err := resetScope(root, pci, topology.FLR); err == nil || !strings.Contains(err.Error(), "E_RESET_RDMA_SCOPE") {
		t.Fatal("HCA-wide reset inferred from PCI isolation")
	}
	var native Native
	for _, h := range []Handle{{Type: topology.PCIFunctionType, Scope: HostScope}, {Type: topology.PCIFunctionType, Scope: HostScope, Shared: true, AllowedResetMethods: []string{topology.FLR}}, {Type: topology.PCIFunctionType, Scope: SandboxScope, AllowedResetMethods: []string{topology.FLR}}} {
		if _, _, err := native.captureReset(h, topology.FLR); err == nil || !strings.Contains(err.Error(), "E_RESET_GRANT") {
			t.Fatalf("ungranted reset reached Linux: %v", err)
		}
	}
}

func TestResetPlansUseNonReplayableRecovery(t *testing.T) {
	object := api.NetworkTopology{Spec: api.TopologySpec{EffectBudget: api.EffectBudget{Allow: []string{topology.PCIReset, topology.DriverBind, Move, Configure}}, Steps: []api.Step{{ID: "endpoint", Kind: topology.AllocationKind, PluginRef: api.PluginRef{Name: "sriov-cni-reset"}, Config: ext.JSON{Raw: []byte(`{"mode":"kernel","linkType":"ethernet","rdmaAccess":false,"driver":"igbvf","resetMethod":"flr","ifName":"secondary0","mtu":1500,"up":true}`)}}}}}
	revision, err := topology.Compile(object, topology.PluginRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareDevices(revision)
	if err != nil || len(plan.Effects) != 2 || plan.Effects[0].Kind != topology.PCIReset || plan.Effects[0].Recovery.Strategy != topology.ObserveOrRetain || plan.Effects[1].Inputs[topology.DevicePort].From != "effects.endpoint-reset.device" {
		t.Fatalf("incorrect reset dependency/recovery: %+v %v", plan, err)
	}
	plan.Effects[0].Recovery.Strategy = topology.ObserveAndReconcile
	if err := Validate(plan, revision.Topology.EffectBudget.Allow); err == nil {
		t.Fatal("replayable reset accepted")
	}
}

// Manual acceptance on an idle secondary function selected by the operator.
func TestNativeFunctionResetRoundTrip(t *testing.T) {
	pci := os.Getenv("DRA_RESET_TEST_PCI")
	if pci == "" {
		t.Skip("set DRA_RESET_TEST_PCI to an idle secondary function")
	}
	driver, err := pciDriver(pci)
	if err != nil {
		t.Fatal(err)
	}
	links, err := os.ReadDir(filepath.Join("/sys/bus/pci/devices", pci, "net"))
	if err != nil || len(links) != 1 {
		t.Fatalf("kernel interface: %v", err)
	}
	native, err := OpenHostNative()
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	handle := Handle{Type: topology.PCIFunctionType, Scope: HostScope, PCIAddress: pci, Name: links[0].Name(), AllowedDrivers: []string{driver}, AllowedResetMethods: []string{topology.FLR}}
	before, scope, err := native.captureReset(handle, topology.FLR)
	if err != nil {
		t.Fatal(err)
	}
	if parent, err := os.Readlink(filepath.Join("/sys/bus/pci/devices", pci, "physfn")); err == nil {
		if before.VF == nil || before.VF.ParentPCI != filepath.Base(parent) || before.VF.Before.Values.MAC == "" || before.VF.Before.Link == nil {
			t.Fatalf("VF reset missing complete administrative/link before-image: %+v", before)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	execution := Execution{ID: "manual-reset", Roots: map[string]Handle{"function": handle}, Program: Program{APIVersion: APIVersion, Effects: []Effect{{ID: "reset", Kind: topology.PCIReset, Inputs: map[string]Ref{topology.DevicePort: {From: "call.allocation.function"}}, Parameters: topology.Config{ResetMethod: topology.FLR}, Requires: []Predicate{{Kind: GrantedInputs}}, Postconditions: []Predicate{{Kind: NativeStateMatch}}, Recovery: Recovery{Strategy: topology.ObserveOrRetain}}}, Exports: map[string]Ref{}}}
	save := func() error { return store.Put("execution", execution) }
	if err := native.Preflight(execution); err != nil {
		t.Fatal(err)
	}
	if err := native.Execute(&execution, save); err != nil {
		t.Fatal(err)
	}
	var recorded Execution
	if found, err := store.Get("execution", &recorded); err != nil || !found {
		t.Fatalf("record missing: %v", err)
	}
	if len(recorded.Records) != 1 || !recorded.Records[0].Reset.Completed || !recorded.Records[0].Reset.Attempted || recorded.Records[0].State != "Applied" {
		t.Fatalf("reset acknowledgement not durable: %+v", recorded)
	}
	if err := native.Verify(execution); err != nil {
		t.Fatal(err)
	}
	if err := native.Execute(&execution, save); err != nil {
		t.Fatal(err)
	} // observes completed reset
	if err := native.Restore(&execution, save); err != nil {
		t.Fatal(err)
	}
	if err := native.Restore(&execution, save); err != nil {
		t.Fatal(err)
	}
	restored, after, err := native.captureReset(handle, topology.FLR)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Digest(before) != topology.Digest(restored) || scope.Scope != after.Scope {
		t.Fatalf("original function/link state not restored: %+v %+v", before, restored)
	}
	t.Log("PASS: actual FLR acknowledged in the durable journal; repeat execution observes it; original methods/driver/override/link state restored")
}

func TestProgrammableResetRetainsNativeContract(t *testing.T) {
	profile := topology.PluginRegistry(nil)["sriov-cni-reset"]
	object := api.NetworkTopology{Spec: api.TopologySpec{EffectBudget: api.EffectBudget{Allow: profile.Spec.AllowedEffects}, Steps: []api.Step{{ID: "endpoint", Kind: topology.AllocationKind, PluginRef: api.PluginRef{Name: profile.Name}, Config: ext.JSON{Raw: []byte(`{"mode":"kernel","linkType":"ethernet","rdmaAccess":false,"driver":"igbvf","resetMethod":"flr","ifName":"secondary0","mtu":1500,"up":true}`)}}}}}
	revision, err := topology.Compile(object, topology.PluginRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	prepare, err := PrepareDevices(revision)
	if err != nil {
		t.Fatal(err)
	}
	connect, err := ConnectNetwork(revision)
	if err != nil {
		t.Fatal(err)
	}
	localize := func(program Program) ext.JSON {
		data, err := json.Marshal(program)
		if err != nil {
			t.Fatal(err)
		}
		raw := strings.ReplaceAll(string(data), "call.allocation.endpoint", "call.allocation")
		raw = strings.ReplaceAll(raw, "endpoint-", "")
		raw = strings.ReplaceAll(raw, `"endpoint.interface":`, `"interface":`)
		return ext.JSON{Raw: []byte(raw)}
	}
	plugin := *profile.DeepCopy()
	plugin.Name = "programmable-reset"
	plugin.Spec.Implementation = api.Implementation{Type: topology.EffectProgramImplementation, Version: topology.AllocationEffectsVersion}
	plugin.Spec.AllocationProfileRef = &api.AllocationProfileRef{Name: profile.Name, Digest: topology.Digest(profile.Spec)}
	plugin.Spec.Hooks = map[string]ext.JSON{topology.PrepareHook: localize(prepare), topology.ConnectHook: localize(connect), topology.RemoveHook: {Raw: []byte(`{"apiVersion":"` + APIVersion + `","restoreRecordedHook":"ConnectNetwork"}`)}, topology.UnprepareHook: {Raw: []byte(`{"apiVersion":"` + APIVersion + `","restoreRecordedHook":"NodePrepare"}`)}}
	object.Spec.Steps[0].PluginRef.Name = plugin.Name
	if _, err := topology.Compile(object, topology.PluginRegistry([]api.NetworkPlugin{plugin})); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Program)
		reason string
	}{
		{"replayable reset", func(p *Program) { p.Effects[0].Recovery.Strategy = topology.ObserveAndReconcile }, "E_TEMPLATE_CONTRACT"},
		{"missing reset", func(p *Program) {
			p.Effects = p.Effects[1:]
			p.Effects[0].Inputs[topology.DevicePort] = Ref{From: "call.allocation.endpoint"}
		}, "E_TEMPLATE_PREPARATION_COVERAGE"},
		{"reset after binding", func(p *Program) {
			p.Effects[0], p.Effects[1] = p.Effects[1], p.Effects[0]
			p.Effects[0].Inputs[topology.DevicePort] = Ref{From: "call.allocation.endpoint"}
			p.Effects[1].Inputs[topology.DevicePort] = Ref{From: "effects.endpoint-bind.device"}
		}, "E_TEMPLATE_PREPARATION_ORDER"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PrepareDevices(revision)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&plan)
			invalid := plugin.DeepCopy()
			invalid.Spec.Hooks[topology.PrepareHook] = localize(plan)
			if _, err := topology.Compile(object, topology.PluginRegistry([]api.NetworkPlugin{*invalid})); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s, got %v", tc.reason, err)
			}
		})
	}
}

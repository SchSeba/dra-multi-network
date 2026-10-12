// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"strings"
	"testing"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestAllocationPreparationOrder(t *testing.T) {
	profile := PluginRegistry(nil)[SRIOVPlugin+"-vfio-configured"]
	plugin := *profile.Spec.DeepCopy()
	plugin.Implementation = api.Implementation{Type: EffectProgramImplementation, Version: AllocationEffectsVersion}
	plugin.AllocationProfileRef = &api.AllocationProfileRef{Name: profile.Name, Digest: Digest(profile.Spec)}
	step := api.Step{ID: "root", Config: ext.JSON{Raw: []byte(`{"mode":"vfio","linkType":"ethernet","driver":"vfio-pci","accessBackend":"cdi","vf":{"trust":false}}`)}}
	config, err := ConfigForPlugin(step, plugin)
	if err != nil {
		t.Fatal(err)
	}
	program := EffectProgram{Effects: []NetworkEffect{
		{ID: "configure", Kind: VFConfigure, Inputs: map[string]EffectRef{DevicePort: {From: "call.allocation.root"}}, Parameters: Config{VF: config.VF}},
		{ID: "bind", Kind: DriverBind, Inputs: map[string]EffectRef{DevicePort: {From: "effects.configure.device"}}, Parameters: Config{Driver: VFIODriver}},
		{ID: "access", Kind: DeviceAccess, Inputs: map[string]EffectRef{DevicePort: {From: "effects.bind.device"}}, Parameters: Config{Mode: VFIOMode, AccessBackend: "cdi"}},
	}, Exports: map[string]EffectRef{VFIOPort: {From: "effects.access.device"}}}
	if err := validateBoundAllocationHook(step, plugin, PrepareHook, program); err != nil {
		t.Fatal(err)
	}
	program.Effects[0], program.Effects[1] = program.Effects[1], program.Effects[0]
	program.Effects[0].Inputs[DevicePort] = EffectRef{From: "call.allocation.root"}
	program.Effects[1].Inputs[DevicePort] = EffectRef{From: "effects.bind.device"}
	program.Effects[2].Inputs[DevicePort] = EffectRef{From: "effects.configure.device"}
	if err := validateBoundAllocationHook(step, plugin, PrepareHook, program); err == nil || !strings.Contains(err.Error(), "E_TEMPLATE_PREPARATION_ORDER") {
		t.Fatalf("VF configuration after binding accepted: %v", err)
	}
}

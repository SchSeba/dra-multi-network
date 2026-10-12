// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"fmt"
	"slices"

	"github.com/SchSeba/dra-multi-network/pkg/networkplugins"
	sdk "github.com/SchSeba/dra-multi-network/pkg/pluginapi"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
)

// PlanCleanup invokes plugins against an opaque saved execution, never newly
// selected devices. The framework restores the journal only after all responses
// agree to restore the original hook. No live CRD lookup is needed for cleanup.
func PlanCleanup(r api.RevisionSpec, hook, execution string) error {
	if hook != sdk.RemoveNetwork && hook != sdk.NodeUnprepare || execution == "" {
		return fmt.Errorf("E_PLUGIN_CLEANUP_AUTHORITY")
	}
	if err := verifyRevisionIdentity(r); err != nil {
		return err
	}
	plugins := map[string]api.PluginSpec{}
	for _, plugin := range r.Plugins {
		plugins[plugin.Name] = plugin.Spec
	}
	steps := map[string]api.Step{}
	for _, step := range r.Topology.Steps {
		steps[step.ID] = step
	}
	for _, id := range slices.Backward(r.Order) {
		step := steps[id]
		if hook == sdk.NodeUnprepare && step.Kind != AllocationKind {
			continue
		}
		plugin := plugins[step.PluginRef.Name]
		call := sdk.Request{APIVersion: sdk.CallAPIVersion, ID: id, Plugin: plugin.Implementation.Name, Version: plugin.Implementation.Version, Hook: hook, RecordedExecution: execution}
		var result sdk.Result
		if plugin.Implementation.Type == EffectProgramImplementation {
			original := sdk.ConnectNetwork
			if hook == sdk.NodeUnprepare {
				original = sdk.NodePrepare
			}
			result.RestoreRecordedHook = original
			if plugin.Kind == AllocationKind {
				var cleanup recordedCleanup
				if err := Decode(plugin.Hooks[hook].Raw, &cleanup); err != nil {
					return err
				}
				result.RestoreRecordedHook = cleanup.RestoreRecordedHook
			}
		} else {
			var err error
			result, err = networkplugins.Run(call)
			if err != nil {
				return err
			}
		}
		if err := sdk.ValidateResult(call, result); err != nil {
			return err
		}
	}
	return nil
}

// ConnectNetwork is a pure in-process plugin facade. The runner binds these
// references only to allocation handles or outputs it has already verified.
func PlanConnect(r api.RevisionSpec) (EffectProgram, error) {
	p := EffectProgram{APIVersion: EffectAPIVersion, Exports: map[string]EffectRef{}}
	plugins := map[string]api.PluginSpec{}
	for _, plugin := range r.Plugins {
		plugins[plugin.Name] = plugin.Spec
	}
	steps := map[string]api.Step{}
	for _, s := range r.Topology.Steps {
		steps[s.ID] = s
	}
	for _, id := range r.Order {
		s := steps[id]
		if plugins[s.PluginRef.Name].Implementation.Type == "EffectProgram" {
			inputs := map[string]EffectRef{}
			for port, raw := range s.Inputs {
				refs, err := References(raw)
				if err != nil {
					return p, err
				}
				inputs[port] = p.Exports[refs[0].From]
			}
			var bound EffectProgram
			var err error
			if s.Kind == AllocationKind {
				bound, err = BindAllocationHook(s, plugins[s.PluginRef.Name], ConnectHook)
			} else {
				bound, err = BindEffectHook(s, plugins[s.PluginRef.Name], inputs)
			}
			if err != nil {
				return p, err
			}
			p.Effects = append(p.Effects, bound.Effects...)
			for name, ref := range bound.Exports {
				p.Exports[id+"."+name] = ref
			}
			continue
		}
		c, err := ConfigFor(s)
		if err != nil {
			return p, err
		}
		call := builtinCall(s, plugins[s.PluginRef.Name], c, sdk.ConnectNetwork)
		for port, raw := range s.Inputs {
			refs, err := References(raw)
			if err != nil {
				return p, err
			}
			for _, ref := range refs {
				call.Inputs[port] = append(call.Inputs[port], p.Exports[ref.From])
			}
		}
		result, err := networkplugins.Run(call)
		if err != nil {
			return p, err
		}
		if err := sdk.ValidateResult(call, result); err != nil {
			return p, err
		}
		p.Effects = append(p.Effects, result.Program.Effects...)
		for name, ref := range result.Program.Exports {
			p.Exports[id+"."+name] = ref
		}
	}
	return p, ValidateProgram(p, r.Topology.EffectBudget.Allow)
}

func PlanPrepare(r api.RevisionSpec) (EffectProgram, error) {
	p := EffectProgram{APIVersion: EffectAPIVersion, Exports: map[string]EffectRef{}}
	plugins := map[string]api.PluginSpec{}
	for _, locked := range r.Plugins {
		plugins[locked.Name] = locked.Spec
	}
	for _, step := range r.Topology.Steps {
		if step.Kind != "Allocation" {
			continue
		}
		if plugins[step.PluginRef.Name].Implementation.Type == EffectProgramImplementation {
			bound, err := BindAllocationHook(step, plugins[step.PluginRef.Name], PrepareHook)
			if err != nil {
				return p, err
			}
			p.Effects = append(p.Effects, bound.Effects...)
			for name, ref := range bound.Exports {
				p.Exports[step.ID+"."+name] = ref
			}
			continue
		}
		c, err := ConfigFor(step)
		if err != nil {
			return p, err
		}
		if c.VF != nil {
			if err := ValidateVFSettings(*c.VF); err != nil {
				return p, err
			}
		}
		call := builtinCall(step, plugins[step.PluginRef.Name], c, sdk.NodePrepare)
		result, err := networkplugins.Run(call)
		if err != nil {
			return p, err
		}
		if err := sdk.ValidateResult(call, result); err != nil {
			return p, err
		}
		p.Effects = append(p.Effects, result.Program.Effects...)
		for name, ref := range result.Program.Exports {
			p.Exports[step.ID+"."+name] = ref
		}
	}
	return p, ValidateProgram(p, r.Topology.EffectBudget.Allow)
}

// Only normalized config and explicitly bound port references cross the plugin
// boundary. DRA selectors, allocation objects and journal authority stay here.
func builtinCall(step api.Step, plugin api.PluginSpec, config Config, hook string) sdk.Request {
	call := sdk.Request{APIVersion: sdk.CallAPIVersion, ID: step.ID, Plugin: plugin.Implementation.Name, Version: plugin.Implementation.Version, Hook: hook, Config: config, Inputs: map[string][]EffectRef{}}
	if step.Kind == AllocationKind {
		call.Allocation = EffectRef{From: "call.allocation." + step.ID}
		if hook == sdk.ConnectNetwork && config.Mode == VFIOMode {
			call.Prepared = map[string]EffectRef{VFIOPort: {From: "call.prepared." + step.ID + "." + VFIOPort}}
		}
	}
	return call
}

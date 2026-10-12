// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
)

const AllocationEffectsVersion = "allocation-effects-v1"
const PrepareHook = "NodePrepare"
const ConnectHook = "ConnectNetwork"
const RemoveHook = "RemoveNetwork"
const UnprepareHook = "NodeUnprepare"

type recordedCleanup struct {
	APIVersion          string `json:"apiVersion"`
	RestoreRecordedHook string `json:"restoreRecordedHook"`
}

func PluginRegistry(items []api.NetworkPlugin) map[string]api.NetworkPlugin {
	plugins := map[string]api.NetworkPlugin{}
	for _, p := range Builtins() {
		plugins[p.Name] = p
	}
	for _, p := range items {
		if p.DeletionTimestamp.IsZero() {
			plugins[p.Name] = p
		}
	}
	return plugins
}

// A template changes planning, never physical identity or allocation accounting.
// The profile is locked to a native contract this executable can actually enforce.
func AllocationProfile(plugin api.PluginSpec) (api.NetworkPlugin, error) {
	for _, profile := range Builtins() {
		if profile.Spec.Kind != AllocationKind {
			continue
		}
		if plugin.Implementation.Type == BuiltinImplementation && Digest(plugin) == Digest(profile.Spec) {
			return profile, nil
		}
		if ref := plugin.AllocationProfileRef; ref != nil && ref.Name == profile.Name && ref.Digest == Digest(profile.Spec) {
			return profile, nil
		}
	}
	return api.NetworkPlugin{}, fmt.Errorf("E_ALLOCATION_PROFILE: unknown or changed native allocation contract")
}

// Extra template config remains available for typed bindings. Framework fields
// are independently validated against the locked allocation profile.
func ConfigForPlugin(step api.Step, plugin api.PluginSpec) (Config, error) {
	if plugin.Implementation.Type != EffectProgramImplementation {
		return ConfigFor(step)
	}
	var config map[string]json.RawMessage
	if err := Decode(step.Config.Raw, &config); err != nil {
		return Config{}, err
	}
	fields := []string{IfNameField, MTUField, UpField}
	var profile api.NetworkPlugin
	if plugin.Kind == AllocationKind {
		var err error
		profile, err = AllocationProfile(plugin)
		if err != nil {
			return Config{}, err
		}
		var props ext.JSONSchemaProps
		if err := Decode(profile.Spec.ConfigSchema.Raw, &props); err != nil {
			return Config{}, err
		}
		fields = nil
		for field := range props.Properties {
			fields = append(fields, field)
		}
	}
	core := map[string]json.RawMessage{}
	for _, field := range fields {
		if value, exists := config[field]; exists {
			core[field] = value
		}
	}
	data, _ := json.Marshal(core)
	if plugin.Kind == AllocationKind {
		if err := validateConfig(ext.JSON{Raw: data}, profile.Spec.ConfigSchema); err != nil {
			return Config{}, err
		}
	}
	var result Config
	err := Decode(data, &result)
	return result, err
}

func validateAllocationTemplate(plugin api.PluginSpec) error {
	if plugin.Implementation.Version != AllocationEffectsVersion || len(plugin.Inputs) != 0 || len(plugin.Hooks) != 4 {
		return fmt.Errorf("E_TEMPLATE_HOOK: allocation requires four lifecycle hooks and no topology inputs")
	}
	profile, err := AllocationProfile(plugin)
	if err != nil {
		return err
	}
	if Digest(plugin.Outputs) != Digest(profile.Spec.Outputs) || Digest(plugin.AllowedEffects) != Digest(profile.Spec.AllowedEffects) {
		return fmt.Errorf("E_ALLOCATION_PROFILE: outputs/effects must preserve the native ownership and mode contract")
	}
	var configSchema ext.JSONSchemaProps
	if err := Decode(plugin.ConfigSchema.Raw, &configSchema); err != nil {
		return err
	}
	if configSchema.Type != SchemaObject || configSchema.AdditionalProperties == nil || configSchema.AdditionalProperties.Allows {
		return fmt.Errorf("E_TEMPLATE_SCHEMA: closed object required")
	}
	var baseline ext.JSONSchemaProps
	if err := Decode(profile.Spec.ConfigSchema.Raw, &baseline); err != nil {
		return err
	}
	for _, field := range baseline.Required {
		if !slices.Contains(configSchema.Required, field) || Digest(configSchema.Properties[field]) != Digest(baseline.Properties[field]) {
			return fmt.Errorf("E_ALLOCATION_PROFILE: preserve required config field %s", field)
		}
	}
	for hook, original := range map[string]string{RemoveHook: ConnectHook, UnprepareHook: PrepareHook} {
		var cleanup recordedCleanup
		if err := Decode(plugin.Hooks[hook].Raw, &cleanup); err != nil {
			return err
		}
		if cleanup != (recordedCleanup{APIVersion: EffectAPIVersion, RestoreRecordedHook: original}) {
			return fmt.Errorf("E_TEMPLATE_CLEANUP: %s must restore its recorded %s effects", hook, original)
		}
	}
	for _, hook := range []string{PrepareHook, ConnectHook} {
		var template programTemplate
		if err := Decode(plugin.Hooks[hook].Raw, &template); err != nil {
			return err
		}
		if template.APIVersion != EffectAPIVersion || len(template.Effects) > 256 {
			return fmt.Errorf("E_TEMPLATE_VERSION_SIZE")
		}
		seen := map[string]bool{}
		for _, e := range template.Effects {
			if !stepID.MatchString(e.ID) || seen[e.ID] || !slices.Contains(plugin.AllowedEffects, e.Kind) {
				return fmt.Errorf("E_TEMPLATE_EFFECT: %s", e.ID)
			}
			preparation := slices.Contains([]string{DriverBind, VFConfigure, DeviceAccess, PCIReset}, e.Kind)
			if e.Kind == DeviceAccess && hook == ConnectHook && profile.Spec.Outputs[RDMAPort].Type == RDMAType {
				preparation = false
			}
			if preparation != (hook == PrepareHook) {
				return fmt.Errorf("E_TEMPLATE_SCOPE: %s in %s", e.Kind, hook)
			}
			schema, roles, err := opcodeSchema(e.Kind)
			if err != nil {
				return err
			}
			if len(e.Inputs) != len(roles) {
				return fmt.Errorf("E_TEMPLATE_PORT_CARDINALITY: %s", e.ID)
			}
			for role := range roles {
				ref, exists := e.Inputs[role]
				if !exists || !validAllocationRef(ref, seen, hook) {
					return fmt.Errorf("E_TEMPLATE_REFERENCE: %s", ref.From)
				}
			}
			var props ext.JSONSchemaProps
			if err := Decode(schema.Raw, &props); err != nil {
				return err
			}
			if err := validateTemplateFields(e, configSchema, props); err != nil {
				return err
			}
			seen[e.ID] = true
		}
		for _, ref := range template.Exports {
			if !validAllocationRef(ref, seen, hook) {
				return fmt.Errorf("E_TEMPLATE_REFERENCE: %s", ref.From)
			}
		}
	}
	return nil
}

func validAllocationRef(ref EffectRef, seen map[string]bool, hook string) bool {
	if ref.From == "call.allocation" {
		return true
	}
	if hook == ConnectHook && ref.From == "call.prepared."+VFIOPort {
		return true
	}
	return strings.HasPrefix(ref.From, "effects.") && checkEffectRef(ref, seen) == nil
}

func BindAllocationHook(step api.Step, plugin api.PluginSpec, hook string) (EffectProgram, error) {
	if hook != PrepareHook && hook != ConnectHook {
		return EffectProgram{}, fmt.Errorf("E_TEMPLATE_HOOK: cleanup consumes recorded effects, never caller targets")
	}
	return bindEffectHook(step, plugin, hook, nil)
}

// Validate the complete bound plan against the allocation contract. Native
// capture then intersects these plans with actual ownership and policy grants.
func validateBoundAllocationHook(step api.Step, plugin api.PluginSpec, hook string, program EffectProgram) error {
	c, err := ConfigForPlugin(step, plugin)
	if err != nil {
		return err
	}
	allocation := "call.allocation." + step.ID
	if c.Representor && hook == ConnectHook {
		if len(program.Effects) == 0 {
			return fmt.Errorf("E_TEMPLATE_REPRESENTOR: explicit allocation mapping required")
		}
		mapping := program.Effects[0]
		if mapping.Kind != RepresentorMap || len(mapping.Inputs) != 1 || mapping.Inputs[DevicePort].From != allocation || Digest(mapping.Parameters) != Digest(Config{}) || program.Exports[RepresentorPort].From != "effects."+mapping.ID+"."+RepresentorPort {
			return fmt.Errorf("E_TEMPLATE_REPRESENTOR: mapping must preserve its allocated VF")
		}
		program.Effects = program.Effects[1:]
		program.Exports = maps.Clone(program.Exports)
		delete(program.Exports, RepresentorPort)
	}
	if hook == PrepareHook {
		device := allocation
		reset, configured, bound, access := false, false, false, false
		for _, e := range program.Effects {
			if e.Inputs[DevicePort].From != device || configured && e.Kind == VFConfigure || bound && (e.Kind == DriverBind || e.Kind == VFConfigure) || access {
				return fmt.Errorf("E_TEMPLATE_PREPARATION_ORDER")
			}
			switch e.Kind {
			case PCIReset:
				if reset || configured || bound || c.ResetMethod == "" || e.Parameters.ResetMethod != c.ResetMethod {
					return fmt.Errorf("E_TEMPLATE_PREPARATION_ORDER: reset must precede other preparation")
				}
				reset = true
			case VFConfigure:
				if c.VF == nil || Digest(e.Parameters.VF) != Digest(c.VF) {
					return fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG: VF settings")
				}
				if err := ValidateVFSettings(*c.VF); err != nil {
					return err
				}
				configured = true
			case DriverBind:
				if e.Parameters.Driver != c.Driver || c.Driver == "" || c.Mode == KernelMode && c.Driver == VFIODriver {
					return fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG: target driver")
				}
				bound = true
			case DeviceAccess:
				if !bound || e.Parameters.Mode != c.Mode || e.Parameters.AccessBackend != c.AccessBackend || c.Mode != VFIOMode {
					return fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG: device access")
				}
				access = true
			default:
				return fmt.Errorf("E_TEMPLATE_SCOPE")
			}
			device = "effects." + e.ID + ".device"
		}
		if reset != (c.ResetMethod != "") || configured != (c.VF != nil) || bound != (c.Driver != "") || access != (c.Mode == VFIOMode) {
			return fmt.Errorf("E_TEMPLATE_PREPARATION_COVERAGE")
		}
		if c.Mode == VFIOMode {
			if len(program.Exports) != 1 || program.Exports[VFIOPort].From != device {
				return fmt.Errorf("E_TEMPLATE_EXPORT: export the allocated VFIO device")
			}
		} else if len(program.Exports) != 0 {
			return fmt.Errorf("E_TEMPLATE_EXPORT: kernel preparation keeps recorded state, not host interface exports")
		}
		return nil
	}
	if c.Mode == VFIOMode {
		if len(program.Effects) != 0 || len(program.Exports) != 1 || program.Exports[VFIOPort].From != "call.prepared."+step.ID+"."+VFIOPort {
			return fmt.Errorf("E_TEMPLATE_EXPORT: connect must verify the prepared VFIO device")
		}
		return nil
	}
	if c.Mode == RDMAMode {
		exports := 1
		if c.RDMAProfile == IPoIBSinglePort {
			exports++
		}
		if len(program.Effects) != 2 || len(program.Exports) != exports {
			return fmt.Errorf("E_TEMPLATE_CONNECT_SHAPE: whole-HCA namespace and device-access operations required")
		}
		e := program.Effects[0]
		if e.Kind != RDMANamespace || e.Inputs[DevicePort].From != allocation || e.Parameters.IfName != c.IfName || e.Parameters.MTU != c.MTU || e.Parameters.Up != c.Up || e.Parameters.RDMAProfile != c.RDMAProfile || e.Parameters.MADAccess != c.MADAccess || program.Exports[RDMAPort].From != "effects."+program.Effects[1].ID+".device" {
			return fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG: whole-HCA namespace connection")
		}
		if c.RDMAProfile == IPoIBSinglePort && program.Exports[InterfacePort].From != "effects."+e.ID+".interface" {
			return fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG: typed IPoIB interface")
		}
		access := program.Effects[1]
		if access.Kind != DeviceAccess || access.Parameters.Mode != RDMAMode || access.Parameters.AccessBackend != c.AccessBackend || access.Inputs[DevicePort].From != "effects."+e.ID+".device" {
			return fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG: namespace-scoped runtime device access")
		}
		return nil
	}
	if len(program.Effects) != 2 || len(program.Exports) != 1 {
		return fmt.Errorf("E_TEMPLATE_CONNECT_SHAPE: realize and configure the allocated interface")
	}
	create, configure := program.Effects[0], program.Effects[1]
	role, kind := InterfacePort, Move
	if c.Mode == MacvlanBridgeMode || c.Mode == IPvlanL2Mode {
		role, kind = ParentPort, CreateMacvlan
		if c.Mode == IPvlanL2Mode {
			kind = CreateIPvlan
		}
		if create.Parameters.Mode != c.Mode || create.Parameters.MTU != c.MTU {
			return fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG: shared child")
		}
	}
	if create.Kind != kind || create.Inputs[role].From != allocation || create.Parameters.IfName != c.IfName || configure.Kind != Configure || configure.Inputs[InterfacePort].From != "effects."+create.ID+".interface" || configure.Parameters.IfName != "" && configure.Parameters.IfName != c.IfName || configure.Parameters.MTU != c.MTU || configure.Parameters.Up != c.Up || program.Exports[InterfacePort].From != "effects."+configure.ID+".interface" {
		return fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG: allocation connection")
	}
	return nil
}

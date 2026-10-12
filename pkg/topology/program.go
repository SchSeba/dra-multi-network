// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/pluginapi"
)

const EffectAPIVersion = "effects.dra-multi-network.schseba.github.io/v1alpha1"
const effectNamespace = "effects"
const (
	Move          = "linux/link-move/v1"
	Configure     = "linux/link-configure/v1"
	CreateBond    = "linux/create-bond/v1"
	AttachMaster  = "linux/attach-master/v1"
	CreateVLAN    = "linux/create-vlan/v1"
	CreateMacvlan = "linux/create-macvlan/v1"
	CreateIPvlan  = "linux/create-ipvlan/v1"
)

type EffectRef = pluginapi.EffectRef
type EffectPredicate = pluginapi.EffectPredicate
type EffectRecovery = pluginapi.EffectRecovery
type NetworkEffect = pluginapi.NetworkEffect
type EffectProgram = pluginapi.EffectProgram

func ValidateProgram(p EffectProgram, budget []string) error {
	if p.APIVersion != EffectAPIVersion || len(p.Effects) > 256 {
		return fmt.Errorf("E_EFFECT_VERSION_SIZE")
	}
	seen := map[string]bool{}
	origins := map[string]string{}
	for _, e := range p.Effects {
		if e.ID == "" || seen[e.ID] {
			return fmt.Errorf("E_EFFECT_ID: %s", e.ID)
		}
		if !slices.Contains(budget, e.Kind) {
			return fmt.Errorf("E_EFFECT_DENIED: %s", e.Kind)
		}
		switch e.Kind {
		case Move, Configure, CreateBond, AttachMaster, CreateVLAN, CreateMacvlan, CreateIPvlan, DriverBind, DeviceAccess, VFConfigure, PCIReset, RDMANamespace, RepresentorMap, BridgeConfigure, OVSConfigure, UplinkConfigure:
		default:
			return fmt.Errorf("E_EFFECT_OPCODE: %s", e.Kind)
		}
		if e.Kind == BridgeConfigure || e.Kind == OVSConfigure || e.Kind == UplinkConfigure {
			if err := ValidateBridgeConfig(e.Parameters); err != nil {
				return err
			}
			if e.Kind == OVSConfigure {
				if err := ValidateOVSConfig(e.Parameters); err != nil {
					return err
				}
			}
			c := e.Parameters
			if len(e.Inputs) != 1 || e.Inputs[RepresentorPort].From == "" || Digest(c) != Digest(Config{Bridge: c.Bridge, MTU: c.MTU, Up: c.Up, VlanID: c.VlanID, Trunk: c.Trunk, Uplink: c.Uplink}) || (e.Kind == UplinkConfigure) != (c.Uplink != "") {
				return fmt.Errorf("E_REPRESENTOR_BRIDGE_PAYLOAD")
			}
		}
		if e.Kind == RepresentorMap && (len(e.Inputs) != 1 || e.Inputs[DevicePort].From == "" || Digest(e.Parameters) != Digest(Config{})) {
			return fmt.Errorf("E_REPRESENTOR_MAPPING_PAYLOAD")
		}
		if e.Parameters.MAC != "" || e.Parameters.Alias != nil {
			if e.Kind != Configure {
				return fmt.Errorf("E_LINK_CONFIG_OPCODE: MAC/alias require link-configure")
			}
			if err := ValidateLinkConfig(e.Parameters); err != nil {
				return err
			}
		}
		origin := e.Kind
		if e.Kind == Configure {
			parts := strings.Split(e.Inputs[InterfacePort].From, ".")
			if len(parts) == 3 && parts[0] == effectNamespace {
				origin = origins[parts[1]]
			}
			if e.Parameters.MAC != "" && (origin == CreateBond || origin == CreateIPvlan) {
				return fmt.Errorf("E_LINK_MAC_PROFILE: %s controls its own MAC", origin)
			}
		}
		if e.Recovery.Strategy != RecoveryStrategy(e.Kind) || len(e.Requires) != 1 || e.Requires[0].Kind != AllocationAndOwnedInputs || len(e.Postconditions) != 1 || e.Postconditions[0].Kind != NativeStateMatch {
			return fmt.Errorf("E_EFFECT_CONTRACT: %s", e.ID)
		}
		for _, ref := range e.Inputs {
			if err := checkEffectRef(ref, seen); err != nil {
				return err
			}
		}
		seen[e.ID] = true
		origins[e.ID] = origin
	}
	for _, ref := range p.Exports {
		if err := checkEffectRef(ref, seen); err != nil {
			return err
		}
	}
	return nil
}
func checkEffectRef(ref EffectRef, seen map[string]bool) error {
	if strings.HasPrefix(ref.From, "call.allocation.") {
		return nil
	}
	parts := strings.Split(ref.From, ".")
	if len(parts) == 4 && parts[0] == "call" && parts[1] == "prepared" && stepID.MatchString(parts[2]) && parts[3] == VFIOPort {
		return nil
	}
	if len(parts) != 3 || parts[0] != effectNamespace || !seen[parts[1]] || !slices.Contains([]string{InterfacePort, DevicePort, RepresentorPort}, parts[2]) {
		return fmt.Errorf("E_EFFECT_REFERENCE: %s", ref.From)
	}
	return nil
}

// Templates retain literal field presence so a false/zero literal cannot also
// be overwritten by a parameter binding. Only validated scalar config values
// and granted port handles enter the closed reference environment.
type effectTemplate struct {
	ID                string                     `json:"id"`
	Kind              string                     `json:"kind"`
	Inputs            map[string]EffectRef       `json:"inputs,omitempty"`
	Parameters        map[string]json.RawMessage `json:"parameters"`
	ParameterBindings map[string]EffectRef       `json:"parameterBindings,omitempty"`
	Requires          []EffectPredicate          `json:"requires"`
	Postconditions    []EffectPredicate          `json:"postconditions"`
	Recovery          EffectRecovery             `json:"recovery"`
}
type programTemplate struct {
	APIVersion string               `json:"apiVersion"`
	Effects    []effectTemplate     `json:"effects"`
	Exports    map[string]EffectRef `json:"exports"`
}

//nolint:goconst // JSON Schema vocabulary remains literal.
func opcodeSchema(kind string) (ext.JSON, map[string]string, error) {
	common := map[string]any{IfNameField: map[string]any{"type": SchemaString, "pattern": `^[A-Za-z0-9_.-]{1,15}$`}, MTUField: integer(576, 9000), UpField: map[string]any{"type": SchemaBoolean}}
	inputs := map[string]string{}
	required := []string{IfNameField, MTUField}
	switch kind {
	case RepresentorMap:
		common, required = map[string]any{}, nil
		inputs[DevicePort] = "ExclusiveConfigure"
	case BridgeConfigure, OVSConfigure, UplinkConfigure:
		for _, plugin := range switchdevProfiles(nil) {
			if plugin.Name == AcceleratedBridgePlugin && kind != UplinkConfigure || plugin.Name == AcceleratedBridgePlugin+"-uplink" && kind == UplinkConfigure {
				return plugin.Spec.ConfigSchema, map[string]string{RepresentorPort: AccessExclusiveMaster}, nil
			}
		}
	case RDMANamespace:
		inputs[DevicePort] = "ExclusiveConfigure"
		common[RDMAProfileField] = enum(RoCESinglePort, IBVerbsSinglePort, IPoIBSinglePort)
		common[MTUField] = integer(256, 65520)
		common["madAccess"] = map[string]any{"type": SchemaBoolean}
		props := map[string]any{"type": SchemaObject, "additionalProperties": false, "properties": common, "required": []string{RDMAProfileField}, "oneOf": []any{
			map[string]any{"properties": map[string]any{RDMAProfileField: enum(IBVerbsSinglePort)}, "not": map[string]any{"anyOf": []any{map[string]any{"required": []string{IfNameField}}, map[string]any{"required": []string{MTUField}}, map[string]any{"required": []string{UpField}}}}},
			map[string]any{"properties": map[string]any{RDMAProfileField: enum(RoCESinglePort), MTUField: integer(576, 9000), "madAccess": map[string]any{"enum": []bool{false}}}, "required": []string{IfNameField, MTUField, UpField}},
			map[string]any{"properties": map[string]any{RDMAProfileField: enum(IPoIBSinglePort)}, "required": []string{IfNameField, MTUField, UpField}},
		}}
		data, _ := json.Marshal(props)
		return ext.JSON{Raw: data}, inputs, nil
	case PCIReset:
		common = map[string]any{"resetMethod": enum(FLR)}
		required = []string{"resetMethod"}
		inputs[DevicePort] = "ExclusiveConfigure"
	case Move:
		common = map[string]any{IfNameField: common[IfNameField]}
		required = []string{IfNameField}
		inputs[InterfacePort] = "ExclusiveConfigure"
	case CreateMacvlan, CreateIPvlan:
		mode := MacvlanBridgeMode
		if kind == CreateIPvlan {
			mode = IPvlanL2Mode
		}
		common["mode"] = enum(mode)
		required = append(required, "mode")
		inputs[ParentPort] = AccessCreateChild
	case DriverBind:
		common = map[string]any{"driver": map[string]any{"type": SchemaString, "pattern": `^[A-Za-z0-9_-]{1,64}$`}}
		required = []string{"driver"}
		inputs[DevicePort] = "ExclusiveConfigure"
	case DeviceAccess:
		common = map[string]any{"mode": enum(VFIOMode, RDMAMode), "accessBackend": enum("cdi", "nri")}
		required = []string{"mode", "accessBackend"}
		inputs[DevicePort] = AccessOwned
	case VFConfigure:
		common = map[string]any{"vf": vfSettingsSchema()}
		required = []string{"vf"}
		inputs[DevicePort] = "ExclusiveConfigure"
	case CreateBond:
		common["mode"] = enum("active-backup")
		common["monitorIntervalMs"] = integer(1, 10000)
		common["failOverMac"] = enum("active")
		required = append(required, "mode", "monitorIntervalMs", "failOverMac")
	case CreateVLAN:
		common["vlanId"] = integer(1, 4094)
		required = append(required, "vlanId")
		inputs[ParentPort] = AccessCreateChild
	case AttachMaster:
		common = map[string]any{}
		required = nil
		inputs[InterfacePort] = AccessExclusiveMaster
		inputs["master"] = AccessOwned
	case Configure:
		common["mac"] = map[string]any{"type": SchemaString, "pattern": `^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`}
		common["alias"] = map[string]any{"type": SchemaString, "maxLength": 128, "pattern": `^[A-Za-z0-9_.:/ -]*$`}
		inputs[InterfacePort] = AccessOwned
		required = []string{MTUField, UpField}
	default:
		return ext.JSON{}, nil, fmt.Errorf("E_TEMPLATE_OPCODE: %s", kind)
	}
	return schema(common, required), inputs, nil
}

var linkLabel = regexp.MustCompile(`^[A-Za-z0-9_.:/ -]{0,128}$`)

func ValidateLinkConfig(c Config) error {
	if c.MAC != "" {
		mac, err := net.ParseMAC(c.MAC)
		if err != nil || len(mac) != 6 || mac[0]&1 != 0 || mac.String() == "00:00:00:00:00:00" || mac.String() != c.MAC {
			return fmt.Errorf("E_LINK_MAC: require a canonical nonzero unicast Ethernet MAC")
		}
	}
	if c.Alias != nil && !linkLabel.MatchString(*c.Alias) {
		return fmt.Errorf("E_LINK_ALIAS: require at most 128 printable label characters without ownership delimiters")
	}
	return nil
}
func validateTemplate(p api.NetworkPlugin) error {
	if p.Spec.Kind == AllocationKind {
		return validateAllocationTemplate(p.Spec)
	}
	if p.Spec.Kind != OperationKind || p.Spec.AllocationProfileRef != nil || p.Spec.Implementation.Version != ContractVersion || len(p.Spec.Hooks) != 1 {
		return fmt.Errorf("E_TEMPLATE_HOOK: require an Operation with one ConnectNetwork hook")
	}
	raw, exists := p.Spec.Hooks["ConnectNetwork"]
	if !exists {
		return fmt.Errorf("E_TEMPLATE_HOOK")
	}
	var configSchema ext.JSONSchemaProps
	if err := Decode(p.Spec.ConfigSchema.Raw, &configSchema); err != nil {
		return err
	}
	if configSchema.Type != SchemaObject || configSchema.AdditionalProperties == nil || configSchema.AdditionalProperties.Allows {
		return fmt.Errorf("E_TEMPLATE_SCHEMA: closed object required")
	}
	for _, name := range []string{IfNameField, MTUField, UpField} {
		if !slices.Contains(configSchema.Required, name) {
			return fmt.Errorf("E_TEMPLATE_SCHEMA: require %s", name)
		}
	}
	if len(p.Spec.Outputs) != 1 || p.Spec.Outputs[InterfacePort] != (api.Port{Type: InterfaceType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Ownership: OwnershipCreated}) {
		return fmt.Errorf("E_TEMPLATE_OUTPUT: one created kernel Ethernet interface required")
	}
	for _, port := range p.Spec.Inputs {
		if port.Type != InterfaceType || port.LinkType != Ethernet || port.MinItems != 1 || port.MaxItems != 1 || !slices.Contains([]string{AccessCreateChild, AccessExclusiveMaster}, port.Access) {
			return fmt.Errorf("E_TEMPLATE_PORT")
		}
	}
	var template programTemplate
	if err := Decode(raw.Raw, &template); err != nil {
		return err
	}
	if template.APIVersion != EffectAPIVersion || len(template.Effects) == 0 || len(template.Effects) > 256 || len(template.Exports) != 1 {
		return fmt.Errorf("E_TEMPLATE_VERSION_SIZE")
	}
	seen := map[string]bool{}
	creates, attachments := 0, 0
	createdKind := ""
	origins := map[string]string{}
	usedInputs := map[string]bool{}
	memberInputs := map[string]bool{}
	for _, e := range template.Effects {
		if !slices.Contains([]string{CreateBond, CreateVLAN, AttachMaster, Configure}, e.Kind) {
			return fmt.Errorf("E_TEMPLATE_OPCODE: %s is not available to an Operation hook", e.Kind)
		}
		if !stepID.MatchString(e.ID) || seen[e.ID] || !slices.Contains(p.Spec.AllowedEffects, e.Kind) {
			return fmt.Errorf("E_TEMPLATE_EFFECT: %s", e.ID)
		}
		if e.Kind == CreateBond || e.Kind == CreateVLAN {
			creates++
			createdKind = e.Kind
		}
		if e.Kind == AttachMaster {
			attachments++
		}
		target, roles, err := opcodeSchema(e.Kind)
		if err != nil {
			return err
		}
		var props ext.JSONSchemaProps
		if err := Decode(target.Raw, &props); err != nil {
			return err
		}
		if len(e.Inputs) != len(roles) {
			return fmt.Errorf("E_TEMPLATE_PORT_CARDINALITY: %s", e.ID)
		}
		for role, access := range roles {
			ref, exists := e.Inputs[role]
			if !exists {
				return fmt.Errorf("E_TEMPLATE_PORT: %s", role)
			}
			if strings.HasPrefix(ref.From, "call.inputs.") {
				name := strings.TrimPrefix(ref.From, "call.inputs.")
				usedInputs[name] = true
				if e.Kind == AttachMaster && role == InterfacePort {
					if memberInputs[name] {
						return fmt.Errorf("E_TEMPLATE_DUPLICATE_MEMBER")
					}
					memberInputs[name] = true
				}
				port, exists := p.Spec.Inputs[name]
				if !exists || access == AccessOwned || port.Access != access {
					return fmt.Errorf("E_TEMPLATE_GRANT: %s", ref.From)
				}
			} else if strings.HasPrefix(ref.From, "call.") {
				return fmt.Errorf("E_TEMPLATE_REFERENCE: %s", ref.From)
			} else if err := checkEffectRef(ref, seen); err != nil {
				return err
			} else {
				origin := origins[strings.Split(ref.From, ".")[1]]
				if access != AccessOwned || origin == "" || (e.Kind == AttachMaster && origin != CreateBond) {
					return fmt.Errorf("E_TEMPLATE_GRANT: %s", ref.From)
				}
			}
		}
		if err := validateTemplateFields(e, configSchema, props); err != nil {
			return err
		}
		if e.Kind == CreateBond || e.Kind == CreateVLAN {
			origins[e.ID] = e.Kind
		}
		if e.Kind == Configure {
			origins[e.ID] = origins[strings.Split(e.Inputs[InterfacePort].From, ".")[1]]
		}
		seen[e.ID] = true
	}
	if len(usedInputs) != len(p.Spec.Inputs) {
		return fmt.Errorf("E_TEMPLATE_UNUSED_PORT")
	}
	if creates != 1 || (createdKind == CreateBond && (attachments < 2 || attachments > 8)) || (createdKind == CreateVLAN && attachments != 0) {
		return fmt.Errorf("E_TEMPLATE_SHAPE: one VLAN or a bond with 2–8 members required")
	}
	ref, exists := template.Exports[InterfacePort]
	if !exists {
		return fmt.Errorf("E_TEMPLATE_EXPORT")
	}
	return checkEffectRef(ref, seen)
}

// BindEffectHook substitutes values and references as JSON data, then validates
// the complete bound opcode payload before the controller publishes a revision.
func BindEffectHook(step api.Step, plugin api.PluginSpec, inputs map[string]EffectRef) (EffectProgram, error) {
	return bindEffectHook(step, plugin, "ConnectNetwork", inputs)
}

func bindEffectHook(step api.Step, plugin api.PluginSpec, hook string, inputs map[string]EffectRef) (EffectProgram, error) {
	p := EffectProgram{APIVersion: EffectAPIVersion, Exports: map[string]EffectRef{}}
	var template programTemplate
	if err := Decode(plugin.Hooks[hook].Raw, &template); err != nil {
		return p, err
	}
	var config map[string]json.RawMessage
	if err := Decode(step.Config.Raw, &config); err != nil {
		return p, err
	}
	bindRef := func(ref EffectRef) (EffectRef, error) {
		if plugin.Kind == AllocationKind && ref.From == "call.allocation" {
			return EffectRef{From: "call.allocation." + step.ID}, nil
		}
		if plugin.Kind == AllocationKind && ref.From == "call.prepared."+VFIOPort {
			return EffectRef{From: "call.prepared." + step.ID + "." + VFIOPort}, nil
		}
		if strings.HasPrefix(ref.From, "call.inputs.") {
			bound, exists := inputs[strings.TrimPrefix(ref.From, "call.inputs.")]
			if !exists {
				return ref, fmt.Errorf("E_TEMPLATE_INPUT: %s", ref.From)
			}
			return bound, nil
		}
		parts := strings.Split(ref.From, ".")
		if len(parts) != 3 || parts[0] != effectNamespace {
			return ref, fmt.Errorf("E_TEMPLATE_REFERENCE")
		}
		return EffectRef{From: "effects." + step.ID + "-" + parts[1] + "." + parts[2]}, nil
	}
	for _, e := range template.Effects {
		parameters := map[string]json.RawMessage{}
		for field, value := range e.Parameters {
			parameters[field] = value
		}
		for field, ref := range e.ParameterBindings {
			value, exists := config[strings.TrimPrefix(ref.From, "call.config.")]
			if !exists {
				return p, fmt.Errorf("E_TEMPLATE_CONFIG: %s", ref.From)
			}
			parameters[field] = value
		}
		data, _ := json.Marshal(parameters)
		target, _, err := opcodeSchema(e.Kind)
		if err != nil {
			return p, err
		}
		if err := validateConfig(ext.JSON{Raw: data}, target); err != nil {
			return p, err
		}
		var c Config
		if err := Decode(data, &c); err != nil {
			return p, err
		}
		if c.IfName != "" && !ValidIfName(c.IfName) {
			return p, fmt.Errorf("E_INTERFACE_NAME: %s", c.IfName)
		}
		effect := NetworkEffect{ID: step.ID + "-" + e.ID, Kind: e.Kind, Parameters: c, Inputs: map[string]EffectRef{}, Requires: e.Requires, Postconditions: e.Postconditions, Recovery: e.Recovery}
		for port, ref := range e.Inputs {
			bound, err := bindRef(ref)
			if err != nil {
				return p, err
			}
			effect.Inputs[port] = bound
		}
		p.Effects = append(p.Effects, effect)
	}
	for name, ref := range template.Exports {
		bound, err := bindRef(ref)
		if err != nil {
			return p, err
		}
		p.Exports[name] = bound
	}
	if plugin.Kind == AllocationKind {
		return p, validateBoundAllocationHook(step, plugin, hook, p)
	}
	var core Config
	data, _ := json.Marshal(map[string]json.RawMessage{IfNameField: config[IfNameField], MTUField: config[MTUField], UpField: config[UpField]})
	if err := Decode(data, &core); err != nil {
		return p, err
	}
	var creation *NetworkEffect
	for i := range p.Effects {
		e := &p.Effects[i]
		if e.Kind == CreateBond || e.Kind == CreateVLAN {
			creation = e
			if e.Parameters.IfName != core.IfName || e.Parameters.MTU != core.MTU {
				return p, fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG")
			}
		}
	}
	if creation == nil {
		return p, fmt.Errorf("E_TEMPLATE_SHAPE")
	}
	export := p.Exports[InterfacePort].From
	var final *NetworkEffect
	for i := range p.Effects {
		if "effects."+p.Effects[i].ID+".interface" == export {
			final = &p.Effects[i]
		}
	}
	if final == nil || final.Kind != Configure || final.Inputs[InterfacePort].From != "effects."+creation.ID+".interface" || final.Parameters.MTU != core.MTU || final.Parameters.Up != core.Up {
		return p, fmt.Errorf("E_TEMPLATE_OUTPUT_CONFIG: export a configured created interface")
	}
	return p, nil
}

func validateTemplateFields(e effectTemplate, configSchema, props ext.JSONSchemaProps) error {
	literals := map[string]any{}
	for field, value := range e.Parameters {
		if _, exists := props.Properties[field]; !exists {
			return fmt.Errorf("E_TEMPLATE_PARAMETER: %s", field)
		}
		var v any
		if err := Decode(value, &v); err != nil {
			return err
		}
		literals[field] = v
	}
	// Validate literal fields independently; required fields may come from bindings.
	literalSchema := props
	literalSchema.Required = nil
	data, _ := json.Marshal(literals)
	literalRaw, _ := json.Marshal(literalSchema)
	if err := validateConfig(ext.JSON{Raw: data}, ext.JSON{Raw: literalRaw}); err != nil {
		return err
	}
	for field, ref := range e.ParameterBindings {
		if _, exists := e.Parameters[field]; exists {
			return fmt.Errorf("E_TEMPLATE_PARAMETER_CONFLICT: %s", field)
		}
		target, exists := props.Properties[field]
		if !exists {
			return fmt.Errorf("E_TEMPLATE_PARAMETER: %s", field)
		}
		if !strings.HasPrefix(ref.From, "call.config.") {
			return fmt.Errorf("E_TEMPLATE_BINDING: %s", ref.From)
		}
		source, exists := configSchema.Properties[strings.TrimPrefix(ref.From, "call.config.")]
		if !exists || source.Type != target.Type || !slices.Contains([]string{SchemaString, "integer", SchemaBoolean, SchemaObject}, source.Type) || source.Type == SchemaObject && Digest(source) != Digest(target) {
			return fmt.Errorf("E_TEMPLATE_BINDING_TYPE: %s", field)
		}
	}
	for _, field := range props.Required {
		_, literal := e.Parameters[field]
		_, binding := e.ParameterBindings[field]
		if !literal && !binding {
			return fmt.Errorf("E_TEMPLATE_PARAMETER_MISSING: %s", field)
		}
	}
	if e.Recovery.Strategy != RecoveryStrategy(e.Kind) || len(e.Requires) != 1 || e.Requires[0].Kind != AllocationAndOwnedInputs || len(e.Postconditions) != 1 || e.Postconditions[0].Kind != NativeStateMatch {
		return fmt.Errorf("E_TEMPLATE_CONTRACT")
	}
	return nil
}

// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	internalext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	validation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	strictjson "sigs.k8s.io/json"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/pluginapi"
)

const (
	BuiltinImplementation = "Builtin"
	AllocationKind        = "Allocation"
	RecordedEffects       = "RecordedEffects"
	BondPlugin            = "bond-cni"
	ContractVersion       = "v1"
	Ethernet              = "ethernet"
	ConfigureEffect       = "linux/link-configure/v1"
	SchemaString          = "string"
	SchemaBoolean         = "boolean"
	SchemaObject          = "object"
	KernelMode            = "kernel"
	OperationKind         = "Operation"
	AccessCreateChild     = "CreateChild"
	AccessExclusiveMaster = "ExclusiveMaster"
	OwnershipCreated      = "Created"
	AccessOwned           = "Owned"
)

const (
	ObserveAndReconcile = "ObserveAndReconcile"
	IPvlanPlugin        = "ipvlan-cni"
	VLANPlugin          = "vlan-cni"
	ParentPort          = "parent"
	UpField             = "up"
	MacvlanBridgeMode   = "bridge"
)

const (
	AllocationAndOwnedInputs = "allocation-and-owned-inputs"
	NativeStateMatch         = "native-state-match"
	MacvlanPlugin            = "macvlan-cni"
	IPvlanL2Mode             = "l2"
)

const EffectProgramImplementation = "EffectProgram"
const IfNameField = "ifName"
const MTUField = "mtu"
const InterfacePort = "interface"

const CompilerVersion = "v1alpha1-kernel-1"
const InterfaceType = "KernelInterface/v1"

func Decode(data []byte, value any) error {
	if len(data) > 256*1024 {
		return fmt.Errorf("E_SIZE: payload exceeds 256 KiB")
	}
	errs, err := strictjson.UnmarshalStrict(data, value)
	if err != nil {
		return err
	}
	if len(errs) != 0 {
		return fmt.Errorf("E_CONFIG_SCHEMA: %v", errs)
	}
	return nil
}
func Digest(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	} // All callers pass JSON API structs.
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type Config = pluginapi.Config

var stepID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
var ifName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

func ValidIfName(name string) bool {
	return ifName.MatchString(name) && !slices.Contains([]string{".", "..", "lo", "eth0"}, name)
}

func ConfigFor(step api.Step) (Config, error) {
	var c Config
	if err := Decode(step.Config.Raw, &c); err != nil {
		return c, fmt.Errorf("steps.%s.config: %w", step.ID, err)
	}
	return c, nil
}
func References(raw ext.JSON) ([]api.Reference, error) {
	if len(raw.Raw) == 0 {
		return nil, fmt.Errorf("E_PORT_REFERENCE: empty reference")
	}
	var refs []api.Reference
	if raw.Raw[0] == '[' {
		if err := Decode(raw.Raw, &refs); err != nil {
			return nil, err
		}
	} else {
		var ref api.Reference
		if err := Decode(raw.Raw, &ref); err != nil {
			return nil, err
		}
		refs = []api.Reference{ref}
	}
	return refs, nil
}

//nolint:goconst // JSON Schema vocabulary remains literal.
func schema(properties map[string]any, required []string) ext.JSON {
	data, _ := json.Marshal(map[string]any{"type": SchemaObject, "additionalProperties": false, "properties": properties, "required": required})
	return ext.JSON{Raw: data}
}

//nolint:goconst // JSON Schema vocabulary remains literal.
func enum(values ...string) map[string]any {
	return map[string]any{"type": SchemaString, "enum": values}
}
func integer(min, max int) map[string]any {
	return map[string]any{"type": "integer", "minimum": min, "maximum": max}
}

//nolint:goconst // JSON Schema vocabulary remains literal.
func Builtins() []api.NetworkPlugin {
	common := map[string]any{IfNameField: map[string]any{"type": "string", "pattern": `^[A-Za-z0-9_.-]{1,15}$`}, MTUField: integer(576, 9000), UpField: map[string]any{"type": "boolean"}}
	var result []api.NetworkPlugin
	for _, name := range []string{HostPlugin, SRIOVPlugin, BondPlugin, VLANPlugin, MacvlanPlugin, IPvlanPlugin} {
		props := maps.Clone(common)
		required := []string{IfNameField, MTUField, UpField}
		p := api.NetworkPlugin{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "NetworkPlugin"}, ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: api.PluginSpec{
			Implementation: api.Implementation{Type: BuiltinImplementation, Name: name, Version: ContractVersion}, Kind: AllocationKind, StateContract: api.StateContract{Version: ContractVersion, Cleanup: RecordedEffects},
			Outputs: map[string]api.Port{"interface": {Type: InterfaceType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}},
		}}
		switch name {
		case HostPlugin, SRIOVPlugin:
			props["mode"] = enum("kernel")
			props["linkType"] = enum(Ethernet)
			props["rdmaAccess"] = map[string]any{"type": "boolean", "enum": []bool{false}}
			required = append(required, "mode", "linkType", "rdmaAccess")
			p.Spec.AllowedEffects = []string{"linux/link-move/v1", ConfigureEffect}
		case BondPlugin:
			p.Spec.Kind = OperationKind
			props["mode"] = enum("active-backup")
			props["monitorIntervalMs"] = integer(1, 10000)
			props["failOverMac"] = enum("active")
			required = append(required, "mode", "monitorIntervalMs", "failOverMac")
			p.Spec.Inputs = map[string]api.Port{"members": {Type: InterfaceType, LinkType: Ethernet, MinItems: 2, MaxItems: 8, Access: AccessExclusiveMaster}}
			p.Spec.AllowedEffects = []string{"linux/create-bond/v1", "linux/attach-master/v1", ConfigureEffect}
		case VLANPlugin:
			p.Spec.Kind = OperationKind
			props["vlanId"] = integer(1, 4094)
			required = append(required, "vlanId")
			p.Spec.Inputs = map[string]api.Port{ParentPort: {Type: InterfaceType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Access: AccessCreateChild}}
			p.Spec.AllowedEffects = []string{"linux/create-vlan/v1", ConfigureEffect}
		case MacvlanPlugin, IPvlanPlugin:
			mode := MacvlanBridgeMode
			effect := "linux/create-macvlan/v1"
			if name == IPvlanPlugin {
				mode = IPvlanL2Mode
				effect = "linux/create-ipvlan/v1"
			}
			props["mode"] = enum(mode)
			props["linkType"] = enum(Ethernet)
			required = append(required, "mode", "linkType")
			p.Spec.Outputs["interface"] = api.Port{Type: InterfaceType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Ownership: OwnershipCreated}
			p.Spec.AllowedEffects = []string{effect, ConfigureEffect}
		}
		p.Spec.ConfigSchema = schema(props, required)
		result = append(result, p)
	}
	result = append(result, hardwareProfiles()...)
	result = append(result, configuredVFProfiles(result)...)
	result = append(result, kernelBindingProfiles()...)
	result = append(result, resetProfiles()...)
	result = append(result, roceProfiles()...)
	result = append(result, switchdevProfiles(result)...)
	result = append(result, ibProfiles()...)
	return append(result, ipamPlugins()...)
}
func ValidatePlugin(p api.NetworkPlugin) error {
	if p.Spec.AllocationDriver != "" && (p.Spec.Implementation.Type != BuiltinImplementation || p.Name != IPAMPlugin) {
		return fmt.Errorf("E_ALLOCATION_DRIVER: only certified IPAM may delegate allocation")
	}
	if p.Spec.StateContract != (api.StateContract{Version: ContractVersion, Cleanup: RecordedEffects}) {
		return fmt.Errorf("E_STATE_CONTRACT: unsupported cleanup contract")
	}
	if p.Spec.Implementation.Type == EffectProgramImplementation {
		return validateTemplate(p)
	}
	if p.Spec.Implementation.Type != BuiltinImplementation {
		return fmt.Errorf("E_PLUGIN_IMPLEMENTATION: this build only executes locked builtin plugins")
	}
	for _, builtin := range Builtins() {
		if p.Name == builtin.Name && Digest(builtin.Spec) == Digest(p.Spec) {
			return nil
		}
	}
	return fmt.Errorf("E_PLUGIN_CONTRACT: builtin descriptor differs from the implementation contract")
}
func validateConfig(raw ext.JSON, s ext.JSON) error {
	var props ext.JSONSchemaProps
	if err := Decode(s.Raw, &props); err != nil {
		return err
	}
	var internal internalext.JSONSchemaProps
	if err := ext.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&props, &internal, nil); err != nil {
		return err
	}
	validator, _, err := validation.NewSchemaValidator(&internal)
	if err != nil {
		return err
	}
	var value any
	if err := Decode(raw.Raw, &value); err != nil {
		return err
	}
	errs := validation.ValidateCustomResource(field.NewPath("config"), value, validator)
	if len(errs) != 0 {
		return fmt.Errorf("E_CONFIG_SCHEMA: %s", errs.ToAggregate())
	}
	return nil
}

func Compile(t api.NetworkTopology, plugins map[string]api.NetworkPlugin) (api.RevisionSpec, error) {
	r := api.RevisionSpec{SourceUID: string(t.UID), CompilerVersion: CompilerVersion, Topology: t.Spec}
	r.Topology.Steps = slices.Clone(t.Spec.Steps)
	r.Topology.EffectBudget.Allow = slices.Clone(t.Spec.EffectBudget.Allow)
	if len(t.Spec.Steps) < 1 || len(t.Spec.Steps) > 32 {
		return r, fmt.Errorf("E_STEPS: require 1–32 steps")
	}
	steps := map[string]api.Step{}
	configs := map[string]Config{}
	locked := map[string]api.LockedPlugin{}
	names := map[string]string{}
	rootCount := 0
	for _, step := range t.Spec.Steps {
		if !stepID.MatchString(step.ID) {
			return r, fmt.Errorf("E_STEP_ID: %s", step.ID)
		}
		if _, exists := steps[step.ID]; exists {
			return r, fmt.Errorf("E_DUPLICATE_ID: %s", step.ID)
		}
		steps[step.ID] = step
		p, exists := plugins[step.PluginRef.Name]
		if !exists {
			return r, fmt.Errorf("E_PLUGIN_REFERENCE: %s", step.PluginRef.Name)
		}
		if err := ValidatePlugin(p); err != nil {
			return r, err
		}
		if step.Kind != p.Spec.Kind {
			return r, fmt.Errorf("E_STEP_KIND: %s", step.ID)
		}
		if err := validateConfig(step.Config, p.Spec.ConfigSchema); err != nil {
			return r, fmt.Errorf("steps.%s: %w", step.ID, err)
		}
		c, err := ConfigForPlugin(step, p.Spec)
		if p.Spec.Implementation.Type == EffectProgramImplementation && p.Spec.Kind == OperationKind {
			inputs := map[string]EffectRef{}
			for port := range p.Spec.Inputs {
				inputs[port] = EffectRef{From: "effects.compiler-" + port + ".interface"}
			}
			bound, bindErr := BindEffectHook(step, p.Spec, inputs)
			if bindErr != nil {
				return r, bindErr
			}
			for _, effect := range bound.Effects {
				if effect.Kind == CreateVLAN {
					c.VlanID = effect.Parameters.VlanID
				}
			}
		}
		if err != nil {
			return r, err
		}
		if p.Spec.Implementation.Name == AcceleratedBridgePlugin {
			if err := ValidateBridgeConfig(c); err != nil {
				return r, err
			}
		}
		if p.Spec.Implementation.Name == OVSPlugin {
			if err := ValidateOVSConfig(c); err != nil {
				return r, err
			}
		}
		if c.VF != nil {
			if err := ValidateVFSettings(*c.VF); err != nil {
				return r, err
			}
		}
		if c.Mode == KernelMode && c.Driver == VFIODriver {
			return r, fmt.Errorf("E_DRIVER_CONTRACT: kernel mode cannot bind vfio-pci")
		}
		if p.Spec.Outputs[InterfacePort].Type == InterfaceType || c.Mode == RDMAMode && c.RDMAProfile != IBVerbsSinglePort {
			if !ValidIfName(c.IfName) {
				return r, fmt.Errorf("E_INTERFACE_NAME: %s", c.IfName)
			}
			if other, exists := names[c.IfName]; exists {
				return r, fmt.Errorf("E_NAME_COLLISION: %s and %s", step.ID, other)
			}
			names[c.IfName] = step.ID
		}
		if p.Spec.AllocationDriver == IPAMDriver {
			if err := ValidateIPAMConfig(c); err != nil {
				return r, err
			}
		}
		configs[step.ID] = c
		if step.Kind == AllocationKind {
			rootCount++
			if len(step.Inputs) != 0 {
				return r, fmt.Errorf("E_ROOT_INPUT: %s", step.ID)
			}
		}
		for _, effect := range p.Spec.AllowedEffects {
			if !slices.Contains(t.Spec.EffectBudget.Allow, effect) {
				return r, fmt.Errorf("E_EFFECT_DENIED: %s requires %s", step.ID, effect)
			}
		}
		locked[p.Name] = api.LockedPlugin{Name: p.Name, Digest: Digest(p.Spec), Spec: p.Spec}
	}
	for _, c := range configs {
		if c.Mode == RDMAMode {
			if rootCount != 1 {
				return r, fmt.Errorf("E_RDMA_TOPOLOGY: this whole-HCA profile requires a single allocation root")
			}
			for _, step := range steps {
				name := plugins[step.PluginRef.Name].Spec.Implementation.Name
				if step.Kind == OperationKind && (!c.Representor || name != AcceleratedBridgePlugin && name != OVSPlugin) {
					return r, fmt.Errorf("E_RDMA_TOPOLOGY: only certified host representor operations may accompany this whole-HCA root")
				}
			}
		}
	}
	if rootCount < 1 || rootCount > 8 {
		return r, fmt.Errorf("E_ROOT_COUNT: require 1–8 allocation roots")
	}
	state := map[string]int{}
	masters := map[string]string{}
	vlanParents := map[string]string{}
	leaseUsers := map[string]string{}
	tags := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 2 {
			return nil
		}
		if state[id] == 1 {
			return fmt.Errorf("E_GRAPH_CYCLE: %s", id)
		}
		state[id] = 1
		step := steps[id]
		p := plugins[step.PluginRef.Name]
		if len(step.Inputs) != len(p.Spec.Inputs) {
			return fmt.Errorf("E_PORT_CARDINALITY: %s", id)
		}
		for _, portName := range slices.Sorted(maps.Keys(p.Spec.Inputs)) {
			port := p.Spec.Inputs[portName]
			raw, exists := step.Inputs[portName]
			if !exists {
				return fmt.Errorf("E_PORT_REFERENCE: %s.%s", id, portName)
			}
			refs, err := References(raw)
			if err != nil {
				return err
			}
			if len(refs) < port.MinItems || len(refs) > port.MaxItems {
				return fmt.Errorf("E_PORT_CARDINALITY: %s.%s", id, portName)
			}
			for _, ref := range refs {
				from, output, ok := strings.Cut(ref.From, ".")
				producer, exists := steps[from]
				if !ok || !exists {
					return fmt.Errorf("E_PORT_REFERENCE: %s", ref.From)
				}
				produced, exists := plugins[producer.PluginRef.Name].Spec.Outputs[output]
				if !exists || produced.Type != port.Type || produced.LinkType != port.LinkType {
					return fmt.Errorf("E_PORT_TYPE: %s", ref.From)
				}
				if err := visit(from); err != nil {
					return err
				}
				if port.Type == AddressLeaseType {
					if leaseUsers[ref.From] != "" {
						return fmt.Errorf("E_LEASE_DUPLICATE: %s", ref.From)
					}
					leaseUsers[ref.From] = id
				}
				if port.Access == AccessExclusiveMaster {
					if port.Type == RepresentorType && producer.Kind != AllocationKind {
						return fmt.Errorf("E_MULTIPLE_MASTERS: representor master chains are not permitted")
					}
					if produced.Ownership == OwnershipCreated && producer.PluginRef.Name != BondPlugin && producer.PluginRef.Name != VLANPlugin {
						return fmt.Errorf("E_PORT_ACCESS: shared children are not certified bond members")
					}
					if masters[ref.From] != "" {
						return fmt.Errorf("E_MULTIPLE_MASTERS: %s", from)
					}
					masters[ref.From] = id
					if port.Type == RepresentorType {
						continue
					}
					if configs[from].MTU != configs[id].MTU {
						return fmt.Errorf("E_MTU: bond/member mismatch")
					}
					if !configs[from].Up {
						return fmt.Errorf("E_LINK_STATE: bond members must be configured up")
					}
				} else if port.Access == AccessCreateChild {
					if configs[from].VlanID != 0 {
						return fmt.Errorf("E_VLAN_STACKING: %s", id)
					}
					if configs[id].MTU > configs[from].MTU {
						return fmt.Errorf("E_MTU: child exceeds parent")
					}
					key := fmt.Sprintf("%s:%d", from, configs[id].VlanID)
					if tags[key] {
						return fmt.Errorf("E_VLAN_DUPLICATE: %s", key)
					}
					tags[key] = true
					vlanParents[id] = from
				}
			}
		}
		state[id] = 2
		r.Order = append(r.Order, id)
		return nil
	}
	// Sorting identifiers makes equivalent presentation order compile identically.
	for _, id := range slices.Sorted(maps.Keys(steps)) {
		if err := visit(id); err != nil {
			return r, err
		}
	}
	for _, step := range steps {
		if plugins[step.PluginRef.Name].Spec.AllocationDriver == IPAMDriver && leaseUsers[step.ID+"."+LeasePort] == "" {
			return r, fmt.Errorf("E_IPAM_UNUSED_ROOT: %s", step.ID)
		}
	}
	for child, parent := range vlanParents {
		if masters[parent+"."+InterfacePort] != "" {
			return r, fmt.Errorf("E_PORT_ACCESS: %s uses enslaved parent %s", child, parent)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(locked)) {
		r.Plugins = append(r.Plugins, locked[name])
	}
	slices.SortFunc(r.Topology.Steps, func(a, b api.Step) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(r.Topology.EffectBudget.Allow)
	if _, err := PlanPrepare(r); err != nil {
		return r, err
	}
	if _, err := PlanConnect(r); err != nil {
		return r, err
	}
	r.Digest = Digest(r)
	data, _ := json.Marshal(r)
	if len(data) > 256*1024 {
		return r, fmt.Errorf("E_SIZE: compiled revision exceeds 256 KiB")
	}
	return r, nil
}

// Cleanup uses the identity of an already admitted saved revision. It must not
// invoke prepare/connect planners again to decide how to restore its journal.
func verifyRevisionIdentity(r api.RevisionSpec) error {
	digest := r.Digest
	r.Digest = ""
	if digest != Digest(r) {
		return fmt.Errorf("E_REVISION_DIGEST: mismatch")
	}
	if r.CompilerVersion != CompilerVersion {
		return fmt.Errorf("E_COMPILER_CONTRACT: unsupported version")
	}
	for _, p := range r.Plugins {
		if p.Digest != Digest(p.Spec) {
			return fmt.Errorf("E_PLUGIN_DIGEST: %s", p.Name)
		}
	}
	return nil
}

func VerifyRevision(r api.RevisionSpec) error {
	if err := verifyRevisionIdentity(r); err != nil {
		return err
	}
	plugins := map[string]api.NetworkPlugin{}
	for _, p := range r.Plugins {
		plugins[p.Name] = api.NetworkPlugin{ObjectMeta: metav1.ObjectMeta{Name: p.Name}, Spec: p.Spec}
	}
	rebuilt, err := Compile(api.NetworkTopology{ObjectMeta: metav1.ObjectMeta{UID: k8stypes.UID(r.SourceUID)}, Spec: r.Topology}, plugins)
	if err != nil {
		return err
	}
	if rebuilt.Digest != r.Digest {
		return fmt.Errorf("E_REVISION_CONTRACT: compilation differs")
	}
	return nil
}

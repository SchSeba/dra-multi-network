// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/pluginapi"
)

const (
	HostPlugin        = "host-cni"
	SRIOVPlugin       = "sriov-cni"
	VFIOMode          = "vfio"
	VFIODriver        = "vfio-pci"
	PCIFunctionType   = "PCIFunction/v1"
	VFIOType          = "VFIODevice/v1"
	DriverBind        = "linux/driver-bind/v1"
	DeviceAccess      = "runtime/device-access/v1"
	VFIOPort          = "vfio"
	DevicePort        = "device"
	VFConfigure       = "linux/vf-configure/v1"
	PCIReset          = "linux/pci-reset/v1"
	FLR               = "flr"
	ObserveOrRetain   = "ObserveOrRetain"
	RDMAType          = "RDMADevice/v1"
	RDMANamespace     = "linux/rdma-namespace/v1"
	RoCESinglePort    = "roce-single-port-v1"
	IBVerbsSinglePort = "ib-verbs-single-port-v1"
	IPoIBSinglePort   = "ib-ipoib-single-port-v1"
	InfiniBand        = "infiniband"
	RDMAMode          = "rdma"
	RDMAPort          = "rdma"
	RDMAProfileField  = "rdmaProfile"
)

func IBProfile(profile string) bool {
	return profile == IBVerbsSinglePort || profile == IPoIBSinglePort
}

//nolint:goconst // JSON Schema and descriptor vocabulary remains literal.
func ibProfiles() []api.NetworkPlugin {
	var result []api.NetworkPlugin
	for _, handler := range []string{HostPlugin, SRIOVPlugin} {
		for _, profile := range []string{IBVerbsSinglePort, IPoIBSinglePort} {
			name := handler + "-ib-verbs"
			props := map[string]any{"mode": enum(RDMAMode), "linkType": enum(InfiniBand), "rdmaAccess": map[string]any{"type": SchemaBoolean, "enum": []bool{true}}, RDMAProfileField: enum(profile), "accessBackend": enum("nri"), "madAccess": map[string]any{"type": SchemaBoolean}}
			required := []string{"mode", "linkType", "rdmaAccess", RDMAProfileField, "accessBackend"}
			outputs := map[string]api.Port{RDMAPort: {Type: RDMAType, LinkType: InfiniBand, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}}
			if profile == IPoIBSinglePort {
				name = handler + "-ipoib"
				props[IfNameField] = map[string]any{"type": SchemaString, "pattern": `^[A-Za-z0-9_.-]{1,15}$`}
				props[MTUField], props[UpField] = integer(256, 65520), map[string]any{"type": SchemaBoolean}
				required = append(required, IfNameField, MTUField, UpField)
				outputs[InterfacePort] = api.Port{Type: InterfaceType, LinkType: InfiniBand, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}
			}
			result = append(result, api.NetworkPlugin{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "NetworkPlugin"}, ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: api.PluginSpec{Implementation: api.Implementation{Type: BuiltinImplementation, Name: handler, Version: profile}, Kind: AllocationKind, StateContract: api.StateContract{Version: ContractVersion, Cleanup: RecordedEffects}, ConfigSchema: schema(props, required), Outputs: outputs, AllowedEffects: []string{RDMANamespace, DeviceAccess}}})
		}
	}
	return result
}

func RecoveryStrategy(kind string) string {
	if kind == PCIReset {
		return ObserveOrRetain
	}
	return ObserveAndReconcile
}

func resetProfiles() []api.NetworkPlugin {
	profiles := kernelBindingProfiles()
	for i := range profiles {
		p := &profiles[i]
		p.Name = strings.TrimSuffix(p.Name, "-rebind") + "-reset"
		p.Spec.Implementation.Version = "kernel-reset-v1"
		var props map[string]any
		_ = json.Unmarshal(p.Spec.ConfigSchema.Raw, &props)
		props["properties"].(map[string]any)["resetMethod"] = enum(FLR)
		props["required"] = append(props["required"].([]any), "resetMethod")
		p.Spec.ConfigSchema.Raw, _ = json.Marshal(props)
		p.Spec.AllowedEffects = append([]string{PCIReset}, p.Spec.AllowedEffects...)
	}
	return profiles
}

type VFSettings = pluginapi.VFSettings

func ValidateVFSettings(v VFSettings) error {
	if len(v.Fields()) == 0 {
		return fmt.Errorf("E_VF_CONFIG: at least one setting required")
	}
	if v.MAC != "" {
		mac, err := net.ParseMAC(v.MAC)
		if err != nil || len(mac) != 6 || mac[0]&1 != 0 || mac.String() == "00:00:00:00:00:00" {
			return fmt.Errorf("E_VF_MAC: require a nonzero unicast Ethernet MAC")
		}
	}
	if v.LinkState != "" && !slices.Contains([]string{"auto", "enable", "disable"}, v.LinkState) {
		return fmt.Errorf("E_VF_LINK_STATE")
	}
	if v.VlanID != nil && (*v.VlanID < 0 || *v.VlanID > 4094) || v.VlanQoS != nil && (*v.VlanQoS < 0 || *v.VlanQoS > 7) {
		return fmt.Errorf("E_VF_VLAN")
	}
	if v.VlanProtocol != "" && !slices.Contains([]string{"802.1Q", "802.1ad"}, v.VlanProtocol) {
		return fmt.Errorf("E_VF_VLAN_PROTOCOL")
	}
	if v.VlanID == nil && (v.VlanQoS != nil || v.VlanProtocol != "") {
		return fmt.Errorf("E_VF_VLAN: QoS/protocol require vlanId")
	}
	for _, rate := range []*uint32{v.MinTxRate, v.MaxTxRate} {
		if rate != nil && *rate > 1000000 {
			return fmt.Errorf("E_VF_RATE")
		}
	}
	if v.MinTxRate != nil && v.MaxTxRate != nil && *v.MaxTxRate != 0 && *v.MinTxRate > *v.MaxTxRate {
		return fmt.Errorf("E_VF_RATE: minimum exceeds maximum")
	}
	return nil
}

//nolint:goconst // JSON Schema vocabulary remains literal.
func vfSettingsSchema() map[string]any {
	return map[string]any{"type": SchemaObject, "additionalProperties": false, "minProperties": 1, "properties": map[string]any{
		"mac":   map[string]any{"type": SchemaString, "pattern": `^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$`},
		"trust": map[string]any{"type": SchemaBoolean}, "spoofCheck": map[string]any{"type": SchemaBoolean},
		"linkState": enum("auto", "enable", "disable"), "vlanId": integer(0, 4094), "vlanQoS": integer(0, 7), "vlanProtocol": enum("802.1Q", "802.1ad"),
		"minTxRate": integer(0, 1000000), "maxTxRate": integer(0, 1000000),
	}}
}

func configuredVFProfiles(bases []api.NetworkPlugin) []api.NetworkPlugin {
	var profiles []api.NetworkPlugin
	// Derive canonical variants without changing already locked descriptors.
	for _, base := range bases {
		if base.Name != SRIOVPlugin && base.Name != SRIOVPlugin+"-vfio" {
			continue
		}
		p := *base.DeepCopy()
		p.Name += "-configured"
		p.Spec.Implementation.Version = "vf-config-v1"
		var props map[string]any
		_ = json.Unmarshal(p.Spec.ConfigSchema.Raw, &props)
		props["properties"].(map[string]any)["vf"] = vfSettingsSchema()
		props["required"] = append(props["required"].([]any), "vf")
		p.Spec.ConfigSchema.Raw, _ = json.Marshal(props)
		p.Spec.AllowedEffects = append([]string{VFConfigure}, p.Spec.AllowedEffects...)
		profiles = append(profiles, p)
	}
	return profiles
}

// Separate canonical descriptors keep already locked kernel contracts intact.
// Both profiles execute the existing host/sriov internal allocation handlers.
//
//nolint:goconst // JSON Schema and descriptor vocabulary remains literal.
func hardwareProfiles() []api.NetworkPlugin {
	var result []api.NetworkPlugin
	for _, handler := range []string{HostPlugin, SRIOVPlugin} {
		result = append(result, api.NetworkPlugin{
			TypeMeta:   metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "NetworkPlugin"},
			ObjectMeta: metav1.ObjectMeta{Name: handler + "-vfio"},
			Spec: api.PluginSpec{
				Implementation: api.Implementation{Type: BuiltinImplementation, Name: handler, Version: "vfio-v1"},
				Kind:           AllocationKind, StateContract: api.StateContract{Version: ContractVersion, Cleanup: RecordedEffects},
				ConfigSchema:   schema(map[string]any{"mode": enum(VFIOMode), "linkType": enum(Ethernet), "driver": enum(VFIODriver), "accessBackend": enum("cdi")}, []string{"mode", "linkType", "driver", "accessBackend"}),
				Outputs:        map[string]api.Port{VFIOPort: {Type: VFIOType, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}},
				AllowedEffects: []string{DriverBind, DeviceAccess},
			},
		})
	}
	return result
}

// Approved kernel rebinding uses the same PCI journal as VFIO. Keep the old
// descriptors unchanged so existing immutable revisions still verify.
//
//nolint:goconst // JSON Schema and descriptor vocabulary remains literal.
func kernelBindingProfiles() []api.NetworkPlugin {
	var profiles []api.NetworkPlugin
	for _, handler := range []string{HostPlugin, SRIOVPlugin} {
		profiles = append(profiles, api.NetworkPlugin{
			TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "NetworkPlugin"}, ObjectMeta: metav1.ObjectMeta{Name: handler + "-rebind"},
			Spec: api.PluginSpec{
				Implementation: api.Implementation{Type: BuiltinImplementation, Name: handler, Version: "kernel-bind-v1"},
				Kind:           AllocationKind, StateContract: api.StateContract{Version: ContractVersion, Cleanup: RecordedEffects},
				ConfigSchema:   schema(map[string]any{"mode": enum(KernelMode), "linkType": enum(Ethernet), "rdmaAccess": map[string]any{"type": SchemaBoolean, "enum": []bool{false}}, "driver": map[string]any{"type": SchemaString, "pattern": `^[A-Za-z0-9_-]{1,64}$`}, IfNameField: map[string]any{"type": SchemaString, "pattern": `^[A-Za-z0-9_.-]{1,15}$`}, MTUField: integer(576, 9000), UpField: map[string]any{"type": SchemaBoolean}}, []string{"mode", "linkType", "rdmaAccess", "driver", IfNameField, MTUField, UpField}),
				Outputs:        map[string]api.Port{InterfacePort: {Type: InterfaceType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}},
				AllowedEffects: []string{DriverBind, Move, Configure},
			},
		})
	}
	return profiles
}

// RoCE owns the complete one-function HCA. Its RDMA output cannot be consumed
// as a bond member or VLAN parent; the runner keeps the associated link private.
func roceProfiles() []api.NetworkPlugin {
	var result []api.NetworkPlugin
	for _, handler := range []string{HostPlugin, SRIOVPlugin} {
		result = append(result, api.NetworkPlugin{
			TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "NetworkPlugin"}, ObjectMeta: metav1.ObjectMeta{Name: handler + "-roce"},
			Spec: api.PluginSpec{
				Implementation: api.Implementation{Type: BuiltinImplementation, Name: handler, Version: RoCESinglePort}, Kind: AllocationKind,
				StateContract:  api.StateContract{Version: ContractVersion, Cleanup: RecordedEffects},
				ConfigSchema:   schema(map[string]any{"mode": enum(RDMAMode), "linkType": enum(Ethernet), "rdmaAccess": map[string]any{"type": SchemaBoolean, "enum": []bool{true}}, RDMAProfileField: enum(RoCESinglePort), "accessBackend": enum("nri"), IfNameField: map[string]any{"type": SchemaString, "pattern": `^[A-Za-z0-9_.-]{1,15}$`}, MTUField: integer(576, 9000), UpField: map[string]any{"type": SchemaBoolean}}, []string{"mode", "linkType", "rdmaAccess", RDMAProfileField, "accessBackend", IfNameField, MTUField, UpField}),
				Outputs:        map[string]api.Port{RDMAPort: {Type: RDMAType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}},
				AllowedEffects: []string{RDMANamespace, DeviceAccess},
			},
		})
	}
	return result
}

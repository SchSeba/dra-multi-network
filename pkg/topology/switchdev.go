// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	sdk "github.com/SchSeba/dra-multi-network/pkg/pluginapi"
)

var ovsUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const (
	RepresentorType         = "HostRepresentor/v1"
	RepresentorPort         = sdk.RepresentorPort
	RepresentorMap          = sdk.RepresentorMap
	BridgeConfigure         = sdk.BridgeConfigure
	AcceleratedBridgePlugin = "accelerated-bridge-cni"
	OVSPlugin               = "ovs-cni"
	OVSConfigure            = sdk.OVSConfigure
	UplinkConfigure         = sdk.UplinkConfigure
)

// Separate profiles preserve the existing allocation and persisted contracts.
// A representor stays in the host namespace even when its endpoint is moved.
//
//nolint:goconst // JSON Schema and descriptor vocabulary remains literal.
func switchdevProfiles(base []api.NetworkPlugin) []api.NetworkPlugin {
	var result []api.NetworkPlugin
	for _, p := range base {
		if p.Name != SRIOVPlugin && p.Name != SRIOVPlugin+"-vfio" && p.Name != SRIOVPlugin+"-roce" {
			continue
		}
		p = *p.DeepCopy()
		p.Name += "-switchdev"
		if p.Spec.Outputs[RDMAPort].Type != RDMAType {
			p.Spec.Implementation.Version = "switchdev-v1"
		}
		var props map[string]any
		_ = json.Unmarshal(p.Spec.ConfigSchema.Raw, &props)
		props["properties"].(map[string]any)["representor"] = map[string]any{"type": SchemaBoolean, "enum": []bool{true}}
		props["required"] = append(props["required"].([]any), "representor")
		p.Spec.ConfigSchema.Raw, _ = json.Marshal(props)
		p.Spec.Outputs[RepresentorPort] = api.Port{Type: RepresentorType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}
		p.Spec.AllowedEffects = append([]string{RepresentorMap}, p.Spec.AllowedEffects...)
		result = append(result, p)
	}
	props := map[string]any{"bridge": map[string]any{"type": SchemaString, "pattern": `^[A-Za-z0-9_.-]{1,15}$`}, MTUField: integer(576, 9000), UpField: map[string]any{"type": SchemaBoolean}, "vlanId": integer(0, 4094), "trunk": map[string]any{"type": "array", "maxItems": 4094, "uniqueItems": true, "items": integer(1, 4094)}}
	result = append(result, api.NetworkPlugin{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "NetworkPlugin"}, ObjectMeta: metav1.ObjectMeta{Name: AcceleratedBridgePlugin}, Spec: api.PluginSpec{
		Implementation: api.Implementation{Type: BuiltinImplementation, Name: AcceleratedBridgePlugin, Version: ContractVersion}, Kind: OperationKind, StateContract: api.StateContract{Version: ContractVersion, Cleanup: RecordedEffects}, ConfigSchema: schema(props, []string{"bridge", MTUField, UpField, "vlanId"}),
		Inputs: map[string]api.Port{RepresentorPort: {Type: RepresentorType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Access: AccessExclusiveMaster}}, Outputs: map[string]api.Port{RepresentorPort: {Type: RepresentorType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}}, AllowedEffects: []string{BridgeConfigure},
	}})
	ovs := *result[len(result)-1].DeepCopy()
	ovs.Name, ovs.Spec.Implementation.Name = OVSPlugin, OVSPlugin
	ovs.Spec.AllowedEffects = []string{OVSConfigure}
	result = append(result, ovs)
	uplink := *result[len(result)-2].DeepCopy()
	uplink.Name = AcceleratedBridgePlugin + "-uplink"
	var uplinkSchema map[string]any
	_ = json.Unmarshal(uplink.Spec.ConfigSchema.Raw, &uplinkSchema)
	uplinkSchema["properties"].(map[string]any)["uplink"] = map[string]any{"type": SchemaString, "pattern": `^[A-Za-z0-9_.-]{1,15}$`}
	uplinkSchema["required"] = append(uplinkSchema["required"].([]any), "uplink")
	uplink.Spec.ConfigSchema.Raw, _ = json.Marshal(uplinkSchema)
	uplink.Spec.AllowedEffects = append([]string{UplinkConfigure}, uplink.Spec.AllowedEffects...)
	result = append(result, uplink)
	return result
}

func ValidateOVSConfig(c Config) error {
	if err := ValidateBridgeConfig(c); err != nil {
		return err
	}
	if c.VlanID == 0 && len(c.Trunk) == 0 {
		return fmt.Errorf("E_OVS_VLAN_REQUIRED: unrestricted trunks are forbidden")
	}
	if c.Uplink != "" {
		return fmt.Errorf("E_OVS_UPLINK_PROFILE")
	}
	return nil
}

func ValidateOVSBridgeGrants(grants []api.OVSBridgeGrant) error {
	seen := map[string]bool{}
	for _, grant := range grants {
		if err := ValidateHostBridgeGrants([]api.HostBridgeGrant{{Name: grant.Name, Alias: grant.Identity, VLANs: grant.VLANs}}); err != nil {
			return err
		}
		if !ovsUUID.MatchString(grant.UUID) || seen[grant.Name] || seen[grant.UUID] || seen[grant.Identity] {
			return fmt.Errorf("E_OVS_BRIDGE_GRANT")
		}
		seen[grant.Name], seen[grant.UUID], seen[grant.Identity] = true, true, true
	}
	return nil
}

func ValidateBridgeConfig(c Config) error {
	if !ValidIfName(c.Bridge) || c.MTU < 576 || c.MTU > 9000 || c.VlanID < 0 || c.VlanID > 4094 || len(c.Trunk) > 4094 {
		return fmt.Errorf("E_REPRESENTOR_BRIDGE_CONFIG")
	}
	if c.Uplink != "" && !ValidIfName(c.Uplink) {
		return fmt.Errorf("E_HOST_UPLINK_CONFIG")
	}
	seen := map[int]bool{}
	for _, id := range c.Trunk {
		if id < 1 || id > 4094 || seen[id] || id == c.VlanID {
			return fmt.Errorf("E_REPRESENTOR_VLAN_CONFIG")
		}
		seen[id] = true
	}
	return nil
}

func ValidateHostBridgeGrants(grants []api.HostBridgeGrant) error {
	seen := map[string]bool{}
	for _, grant := range grants {
		if !ValidIfName(grant.Name) || !linkLabel.MatchString(grant.Alias) || grant.Alias == "" || strings.HasPrefix(grant.Alias, "dra-mn:") || seen[grant.Name] || slices.Contains([]string{"cni0", "flannel.1", "ovn-k8s-mp0", "br-ex"}, grant.Name) || len(grant.VLANs) > 4094 {
			return fmt.Errorf("E_HOST_BRIDGE_GRANT")
		}
		seen[grant.Name] = true
		vlans := map[int]bool{}
		for _, id := range grant.VLANs {
			if id < 1 || id > 4094 || vlans[id] {
				return fmt.Errorf("E_HOST_BRIDGE_VLAN_GRANT")
			}
			vlans[id] = true
		}
		uplinks := map[string]bool{}
		for _, uplink := range grant.Uplinks {
			if !ValidIfName(uplink.Name) || uplink.Name == grant.Name || !linkLabel.MatchString(uplink.Alias) || uplink.Alias == "" || strings.HasPrefix(uplink.Alias, "dra-mn:") || uplinks[uplink.Name] || !managedUplinkPCI.MatchString(uplink.PCIAddress) {
				return fmt.Errorf("E_HOST_UPLINK_GRANT")
			}
			uplinks[uplink.Name] = true
			vids := map[int]bool{}
			for _, vid := range uplink.VLANs {
				if vids[vid] || !vlans[vid] {
					return fmt.Errorf("E_HOST_UPLINK_VLAN_GRANT")
				}
				vids[vid] = true
			}
		}
	}
	return nil
}

var managedUplinkPCI = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

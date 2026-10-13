// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
// Package pluginapi is the transport-neutral plugin contract. It deliberately
// imports no Kubernetes, DRA, netlink or journal packages. Allocation and port
// references are opaque authority issued by the framework, never PCI targets.
package pluginapi

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

const APIVersion = "effects.dra-multi-network.schseba.github.io/v1alpha1"
const CallAPIVersion = "plugins.dra-multi-network.schseba.github.io/v1alpha1"
const (
	NodePrepare      = "NodePrepare"
	ConnectNetwork   = "ConnectNetwork"
	RemoveNetwork    = "RemoveNetwork"
	NodeUnprepare    = "NodeUnprepare"
	InterfacePort    = "interface"
	DevicePort       = "device"
	VFIOPort         = "vfio"
	RDMAPort         = "rdma"
	RepresentorPort  = "representor"
	RepresentorMap   = "linux/representor-map/v1"
	BridgeConfigure  = "linux/representor-bridge/v1"
	OVSConfigure     = "ovs/representor-port/v1"
	EswitchConfigure = "linux/eswitch-configure/v1"
	UplinkConfigure  = "linux/shared-uplink-vlan/v1"
	Move             = "linux/link-move/v1"
	Configure        = "linux/link-configure/v1"
	IPv4Forwarding   = "linux/ipv4-forwarding/v1"
	CreateBond       = "linux/create-bond/v1"
	AttachMaster     = "linux/attach-master/v1"
	CreateVLAN       = "linux/create-vlan/v1"
	CreateMacvlan    = "linux/create-macvlan/v1"
	CreateIPvlan     = "linux/create-ipvlan/v1"
	DriverBind       = "linux/driver-bind/v1"
	DeviceAccess     = "runtime/device-access/v1"
	VFConfigure      = "linux/vf-configure/v1"
	PCIReset         = "linux/pci-reset/v1"
	RDMANamespace    = "linux/rdma-namespace/v1"
)

// Request carries one validated plugin invocation, without claims, selectors,
// classes, clientsets, namespace paths or access to the framework's journal.
// Config is normalized under the locked descriptor's schema before invocation.
type Request struct {
	APIVersion        string                 `json:"apiVersion"`
	ID                string                 `json:"id"`
	Plugin            string                 `json:"plugin"`
	Version           string                 `json:"version"`
	Hook              string                 `json:"hook"`
	Config            Config                 `json:"config"`
	Allocation        EffectRef              `json:"allocation,omitempty"`
	Inputs            map[string][]EffectRef `json:"inputs,omitempty"`
	Prepared          map[string]EffectRef   `json:"prepared,omitempty"`
	RecordedExecution string                 `json:"recordedExecution,omitempty"`
}

// Cleanup references the framework-owned original execution. Plugins never
// supply fresh deletion targets. GA gRPC uses these same request/result values.
type Result struct {
	Program             EffectProgram `json:"program"`
	RestoreRecordedHook string        `json:"restoreRecordedHook,omitempty"`
}

type Config struct {
	IPv4Forward       *bool       `json:"ipv4Forward,omitempty"`
	Network           string      `json:"network,omitempty"`
	Subnet            string      `json:"subnet,omitempty"`
	IfName            string      `json:"ifName,omitempty"`
	MTU               int         `json:"mtu,omitempty"`
	Up                bool        `json:"up,omitempty"`
	Mode              string      `json:"mode,omitempty"`
	LinkType          string      `json:"linkType,omitempty"`
	RDMAAccess        bool        `json:"rdmaAccess,omitempty"`
	RDMAProfile       string      `json:"rdmaProfile,omitempty"`
	MADAccess         bool        `json:"madAccess,omitempty"`
	Driver            string      `json:"driver,omitempty"`
	AccessBackend     string      `json:"accessBackend,omitempty"`
	VlanID            int         `json:"vlanId,omitempty"`
	MonitorIntervalMs int         `json:"monitorIntervalMs,omitempty"`
	FailOverMac       string      `json:"failOverMac,omitempty"`
	VF                *VFSettings `json:"vf,omitempty"`
	ResetMethod       string      `json:"resetMethod,omitempty"`
	MAC               string      `json:"mac,omitempty"`
	Alias             *string     `json:"alias,omitempty"`
	Representor       bool        `json:"representor,omitempty"`
	Bridge            string      `json:"bridge,omitempty"`
	Trunk             []int       `json:"trunk,omitempty"`
	Uplink            string      `json:"uplink,omitempty"`
}
type VFSettings struct {
	MAC          string  `json:"mac,omitempty"`
	Trust        *bool   `json:"trust,omitempty"`
	SpoofCheck   *bool   `json:"spoofCheck,omitempty"`
	LinkState    string  `json:"linkState,omitempty"`
	VlanID       *int    `json:"vlanId,omitempty"`
	VlanQoS      *int    `json:"vlanQoS,omitempty"`
	VlanProtocol string  `json:"vlanProtocol,omitempty"`
	MinTxRate    *uint32 `json:"minTxRate,omitempty"`
	MaxTxRate    *uint32 `json:"maxTxRate,omitempty"`
}

func (v VFSettings) Fields() []string {
	data, _ := json.Marshal(v)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	return slices.Sorted(maps.Keys(fields))
}

type EffectRef struct {
	From string `json:"from"`
}
type EffectPredicate struct {
	Kind string `json:"kind"`
}
type EffectRecovery struct {
	Strategy string `json:"strategy"`
}
type NetworkEffect struct {
	ID             string               `json:"id"`
	Kind           string               `json:"kind"`
	Inputs         map[string]EffectRef `json:"inputs,omitempty"`
	Parameters     Config               `json:"parameters"`
	Requires       []EffectPredicate    `json:"requires"`
	Postconditions []EffectPredicate    `json:"postconditions"`
	Recovery       EffectRecovery       `json:"recovery"`
}
type EffectProgram struct {
	APIVersion string               `json:"apiVersion"`
	Effects    []NetworkEffect      `json:"effects"`
	Exports    map[string]EffectRef `json:"exports"`
}

// ValidateResult is a framework check, applied equally to Go and future RPC
// responses. A plugin cannot reference another step's allocation or effects.
// Descriptor/schema, effect budget and live ownership checks remain mandatory.
func ValidateResult(call Request, result Result) error {
	if call.APIVersion != CallAPIVersion {
		return fmt.Errorf("E_PLUGIN_CALL_VERSION")
	}
	if call.Hook == RemoveNetwork || call.Hook == NodeUnprepare {
		original := ConnectNetwork
		if call.Hook == NodeUnprepare {
			original = NodePrepare
		}
		if call.RecordedExecution == "" || result.RestoreRecordedHook != original || len(result.Program.Effects) != 0 || len(result.Program.Exports) != 0 {
			return fmt.Errorf("E_PLUGIN_CLEANUP_AUTHORITY")
		}
		return nil
	}
	if call.Hook != NodePrepare && call.Hook != ConnectNetwork || result.RestoreRecordedHook != "" || result.Program.APIVersion != APIVersion {
		return fmt.Errorf("E_PLUGIN_HOOK_CONTRACT")
	}
	allowed := map[string]bool{}
	if call.Allocation.From != "" {
		allowed[call.Allocation.From] = true
	}
	for _, refs := range call.Inputs {
		for _, ref := range refs {
			allowed[ref.From] = true
		}
	}
	for _, ref := range call.Prepared {
		allowed[ref.From] = true
	}
	seen := map[string]bool{}
	valid := func(ref EffectRef) bool {
		if ref.From == "" {
			return false
		}
		if allowed[ref.From] {
			return true
		}
		parts := strings.Split(ref.From, ".")
		return len(parts) == 3 && parts[0] == "effects" && seen[parts[1]]
	}
	for _, effect := range result.Program.Effects {
		if !strings.HasPrefix(effect.ID, call.ID+"-") || seen[effect.ID] {
			return fmt.Errorf("E_PLUGIN_EFFECT_SCOPE")
		}
		for _, ref := range effect.Inputs {
			if !valid(ref) {
				return fmt.Errorf("E_PLUGIN_INPUT_AUTHORITY: %s", ref.From)
			}
		}
		seen[effect.ID] = true
	}
	for _, ref := range result.Program.Exports {
		if !valid(ref) {
			return fmt.Errorf("E_PLUGIN_OUTPUT_AUTHORITY: %s", ref.From)
		}
	}
	return nil
}

// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
// Package networkplugins contains in-process MVP plugins. Each invocation is
// a pure request/result exchange; the package has no DRA or Kubernetes imports,
// device discovery, host mutation or journal access. GA replaces the invocation
// transport with gRPC without moving allocation authority into plugins.
package networkplugins

import (
	"fmt"

	sdk "github.com/SchSeba/dra-multi-network/pkg/pluginapi"
)

const (
	ovsPlugin     = "ovs-cni"
	vfioMode      = "vfio"
	rdmaMode      = "rdma"
	hostPlugin    = "host-cni"
	sriovPlugin   = "sriov-cni"
	ipvlanPlugin  = "ipvlan-cni"
	macvlanPlugin = "macvlan-cni"
)

func Run(call sdk.Request) (sdk.Result, error) {
	result := sdk.Result{Program: sdk.EffectProgram{APIVersion: sdk.APIVersion, Exports: map[string]sdk.EffectRef{}}}
	if call.APIVersion != sdk.CallAPIVersion || call.ID == "" || call.Version == "" {
		return result, fmt.Errorf("E_PLUGIN_CALL_IDENTITY")
	}
	switch call.Plugin {
	case hostPlugin, sriovPlugin, macvlanPlugin, ipvlanPlugin, "bond-cni", "vlan-cni", "tuning-cni", "accelerated-bridge-cni", ovsPlugin, "ipam", "address-cni":
	default:
		return result, fmt.Errorf("E_PLUGIN_IMPLEMENTATION: %s", call.Plugin)
	}
	if call.Hook == sdk.RemoveNetwork || call.Hook == sdk.NodeUnprepare {
		result.RestoreRecordedHook = sdk.ConnectNetwork
		if call.Hook == sdk.NodeUnprepare {
			result.RestoreRecordedHook = sdk.NodePrepare
		}
		return result, sdk.ValidateResult(call, result)
	}
	c, p := call.Config, &result.Program
	if call.Plugin == "ipam" {
		if call.Hook == sdk.ConnectNetwork {
			p.Exports["lease"] = call.Allocation
		}
		return result, sdk.ValidateResult(call, result)
	}
	add := func(id, kind string, c sdk.Config, inputs map[string]sdk.EffectRef) {
		strategy := "ObserveAndReconcile"
		if kind == sdk.PCIReset {
			strategy = "ObserveOrRetain"
		}
		p.Effects = append(p.Effects, sdk.NetworkEffect{ID: id, Kind: kind, Parameters: c, Inputs: inputs, Requires: []sdk.EffectPredicate{{Kind: "allocation-and-owned-inputs"}}, Postconditions: []sdk.EffectPredicate{{Kind: "native-state-match"}}, Recovery: sdk.EffectRecovery{Strategy: strategy}})
	}
	if call.Hook == sdk.NodePrepare {
		device := call.Allocation
		if c.ResetMethod != "" {
			id := call.ID + "-reset"
			add(id, sdk.PCIReset, sdk.Config{ResetMethod: c.ResetMethod}, map[string]sdk.EffectRef{sdk.DevicePort: device})
			device = sdk.EffectRef{From: "effects." + id + ".device"}
		}
		if c.VF != nil {
			id := call.ID + "-vf-configure"
			add(id, sdk.VFConfigure, sdk.Config{VF: c.VF}, map[string]sdk.EffectRef{sdk.DevicePort: device})
			if c.Mode == vfioMode {
				device = sdk.EffectRef{From: "effects." + id + ".device"}
			}
		}
		if c.Driver != "" {
			bind, access := call.ID+"-bind", call.ID+"-access"
			add(bind, sdk.DriverBind, sdk.Config{Driver: c.Driver}, map[string]sdk.EffectRef{sdk.DevicePort: device})
			if c.Mode == vfioMode {
				add(access, sdk.DeviceAccess, sdk.Config{Mode: c.Mode, AccessBackend: c.AccessBackend}, map[string]sdk.EffectRef{sdk.DevicePort: {From: "effects." + bind + ".device"}})
				p.Exports[sdk.VFIOPort] = sdk.EffectRef{From: "effects." + access + ".device"}
			}
		}
		return result, sdk.ValidateResult(call, result)
	}
	if call.Hook != sdk.ConnectNetwork {
		return result, fmt.Errorf("E_PLUGIN_HOOK_CONTRACT")
	}
	if call.Plugin == "tuning-cni" {
		if len(call.Inputs[sdk.InterfacePort]) != 1 {
			return result, fmt.Errorf("E_PLUGIN_PORT_CARDINALITY")
		}
		if c.IPv4Forward == nil {
			return result, fmt.Errorf("E_TUNING_CONFIG: ipv4Forward is required")
		}
		id := call.ID + "-forwarding"
		add(id, sdk.IPv4Forwarding, sdk.Config{IPv4Forward: c.IPv4Forward}, map[string]sdk.EffectRef{sdk.InterfacePort: call.Inputs[sdk.InterfacePort][0]})
		p.Exports["configured"] = sdk.EffectRef{From: "effects." + id + ".interface"}
		return result, sdk.ValidateResult(call, result)
	}
	if call.Plugin == "address-cni" {
		if len(call.Inputs["interface"]) != 1 || len(call.Inputs["lease"]) != 1 {
			return result, fmt.Errorf("E_PLUGIN_PORT_CARDINALITY")
		}
		id := call.ID + "-address"
		add(id, "linux/address-configure/v1", sdk.Config{}, map[string]sdk.EffectRef{"interface": call.Inputs["interface"][0], "lease": call.Inputs["lease"][0]})
		p.Exports["configured"] = sdk.EffectRef{From: "effects." + id + ".interface"}
		return result, sdk.ValidateResult(call, result)
	}
	if call.Plugin == "accelerated-bridge-cni" || call.Plugin == ovsPlugin {
		ports := call.Inputs[sdk.RepresentorPort]
		if len(ports) != 1 {
			return result, fmt.Errorf("E_PLUGIN_PORT_CARDINALITY")
		}
		ref := ports[0]
		id := call.ID + "-bridge"
		kind := sdk.BridgeConfigure
		if call.Plugin == ovsPlugin {
			kind = sdk.OVSConfigure
		}
		if c.Uplink != "" {
			if call.Plugin == ovsPlugin {
				return result, fmt.Errorf("E_OVS_UPLINK_PROFILE")
			}
			uplink := call.ID + "-uplink"
			add(uplink, sdk.UplinkConfigure, c, map[string]sdk.EffectRef{sdk.RepresentorPort: ref})
			ref = sdk.EffectRef{From: "effects." + uplink + ".representor"}
			c.Uplink = ""
		}
		add(id, kind, c, map[string]sdk.EffectRef{sdk.RepresentorPort: ref})
		p.Exports[sdk.RepresentorPort] = sdk.EffectRef{From: "effects." + id + ".representor"}
		return result, sdk.ValidateResult(call, result)
	}
	if c.Representor {
		id := call.ID + "-representor"
		add(id, sdk.RepresentorMap, sdk.Config{}, map[string]sdk.EffectRef{sdk.DevicePort: call.Allocation})
		p.Exports[sdk.RepresentorPort] = sdk.EffectRef{From: "effects." + id + ".representor"}
	}
	if c.Mode == vfioMode {
		p.Exports[sdk.VFIOPort] = call.Prepared[sdk.VFIOPort]
		return result, sdk.ValidateResult(call, result)
	}
	if c.Mode == rdmaMode {
		output, access := call.ID+"-rdma", call.ID+"-access"
		add(output, sdk.RDMANamespace, sdk.Config{IfName: c.IfName, MTU: c.MTU, Up: c.Up, RDMAProfile: c.RDMAProfile, MADAccess: c.MADAccess}, map[string]sdk.EffectRef{sdk.DevicePort: call.Allocation})
		add(access, sdk.DeviceAccess, sdk.Config{Mode: c.Mode, AccessBackend: c.AccessBackend}, map[string]sdk.EffectRef{sdk.DevicePort: {From: "effects." + output + ".device"}})
		p.Exports[sdk.RDMAPort] = sdk.EffectRef{From: "effects." + access + ".device"}
		if c.RDMAProfile == "ib-ipoib-single-port-v1" {
			p.Exports[sdk.InterfacePort] = sdk.EffectRef{From: "effects." + output + ".interface"}
		}
		return result, sdk.ValidateResult(call, result)
	}
	output := call.ID + "-create"
	switch call.Plugin {
	case hostPlugin, sriovPlugin:
		add(output, sdk.Move, sdk.Config{IfName: c.IfName}, map[string]sdk.EffectRef{sdk.InterfacePort: call.Allocation})
	case macvlanPlugin, ipvlanPlugin:
		kind := sdk.CreateMacvlan
		if call.Plugin == ipvlanPlugin {
			kind = sdk.CreateIPvlan
		}
		add(output, kind, c, map[string]sdk.EffectRef{"parent": call.Allocation})
	case "bond-cni":
		add(output, sdk.CreateBond, c, nil)
		for i, ref := range call.Inputs["members"] {
			add(fmt.Sprintf("%s-member-%d", call.ID, i), sdk.AttachMaster, sdk.Config{}, map[string]sdk.EffectRef{sdk.InterfacePort: ref, "master": {From: "effects." + output + ".interface"}})
		}
	case "vlan-cni":
		parents := call.Inputs["parent"]
		if len(parents) != 1 {
			return result, fmt.Errorf("E_PLUGIN_PORT_CARDINALITY")
		}
		add(output, sdk.CreateVLAN, c, map[string]sdk.EffectRef{"parent": parents[0]})
	}
	configure := call.ID + "-configure"
	add(configure, sdk.Configure, sdk.Config{IfName: c.IfName, MTU: c.MTU, Up: c.Up}, map[string]sdk.EffectRef{sdk.InterfacePort: {From: "effects." + output + ".interface"}})
	p.Exports[sdk.InterfacePort] = sdk.EffectRef{From: "effects." + configure + ".interface"}
	return result, sdk.ValidateResult(call, result)
}

// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/utils/ptr"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/consts"
	"github.com/SchSeba/dra-multi-network/pkg/discovery"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/host"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type Inventory struct {
	PCI         string                       `json:"pci"`
	Parent      string                       `json:"parent"`
	Kind        string                       `json:"kind"`
	IfName      string                       `json:"ifName"`
	Driver      string                       `json:"driver"`
	Vendor      string                       `json:"vendor"`
	DeviceID    string                       `json:"deviceID"`
	LinkType    string                       `json:"linkType"`
	VFID        int                          `json:"vfID"`
	Protected   bool                         `json:"protected"`
	RDMA        []effects.RDMADevice         `json:"rdma,omitempty"`
	Representor *effects.RepresentorIdentity `json:"representor,omitempty"`
	EswitchMode string                       `json:"eswitchMode,omitempty"`
}
type Persona struct {
	Device    resourceapi.Device       `json:"device"`
	Inventory Inventory                `json:"inventory"`
	Policy    api.DeviceExposurePolicy `json:"policy"`
	Plugin    *api.PluginSpec          `json:"plugin,omitempty"`
}

func Discover() (map[string]Inventory, error) {
	// Reuse the copied driver's PCI/SR-IOV discovery and host provider.
	devices, err := discovery.DiscoverSriovDevices()
	if err != nil {
		return nil, err
	}
	physical, err := discovery.DiscoverPhysicalPciDevices()
	if err != nil {
		return nil, err
	}
	maps.Copy(devices, physical)
	protected, err := protectedPCI()
	if err != nil {
		return nil, err
	}
	inventory := map[string]Inventory{}
	for _, d := range devices {
		get := func(key string) string {
			a := d.Attributes[resourceapi.QualifiedName(key)]
			if a.StringValue != nil {
				return *a.StringValue
			}
			return ""
		}
		pci := get(consts.AttributePciAddress)
		kind := "PCI"
		parent := pci
		vfID := -1
		if host.GetHelpers().IsSriovVF(pci) {
			kind = VFKind
			parent = get(consts.AttributePfPciAddress)
			vf := d.Attributes[consts.AttributeVFID]
			if vf.IntValue != nil {
				vfID = int(*vf.IntValue)
			}
		} else if host.GetHelpers().IsSriovPF(pci) {
			kind = PFKind
		}
		if parent == "" {
			return nil, fmt.Errorf("E_INVENTORY_PARENT: %s", pci)
		}
		driver, err := filepath.EvalSymlinks(filepath.Join("/sys/bus/pci/devices", pci, "driver"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		inventory[pci] = Inventory{PCI: pci, Parent: parent, Kind: kind, IfName: host.GetHelpers().TryGetInterfaceName(pci), Driver: filepath.Base(driver), Vendor: get(consts.AttributeVendorID), DeviceID: get(consts.AttributeDeviceID), LinkType: get(consts.AttributeLinkType), VFID: vfID, Protected: protected[pci] || protected[parent]}
	}
	rdma, err := effects.DiscoverRDMA()
	if err != nil {
		return nil, err
	}
	joinRDMAInventory(inventory, rdma)
	ports, err := effects.HostDevlinkPorts()
	if err != nil {
		return nil, err
	}
	for pci, d := range inventory {
		if d.Kind != VFKind {
			continue
		}
		d.Representor, err = effects.DiscoverVFRepresentor(ports, d.PCI, d.Parent, d.VFID)
		if err != nil {
			return nil, err
		}
		if d.Representor != nil {
			d.EswitchMode, err = effects.HostEswitchMode(d.Representor.Bus, d.Representor.Device)
			if err != nil {
				return nil, err
			}
		}
		inventory[pci] = d
	}
	return inventory, nil
}

func joinRDMAInventory(inventory map[string]Inventory, rdma []effects.RDMADevice) {
	for _, device := range rdma {
		if d, exists := inventory[device.PCI]; exists {
			d.RDMA = append(d.RDMA, device)
			if len(device.Ports) == 1 && device.Ports[0].LinkLayer == topology.InfiniBand {
				d.LinkType = topology.InfiniBand
			}
			inventory[device.PCI] = d
		}
	}
	protectRDMAInventory(inventory, rdma)
}

func protectRDMAInventory(inventory map[string]Inventory, rdma []effects.RDMADevice) {
	// Protect all observed owners of a management HCA, including mappings to a
	// different PCI function. GUID equality alone is not proof of shared scope.
	for changed := true; changed; {
		changed = false
		for _, device := range rdma {
			protected := false
			for _, pci := range device.RelatedPCI {
				d := inventory[pci]
				protected = protected || d.Protected || inventory[d.Parent].Protected
			}
			if !protected {
				continue
			}
			for pci, d := range inventory {
				if !d.Protected && (slices.Contains(device.RelatedPCI, pci) || slices.Contains(device.RelatedPCI, d.Parent)) {
					d.Protected = true
					inventory[pci] = d
					changed = true
				}
			}
		}
	}
}
func protectedPCI() (map[string]bool, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	routes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	protected := protectedLinks(links, routes)
	ports, err := effects.HostDevlinkPorts()
	if err != nil {
		return nil, err
	}
	result, err := protectedDevlinkPCI("/sys", ports, links, routes)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		if !protected[link.Attrs().Index] {
			continue
		}
		pci, err := effects.PCIOwner("/sys", filepath.Join("/sys/class/net", link.Attrs().Name, "device"))
		if err == nil {
			if pci == "" {
				continue
			}
			result[pci] = true
			if parent, err := filepath.EvalSymlinks(filepath.Join("/sys/bus/pci/devices", pci, "physfn")); err == nil {
				result[filepath.Base(parent)] = true
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return result, nil
}

func protectedDevlinkPCI(sysRoot string, ports []*netlink.DevlinkPort, links []netlink.Link, routes []netlink.Route) (map[string]bool, error) {
	protected := protectedLinks(links, routes)
	result := map[string]bool{}
	for _, link := range links {
		if !protected[link.Attrs().Index] {
			continue
		}
		owners, err := effects.DevlinkPortOwners(sysRoot, ports, link.Attrs().Name, link.Attrs().Index)
		if err != nil {
			return nil, err
		}
		for _, pci := range owners {
			result[pci] = true
		}
	}
	return result, nil
}

func protectedLinks(links []netlink.Link, routes []netlink.Route) map[int]bool {
	return effects.ProtectedLinks(links, routes)
}

func ValidatePolicy(p api.DeviceExposurePolicy, plugins map[string]api.NetworkPlugin) error {
	if p.Spec.Action == ManageEswitch {
		return validateEswitchPolicy(p)
	}
	if p.Spec.Management != nil {
		return fmt.Errorf("E_ESWITCH_POLICY_ACTION")
	}
	if p.Spec.Action == "Exclude" {
		return nil
	}
	e := p.Spec.Exposure
	if e == nil {
		return fmt.Errorf("E_POLICY_EXPOSURE")
	}
	plugin, exists := plugins[e.Plugin]
	if !exists {
		return fmt.Errorf("E_POLICY_PERSONA: unknown plugin %s", e.Plugin)
	}
	if err := topology.ValidatePlugin(plugin); err != nil {
		return err
	}
	profile, err := topology.AllocationProfile(plugin.Spec)
	if err != nil {
		return err
	}
	modes := map[string]string{"host-cni": topology.KernelMode, "sriov-cni": topology.KernelMode, "macvlan-cni": "bridge", "ipvlan-cni": "l2", "host-cni-vfio": topology.VFIOMode, "sriov-cni-vfio": topology.VFIOMode, "sriov-cni-configured": topology.KernelMode, "sriov-cni-vfio-configured": topology.VFIOMode, "host-cni-rebind": topology.KernelMode, "sriov-cni-rebind": topology.KernelMode, "host-cni-reset": topology.KernelMode, "sriov-cni-reset": topology.KernelMode, "host-cni-roce": topology.RDMAMode, "sriov-cni-roce": topology.RDMAMode}
	for _, handler := range []string{topology.HostPlugin, topology.SRIOVPlugin} {
		modes[handler+"-ib-verbs"], modes[handler+"-ipoib"] = topology.RDMAMode, topology.RDMAMode
	}
	modes[topology.SRIOVPlugin+"-switchdev"] = topology.KernelMode
	modes[topology.SRIOVPlugin+"-vfio-switchdev"] = topology.VFIOMode
	modes[topology.SRIOVPlugin+"-roce-switchdev"] = topology.RDMAMode
	mode, exists := modes[profile.Name]
	if !exists || e.Mode != mode {
		return fmt.Errorf("E_POLICY_PERSONA: unsupported plugin/mode")
	}
	switchdev := profile.Spec.Outputs[topology.RepresentorPort].Type == topology.RepresentorType
	if switchdev && p.Spec.Selector.Kind != VFKind || !switchdev && len(e.AllowedHostBridges) > 0 || len(e.AllowedHostBridges) > 0 && !slices.Contains(e.EffectPermissions, topology.BridgeConfigure) {
		return fmt.Errorf("E_REPRESENTOR_POLICY_GRANT")
	}
	if err := topology.ValidateHostBridgeGrants(e.AllowedHostBridges); err != nil {
		return err
	}
	for _, bridge := range e.AllowedHostBridges {
		if len(bridge.Uplinks) > 0 && !slices.Contains(e.EffectPermissions, topology.UplinkConfigure) {
			return fmt.Errorf("E_HOST_UPLINK_EFFECT_GRANT")
		}
	}
	if len(e.AllowedOVSBridges) > 0 && (!switchdev || !slices.Contains(e.EffectPermissions, topology.OVSConfigure)) {
		return fmt.Errorf("E_OVS_POLICY_GRANT")
	}
	if err := topology.ValidateOVSBridgeGrants(e.AllowedOVSBridges); err != nil {
		return err
	}
	roce := e.Mode == topology.RDMAMode
	if roce != (len(e.AllowedRDMAProfiles) > 0) || roce && (len(e.AllowedRDMAProfiles) != 1 || string(e.AllowedRDMAProfiles[0]) != profile.Spec.Implementation.Version || len(e.AllowedDrivers) == 0) || e.AllowedMADAccess && (!roce || !topology.IBProfile(profile.Spec.Implementation.Version)) {
		return fmt.Errorf("E_RDMA_PROFILE_GRANT: matching whole-HCA profile and driver allowlist required; MAD is an explicit IB grant")
	}
	configured := strings.HasSuffix(profile.Name, "-configured")
	if configured && p.Spec.Selector.Kind != VFKind {
		return fmt.Errorf("E_POLICY_DEVICE_KIND: configured profile requires VFs")
	}
	if configured != (len(e.AllowedVFSettings) > 0) {
		return fmt.Errorf("E_VF_CONFIG_GRANT: configured VF profiles require allowedVFSettings")
	}
	fields := map[api.VFSettingField]bool{}
	for _, field := range e.AllowedVFSettings {
		if fields[field] || !slices.Contains([]string{"mac", "trust", "spoofCheck", "linkState", "vlanId", "vlanQoS", "vlanProtocol", "minTxRate", "maxTxRate"}, string(field)) {
			return fmt.Errorf("E_VF_CONFIG_GRANT: invalid or duplicate field %s", field)
		}
		fields[field] = true
	}
	if e.Persona == "" || len(e.Persona) > 16 {
		return fmt.Errorf("E_POLICY_PERSONA: require a short stable identity")
	}
	if e.Mode == topology.VFIOMode && !slices.Contains(e.AllowedDrivers, topology.VFIODriver) {
		return fmt.Errorf("E_DRIVER_GRANT: VFIO policy must allow vfio-pci")
	}
	if (strings.HasSuffix(profile.Name, "-rebind") || strings.HasSuffix(profile.Name, "-reset")) && len(e.AllowedDrivers) == 0 {
		return fmt.Errorf("E_DRIVER_GRANT: kernel binding requires allowedDrivers")
	}
	reset := strings.HasSuffix(profile.Name, "-reset")
	if reset != (len(e.AllowedResetMethods) > 0) || reset && (len(e.AllowedResetMethods) != 1 || string(e.AllowedResetMethods[0]) != topology.FLR) {
		return fmt.Errorf("E_RESET_GRANT: reset profile requires exactly the flr method")
	}
	shared := profile.Name == "macvlan-cni" || profile.Name == "ipvlan-cni"
	if shared != e.AllowMultipleAllocations {
		return fmt.Errorf("E_POLICY_SHARING")
	}
	if shared {
		c, exists := e.Capacity["children"]
		n, err := strconv.Atoi(c.Value)
		if !exists || err != nil || n < 1 || n > 4096 || c.Default != "1" || len(c.ValidValues) != 1 || c.ValidValues[0] != "1" {
			return fmt.Errorf("E_POLICY_CAPACITY: children requires 1–4096 with default/valid value 1")
		}
	} else if len(e.Capacity) != 0 {
		return fmt.Errorf("E_POLICY_CAPACITY: exclusive persona has no children capacity")
	}
	// Compatibility is derived from physical inventory, never weakened by policy.
	if len(e.Compatibility) != 0 {
		return fmt.Errorf("E_POLICY_COMPATIBILITY: this profile derives interface/family domains automatically")
	}
	for _, effect := range plugin.Spec.AllowedEffects {
		if !slices.Contains(e.EffectPermissions, effect) {
			return fmt.Errorf("E_EFFECT_DENIED: policy requires %s", effect)
		}
	}
	return nil
}
func Matches(p api.DeviceExposurePolicy, d Inventory, node corev1.Node) (bool, error) {
	if p.Spec.NodeSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.NodeSelector)
		if err != nil {
			return false, err
		}
		if !selector.Matches(labels.Set(node.Labels)) {
			return false, nil
		}
	}
	s := p.Spec.Selector
	if s.Kind != d.Kind && !(s.Kind == "PCI" && d.Kind != VFKind) {
		return false, nil
	}
	if len(s.PCIAddresses) > 0 && !slices.Contains(s.PCIAddresses, d.PCI) {
		return false, nil
	}
	if len(s.ParentPCIAddresses) > 0 && !slices.Contains(s.ParentPCIAddresses, d.Parent) {
		return false, nil
	}
	return (s.Vendor == "" || s.Vendor == d.Vendor) && (s.Driver == "" || s.Driver == d.Driver) && (s.LinkType == "" || s.LinkType == d.LinkType), nil
}
func Personas(inventory map[string]Inventory, policies []api.DeviceExposurePolicy, node corev1.Node, plugins map[string]api.NetworkPlugin) (map[string]Persona, error) {
	result := map[string]Persona{}
	var rdma []effects.RDMADevice
	for _, d := range inventory {
		rdma = append(rdma, d.RDMA...)
	}
	slices.SortFunc(policies, func(a, b api.DeviceExposurePolicy) int {
		if a.Spec.Priority > b.Spec.Priority {
			return -1
		}
		if a.Spec.Priority < b.Spec.Priority {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, d := range inventory {
		if d.Protected || d.LinkType != topology.Ethernet && d.LinkType != "eth" && d.LinkType != topology.InfiniBand {
			continue
		}
		taken := map[string]bool{}
		for _, p := range policies {
			if err := ValidatePolicy(p, plugins); err != nil {
				return nil, fmt.Errorf("policy %s: %w", p.Name, err)
			}
			if p.Spec.Action == ManageEswitch {
				continue
			}
			match, err := Matches(p, d, node)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
			if p.Spec.Action == "Exclude" {
				break
			}
			e := p.Spec.Exposure
			// Ethernet handoff/bind/reset profiles do not own an HCA. Withhold
			// all observed owners, including peers without direct RDMA metadata.
			// Shared children retain the host parent and grant no RDMA access.
			if e.Mode != topology.RDMAMode && !e.AllowMultipleAllocations && effects.RDMAFunction(d.PCI, rdma) {
				continue
			}
			plugin := plugins[e.Plugin]
			profile, err := topology.AllocationProfile(plugin.Spec)
			if err != nil {
				return nil, err
			}
			if profile.Spec.Outputs[topology.RepresentorPort].Type == topology.RepresentorType && (d.Representor == nil || d.EswitchMode != "switchdev") {
				continue
			}
			if d.LinkType == topology.InfiniBand && (e.Mode != topology.RDMAMode || !topology.IBProfile(profile.Spec.Implementation.Version)) {
				continue
			}
			if e.Mode == topology.RDMAMode {
				h := roceRootHandle(Root{Persona: Persona{Inventory: d, Policy: p}})
				if !slices.Contains(e.AllowedDrivers, d.Driver) || effects.RDMAAllocationScope(h, rdma, profile.Spec.Implementation.Version) != nil {
					continue
				}
			}
			if taken[e.Plugin] {
				continue
			}
			taken[e.Plugin] = true
			if strings.HasPrefix(profile.Name, "sriov-cni") && d.Kind != VFKind || strings.HasPrefix(profile.Name, "host-cni") && d.Kind == VFKind {
				return nil, fmt.Errorf("E_POLICY_DEVICE_KIND: %s", p.Name)
			}
			if e.Mode == topology.VFIOMode || strings.HasSuffix(profile.Name, "-rebind") || strings.HasSuffix(profile.Name, "-reset") {
				if e.Mode == topology.VFIOMode || d.Driver == topology.VFIODriver {
					if _, err := effects.SingleFunctionIOMMU(d.PCI); err != nil {
						continue
					}
				}
				if !slices.Contains(e.AllowedDrivers, d.Driver) {
					continue
				}
			}
			if strings.HasSuffix(profile.Name, "-reset") {
				if _, err := effects.ResetScope(d.PCI, topology.FLR); err != nil {
					continue
				}
			}
			name := strings.NewReplacer(":", "-", ".", "-").Replace(d.PCI) + "-" + e.Persona
			attrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}
			linkType := topology.Ethernet
			if d.LinkType == topology.InfiniBand {
				linkType = topology.InfiniBand
			}
			for key, value := range map[string]string{"plugin": e.Plugin, "persona": e.Persona, "mode": e.Mode, "linkType": linkType, "pciAddress": d.PCI, "pfPciAddress": d.Parent, "vendor": d.Vendor, "deviceID": d.DeviceID, "interfaceName": d.IfName} {
				attrs[resourceapi.QualifiedName(api.Driver+"/"+key)] = resourceapi.DeviceAttribute{StringValue: ptr.To(value)}
			}
			attrs[resourceapi.QualifiedName(api.Driver+"/allocationReady")] = resourceapi.DeviceAttribute{BoolValue: ptr.To(true)}
			if len(d.RDMA) > 0 {
				attrs[resourceapi.QualifiedName(api.Driver+"/rdmaCapable")] = resourceapi.DeviceAttribute{BoolValue: ptr.To(true)}
			}
			device := resourceapi.Device{Name: name, Attributes: attrs}
			interfaceGroup := "kernel-handoff"
			if e.Mode == topology.RDMAMode {
				interfaceGroup = "rdma-handoff"
			}
			if e.Mode == topology.VFIOMode {
				interfaceGroup = "vfio-handoff"
			}
			if strings.HasSuffix(e.Plugin, "-configured") || strings.HasSuffix(e.Plugin, "-rebind") || strings.HasSuffix(e.Plugin, "-reset") {
				interfaceGroup = e.Plugin
			}
			if profile.Spec.Outputs[topology.RepresentorPort].Type == topology.RepresentorType {
				interfaceGroup = e.Plugin
			}
			familyGroup := "host-retained"
			if d.Kind != VFKind {
				familyGroup = "pf-handoff"
			}
			if e.AllowMultipleAllocations {
				interfaceGroup = strings.TrimSuffix(e.Plugin, "-cni")
				familyGroup = "host-retained"
				device.AllowMultipleAllocations = ptr.To(true)
				device.Capacity = map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{resourceapi.QualifiedName(api.Driver + "/children"): {Value: resource.MustParse(e.Capacity["children"].Value), RequestPolicy: &resourceapi.CapacityRequestPolicy{Default: ptr.To(resource.MustParse("1")), ValidValues: []resource.Quantity{resource.MustParse("1")}}}}
			}
			var locked *api.PluginSpec
			if plugin.Spec.Implementation.Type == topology.EffectProgramImplementation {
				locked = plugin.Spec.DeepCopy()
				attrs[resourceapi.QualifiedName(api.Driver+"/pluginContractDigest")] = resourceapi.DeviceAttribute{StringValue: ptr.To(topology.Digest(plugin.Spec))}
				// Different programmable allocation contracts cannot duplicate a
				// shared parent's capacity or bypass its physical ownership domain.
				interfaceGroup = "p-" + topology.Digest([]string{plugin.Name, topology.Digest(plugin.Spec)})[:32]
			}
			for _, domain := range []struct{ name, group string }{{"i-" + topology.Digest(d.PCI)[:16], interfaceGroup}, {"f-" + topology.Digest(d.Parent)[:16], familyGroup}} {
				device.ConsumesCounters = append(device.ConsumesCounters, resourceapi.DeviceCounterConsumption{CounterSet: domain.name, Counters: map[string]resourceapi.Counter{"members": {Value: resource.MustParse("1")}}, CompatibilityGroups: []string{domain.group}})
			}
			result[name] = Persona{Device: device, Inventory: d, Policy: p, Plugin: locked}
		}
	}
	return result, nil
}
func Pool(personas map[string]Persona) resourceslice.Pool {
	counters := map[string]int{}
	devices := make([]resourceapi.Device, 0, len(personas))
	for _, name := range slices.Sorted(maps.Keys(personas)) {
		p := personas[name]
		devices = append(devices, p.Device)
		for _, c := range p.Device.ConsumesCounters {
			counters[c.CounterSet]++
		}
	}
	pool := resourceslice.Pool{}
	var sets []resourceapi.CounterSet
	for _, name := range slices.Sorted(maps.Keys(counters)) {
		sets = append(sets, resourceapi.CounterSet{Name: name, Counters: map[string]resourceapi.Counter{"members": {Value: resource.MustParse(strconv.Itoa(counters[name]))}}})
	}
	// API limits are 8 counter sets in a counter-bearing slice, 128 devices in a
	// device-bearing slice, and 64 pools/slices as enforced by the SDK publisher.
	for len(sets) > 0 {
		n := min(8, len(sets))
		pool.Slices = append(pool.Slices, resourceslice.Slice{SharedCounters: sets[:n]})
		sets = sets[n:]
	}
	for len(devices) > 0 {
		n := min(128, len(devices))
		pool.Slices = append(pool.Slices, resourceslice.Slice{Devices: devices[:n]})
		devices = devices[n:]
	}
	if len(pool.Slices) == 0 {
		pool.Slices = []resourceslice.Slice{{}}
	}
	return pool
}
func (a *Agent) publish(ctx context.Context) error {
	var pluginList api.NetworkPluginList
	if err := a.client.List(ctx, &pluginList); err != nil {
		return err
	}
	var policies api.DeviceExposurePolicyList
	if err := a.client.List(ctx, &policies); err != nil {
		return err
	}
	active, orphanRoots, err := a.activeClaims(ctx)
	if err != nil {
		return err
	}
	if _, err := a.reconcileEswitch(ctx, policies.Items, active); err != nil {
		return err
	}
	// Keep the saved physical inventory while allocations or cleanup records
	// exist: their netdevices may be in sandboxes or rebound to another driver.
	// A complete drain permits rediscovery after administrator maintenance.
	if len(active) == 0 {
		inventory, err := Discover()
		if err != nil {
			return err
		}
		if topology.Digest(inventory) != topology.Digest(a.inventory) {
			if err := a.store.Put("inventory", inventory); err != nil {
				return err
			}
			a.inventory = inventory
		}
	}
	desired, err := Personas(a.inventory, policies.Items, a.node, topology.PluginRegistry(pluginList.Items))
	if err != nil {
		return err
	}
	desired = retainedPublication(desired, a.personas, len(active) > 0, orphanRoots)
	for name, p := range desired {
		if a.eswitchBlocked[p.Inventory.PCI] || a.eswitchBlocked[p.Inventory.Parent] {
			p.Device = *p.Device.DeepCopy()
			p.Device.Attributes[resourceapi.QualifiedName(api.Driver+"/allocationReady")] = resourceapi.DeviceAttribute{BoolValue: ptr.To(false)}
			desired[name] = p
		}
	}
	if err := a.store.Put("publication", desired); err != nil {
		return err
	}
	if err := a.helper.PublishResources(ctx, resourceslice.DriverResources{Pools: map[string]resourceslice.Pool{a.node.Name: Pool(desired)}}); err != nil {
		return err
	}
	a.personas = desired
	return nil
}

// Readiness is a scheduling hint, not part of the retained policy/accounting
// contract. Comparing it would latch a temporary drain after cleanup completes.
func publicationContract(personas map[string]Persona) string {
	copy := maps.Clone(personas)
	for name, p := range copy {
		p.Device = *p.Device.DeepCopy()
		delete(p.Device.Attributes, resourceapi.QualifiedName(api.Driver+"/allocationReady"))
		copy[name] = p
	}
	return topology.Digest(copy)
}
func retainedPublication(desired, previous map[string]Persona, active bool, orphanRoots []Root) map[string]Persona {
	drain := active && publicationContract(desired) != publicationContract(previous)
	if drain {
		desired = maps.Clone(previous)
	}
	for name, p := range desired {
		blocked := drain
		for _, root := range orphanRoots {
			// ponytail: withhold the shared parent while orphan shares exist;
			// account individual orphan capacity if partial availability matters.
			blocked = blocked || p.Inventory.PCI == root.Persona.Inventory.PCI || conflicts(p, root.Persona)
		}
		p.Device = *p.Device.DeepCopy()
		p.Device.Attributes[resourceapi.QualifiedName(api.Driver+"/allocationReady")] = resourceapi.DeviceAttribute{BoolValue: ptr.To(!blocked)}
		desired[name] = p
	}
	return desired
}
func (a *Agent) activeClaims(ctx context.Context) (map[string]bool, []Root, error) {
	var list resourceapi.ResourceClaimList
	if err := a.client.List(ctx, &list); err != nil {
		return nil, nil, err
	}
	active := map[string]bool{}
	fingerprints := map[string]string{}
	var orphanRoots []Root
	for _, claim := range list.Items {
		if claim.Status.Allocation == nil {
			continue
		}
		for _, r := range claim.Status.Allocation.Devices.Results {
			if r.Driver == api.Driver && r.Pool == a.node.Name {
				active[string(claim.UID)] = true
				fingerprints[string(claim.UID)] = allocationFingerprint(&claim)
			}
		}
	}
	records, err := a.store.List("prepare/")
	if err != nil {
		return nil, nil, err
	}
	for _, raw := range records {
		var p Preparation
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, nil, err
		}
		if p.State != Released {
			active[string(p.Claim.UID)] = true
			if fingerprints[string(p.Claim.UID)] != p.Fingerprint {
				orphanRoots = append(orphanRoots, p.Roots...)
			}
		}
	}
	return active, orphanRoots, nil
}

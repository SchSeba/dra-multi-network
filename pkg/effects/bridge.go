// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
// Bridge port/VLAN semantics follow accelerated-bridge-cni pkg/manager at
// 8035095fefd1be945f805992902873e7aeeae704. Ownership and compensation use
// the framework journal; neither a bridge name nor CNI cache is authority.
package effects

import (
	"fmt"
	"math"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type PortVLAN struct {
	ID    uint16 `json:"id"`
	Flags uint16 `json:"flags"`
}

type BridgePortState struct {
	Before      LinkState  `json:"before"`
	BeforeVLANs []PortVLAN `json:"beforeVLANs"`
	TargetVLANs []PortVLAN `json:"targetVLANs"`
	Bridge      LinkState  `json:"bridge"`
	BridgeIndex int        `json:"bridgeIndex"`
	DefaultPVID uint16     `json:"defaultPVID"`
	Restoring   bool       `json:"restoring,omitempty"`
}

func (n *Native) mapRepresentor(root Handle) (Handle, error) {
	if root.Shared || root.Scope != HostScope || root.Representor == nil || !slices.Contains([]string{topology.InterfaceType, topology.VFIOType, topology.RDMAType}, root.Type) {
		return Handle{}, fmt.Errorf("E_REPRESENTOR_ALLOCATION_GRANT")
	}
	h := Handle{Type: topology.RepresentorType, Scope: HostScope, Name: root.Representor.Name, IfIndex: int(root.Representor.IfIndex), PCIAddress: root.PCIAddress, ParentPCI: root.ParentPCI, VFID: root.VFID, Representor: root.Representor, AllowedHostBridges: root.AllowedHostBridges, AllowedOVSBridges: root.AllowedOVSBridges}
	if _, err := n.representor(h); err != nil {
		return Handle{}, err
	}
	return h, nil
}

func (n *Native) representor(h Handle) (netlink.Link, error) {
	r := h.Representor
	if h.Type != topology.RepresentorType || h.Scope != HostScope || h.Shared || r == nil || r.VFPCI != h.PCIAddress || r.PFPCI != h.ParentPCI || int(r.VFNumber) != h.VFID || h.Name != r.Name || h.IfIndex != int(r.IfIndex) {
		return nil, fmt.Errorf("E_REPRESENTOR_ALLOCATION_GRANT")
	}
	g, err := netlink.NewHandleAt(n.hostNS, unix.NETLINK_GENERIC)
	if err != nil {
		return nil, err
	}
	defer g.Close()
	ports, err := g.DevLinkGetAllPortList()
	if err != nil {
		return nil, err
	}
	actual, err := RepresentorForVF("/sys", ports, h.PCIAddress, h.ParentPCI, h.VFID)
	if err != nil {
		return nil, err
	}
	if actual == nil || *actual != *r {
		return nil, fmt.Errorf("E_REPRESENTOR_MAPPING_CHANGED")
	}
	dev, err := g.DevLinkGetDeviceByName(r.Bus, r.Device)
	if err != nil {
		return nil, err
	}
	if dev.Attrs.Eswitch.Mode != switchdevMode {
		return nil, fmt.Errorf("E_REPRESENTOR_ESWITCH_MODE")
	}
	l, err := n.host.LinkByIndex(h.IfIndex)
	if err != nil {
		return nil, err
	}
	if l.Attrs().Name != h.Name {
		return nil, fmt.Errorf("E_REPRESENTOR_INCARNATION")
	}
	return l, nil
}

func (n *Native) allowedBridge(grants []api.HostBridgeGrant, c topology.Config) (*netlink.Bridge, error) {
	if err := topology.ValidateBridgeConfig(c); err != nil {
		return nil, err
	}
	if err := topology.ValidateHostBridgeGrants(grants); err != nil {
		return nil, err
	}
	var grant *api.HostBridgeGrant
	for i := range grants {
		if grants[i].Name == c.Bridge {
			grant = &grants[i]
		}
	}
	if grant == nil {
		return nil, fmt.Errorf("E_HOST_BRIDGE_GRANT")
	}
	l, err := n.host.LinkByName(c.Bridge)
	if err != nil {
		return nil, err
	}
	b, ok := l.(*netlink.Bridge)
	if !ok || b.Attrs().Alias != grant.Alias || b.VlanFiltering == nil || !*b.VlanFiltering || b.VlanDefaultPVID == nil || b.MTU != c.MTU || b.Flags&net.FlagUp == 0 {
		return nil, fmt.Errorf("E_HOST_BRIDGE_IDENTITY_OR_CONFIG")
	}
	links, err := n.host.LinkList()
	if err != nil {
		return nil, err
	}
	routes, err := n.host.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	aliases := 0
	for _, link := range links {
		if link.Attrs().Alias == grant.Alias {
			aliases++
		}
	}
	if aliases != 1 || ProtectedLinks(links, routes)[b.Index] {
		return nil, fmt.Errorf("E_PROTECTED_OR_AMBIGUOUS_HOST_BRIDGE")
	}
	// Enslaving a port can otherwise change a bridge's auto-selected MAC. The
	// approved shared bridge must have an administrator-set MAC, not an effect
	// which silently mutates global attributes used by unrelated ports.
	assignment, err := os.ReadFile("/sys/class/net/" + b.Name + "/addr_assign_type")
	if err != nil || strings.TrimSpace(string(assignment)) != "3" {
		return nil, fmt.Errorf("E_HOST_BRIDGE_FIXED_MAC_REQUIRED")
	}
	for _, vlan := range desiredPortVLANs(c, *b.VlanDefaultPVID) {
		if !slices.Contains(grant.VLANs, int(vlan.ID)) {
			return nil, fmt.Errorf("E_HOST_BRIDGE_VLAN_GRANT: %d", vlan.ID)
		}
	}
	return b, nil
}

func desiredPortVLANs(c topology.Config, defaultPVID uint16) []PortVLAN {
	var result []PortVLAN
	for _, id := range c.Trunk {
		if id < 1 || id > 4094 {
			return nil
		}
		result = append(result, PortVLAN{ID: uint16(id)})
	}
	if c.VlanID < 0 || c.VlanID > 4094 {
		return nil
	}
	pvid := uint16(c.VlanID)
	if pvid == 0 && len(c.Trunk) == 0 {
		pvid = defaultPVID
	}
	if pvid != 0 {
		result = append(result, PortVLAN{ID: pvid, Flags: nl.BRIDGE_VLAN_INFO_PVID | nl.BRIDGE_VLAN_INFO_UNTAGGED})
	}
	slices.SortFunc(result, func(a, b PortVLAN) int { return int(a.ID) - int(b.ID) })
	return result
}

func portVLANs(h *netlink.Handle, index int) ([]PortVLAN, error) {
	if index <= 0 || index > math.MaxInt32 {
		return nil, fmt.Errorf("E_BRIDGE_PORT_INDEX")
	}
	table, err := h.BridgeVlanList()
	if err != nil {
		return nil, err
	}
	return normalizePortVLANs(table[int32(index)])
}

func normalizePortVLANs(entries []*nl.BridgeVlanInfo) ([]PortVLAN, error) {
	var result []PortVLAN
	var start *nl.BridgeVlanInfo
	seen := map[uint16]bool{}
	add := func(id, flags uint16) error {
		if id == 0 || id > 4094 || seen[id] || flags & ^uint16(nl.BRIDGE_VLAN_INFO_PVID|nl.BRIDGE_VLAN_INFO_UNTAGGED) != 0 {
			return fmt.Errorf("E_BRIDGE_VLAN_METADATA")
		}
		seen[id] = true
		result = append(result, PortVLAN{ID: id, Flags: flags})
		return nil
	}
	for _, info := range entries {
		if info == nil {
			return nil, fmt.Errorf("E_BRIDGE_VLAN_METADATA")
		}
		flags := info.Flags & ^uint16(nl.BRIDGE_VLAN_INFO_RANGE_BEGIN|nl.BRIDGE_VLAN_INFO_RANGE_END)
		switch {
		case info.Flags&nl.BRIDGE_VLAN_INFO_RANGE_BEGIN != 0:
			if start != nil || info.Flags&nl.BRIDGE_VLAN_INFO_RANGE_END != 0 {
				return nil, fmt.Errorf("E_BRIDGE_VLAN_RANGE")
			}
			start = info
		case info.Flags&nl.BRIDGE_VLAN_INFO_RANGE_END != 0:
			if start == nil || info.Vid < start.Vid || info.Vid > 4094 || flags != start.Flags & ^uint16(nl.BRIDGE_VLAN_INFO_RANGE_BEGIN) {
				return nil, fmt.Errorf("E_BRIDGE_VLAN_RANGE")
			}
			for id := start.Vid; id <= info.Vid; id++ {
				if err := add(id, flags); err != nil {
					return nil, err
				}
			}
			start = nil
		default:
			if start != nil {
				return nil, fmt.Errorf("E_BRIDGE_VLAN_RANGE")
			}
			if err := add(info.Vid, flags); err != nil {
				return nil, err
			}
		}
	}
	if start != nil {
		return nil, fmt.Errorf("E_BRIDGE_VLAN_RANGE")
	}
	slices.SortFunc(result, func(a, b PortVLAN) int { return int(a.ID) - int(b.ID) })
	return result, nil
}

func (n *Native) captureBridge(input Handle, c topology.Config) (*BridgePortState, error) {
	l, err := n.representor(input)
	if err != nil {
		return nil, err
	}
	return n.captureBridgePort(input, l, c)
}

// The kernel port primitives also have native Linux checks without pretending
// that the lab VM's dummy interfaces are switchdev representors.
func (n *Native) captureBridgePort(input Handle, l netlink.Link, c topology.Config) (*BridgePortState, error) {
	b, err := n.allowedBridge(input.AllowedHostBridges, c)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(l.Attrs().Alias, "dra-mn:") {
		return nil, fmt.Errorf("E_REPRESENTOR_ALREADY_OWNED")
	}
	if err := n.preflightLinkUsers(l, false, b.Index); err != nil {
		return nil, err
	}
	v, err := portVLANs(n.host, l.Attrs().Index)
	if err != nil {
		return nil, err
	}
	return &BridgePortState{Before: state(l), BeforeVLANs: v, TargetVLANs: desiredPortVLANs(c, *b.VlanDefaultPVID), Bridge: state(b), BridgeIndex: b.Index, DefaultPVID: *b.VlanDefaultPVID}, nil
}

func (n *Native) bridgeAnchor(r Record) (*netlink.Bridge, error) {
	b, err := n.allowedBridge(r.Output.AllowedHostBridges, r.Effect.Parameters)
	if err != nil {
		return nil, err
	}
	if r.Bridge == nil || b.Index != r.Bridge.BridgeIndex || state(b) != r.Bridge.Bridge || *b.VlanDefaultPVID != r.Bridge.DefaultPVID {
		return nil, fmt.Errorf("E_HOST_BRIDGE_INCARNATION_OR_STATE")
	}
	return b, nil
}

func bridgePortGuard(r Record, l netlink.Link, vlans []PortVLAN) error {
	b := r.Bridge
	if b == nil {
		return fmt.Errorf("E_BRIDGE_BEFORE_IMAGE")
	}
	s := state(l)
	if s.Name != b.Before.Name || s.MAC != b.Before.MAC || l.Attrs().Index != r.Output.IfIndex {
		return fmt.Errorf("E_REPRESENTOR_INCARNATION_OR_MAC")
	}
	if s.Alias == b.Before.Alias {
		if s != b.Before || !slices.Equal(vlans, b.BeforeVLANs) {
			return fmt.Errorf("E_REPRESENTOR_ORIGINAL_CHANGED")
		}
		return nil
	}
	if s.Alias != r.Output.Alias || s.MTU != b.Before.MTU && s.MTU != r.Effect.Parameters.MTU || s.MasterIndex != b.Before.MasterIndex && s.MasterIndex != b.BridgeIndex && s.MasterIndex != 0 {
		return fmt.Errorf("E_REPRESENTOR_FOREIGN_STATE")
	}
	allowed := append(slices.Clone(b.BeforeVLANs), b.TargetVLANs...)
	if b.DefaultPVID != 0 {
		allowed = append(allowed, PortVLAN{ID: b.DefaultPVID, Flags: nl.BRIDGE_VLAN_INFO_PVID | nl.BRIDGE_VLAN_INFO_UNTAGGED})
	}
	for _, vlan := range vlans {
		if !slices.Contains(allowed, vlan) {
			return fmt.Errorf("E_REPRESENTOR_FOREIGN_VLAN")
		}
	}
	return nil
}

func setPortVLANs(h *netlink.Handle, l netlink.Link, target []PortVLAN) error {
	current, err := portVLANs(h, l.Attrs().Index)
	if err != nil {
		return err
	}
	for _, v := range current {
		if !slices.Contains(target, v) {
			if err := h.BridgeVlanDel(l, v.ID, v.Flags&nl.BRIDGE_VLAN_INFO_PVID != 0, v.Flags&nl.BRIDGE_VLAN_INFO_UNTAGGED != 0, false, false); err != nil {
				return err
			}
		}
	}
	for _, v := range target {
		if !slices.Contains(current, v) {
			if err := h.BridgeVlanAdd(l, v.ID, v.Flags&nl.BRIDGE_VLAN_INFO_PVID != 0, v.Flags&nl.BRIDGE_VLAN_INFO_UNTAGGED != 0, false, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (n *Native) applyBridge(r *Record, save func() error) error {
	l, err := n.representor(r.Output)
	if err != nil {
		return err
	}
	return n.applyBridgePort(r, l, save)
}

func (n *Native) applyBridgePort(r *Record, l netlink.Link, save func() error) error {
	if r.Bridge == nil || r.Bridge.Restoring {
		return fmt.Errorf("E_BRIDGE_RESTORING_OR_MISSING_INTENT")
	}
	if r.State == Applied {
		return n.observeBridgePort(*r, l)
	}
	b, err := n.bridgeAnchor(*r)
	if err != nil {
		return err
	}
	v, err := portVLANs(n.host, l.Attrs().Index)
	if err != nil {
		return err
	}
	if err := bridgePortGuard(*r, l, v); err != nil {
		return err
	}
	if l.Attrs().Alias != r.Output.Alias {
		if err := n.host.LinkSetAlias(l, r.Output.Alias); err != nil {
			return err
		}
	}
	// Keep the port down until access/trunk VLANs are installed. The bridge's
	// automatically added default PVID must not temporarily forward traffic.
	if err := n.host.LinkSetDown(l); err != nil {
		return err
	}
	if err := n.host.LinkSetMTU(l, r.Effect.Parameters.MTU); err != nil {
		return err
	}
	if l.Attrs().MasterIndex != b.Index {
		if err := n.host.LinkSetMaster(l, b); err != nil {
			return err
		}
	}
	if err := setPortVLANs(n.host, l, r.Bridge.TargetVLANs); err != nil {
		return err
	}
	if r.Effect.Parameters.Up {
		if err := n.host.LinkSetUp(l); err != nil {
			return err
		}
	}
	if err := n.observeBridgePort(*r, l); err != nil {
		return err
	}
	return save()
}

func (n *Native) observeBridge(r Record) error {
	l, err := n.link(r.Output)
	if err != nil {
		return err
	}
	return n.observeBridgePort(r, l)
}

func (n *Native) observeBridgePort(r Record, l netlink.Link) error {
	if _, err := n.bridgeAnchor(r); err != nil {
		return err
	}
	actual, err := n.host.LinkByIndex(l.Attrs().Index)
	if err != nil {
		return err
	}
	v, err := portVLANs(n.host, actual.Attrs().Index)
	if err != nil {
		return err
	}
	b := r.Bridge
	if err := bridgePortGuard(r, actual, v); err != nil {
		return err
	}
	s := state(actual)
	if s.Alias != r.Output.Alias || s.MTU != r.Effect.Parameters.MTU || s.Up != r.Effect.Parameters.Up || s.MasterIndex != b.BridgeIndex || !slices.Equal(v, b.TargetVLANs) {
		return fmt.Errorf("E_BRIDGE_PORT_POSTCONDITION")
	}
	return nil
}

func (n *Native) restoreBridge(r *Record, save func() error) error {
	l, err := n.representor(r.Output)
	if err != nil {
		return err
	}
	return n.restoreBridgePort(r, l, save)
}

func (n *Native) restoreBridgePort(r *Record, l netlink.Link, save func() error) error {
	v, err := portVLANs(n.host, l.Attrs().Index)
	if err != nil {
		return err
	}
	if err := bridgePortGuard(*r, l, v); err != nil {
		return err
	}
	b := r.Bridge
	if state(l) == b.Before && slices.Equal(v, b.BeforeVLANs) {
		return nil
	}
	if _, err := n.bridgeAnchor(*r); err != nil {
		return err
	}
	if !b.Restoring {
		if r.State == Applied {
			if err := n.observeBridgePort(*r, l); err != nil {
				return err
			}
		}
		b.Restoring = true
		if err := save(); err != nil {
			return err
		}
	}
	if err := n.host.LinkSetDown(l); err != nil {
		return err
	}
	if b.Before.MasterIndex == 0 {
		if err := n.host.LinkSetNoMaster(l); err != nil {
			return err
		}
	} else {
		master, err := n.host.LinkByIndex(b.Before.MasterIndex)
		if err != nil {
			return err
		}
		if err := n.host.LinkSetMaster(l, master); err != nil {
			return err
		}
		if err := setPortVLANs(n.host, l, b.BeforeVLANs); err != nil {
			return err
		}
	}
	if err := n.host.LinkSetMTU(l, b.Before.MTU); err != nil {
		return err
	}
	if b.Before.Up {
		if err := n.host.LinkSetUp(l); err != nil {
			return err
		}
	}
	actual, err := n.host.LinkByIndex(l.Attrs().Index)
	if err != nil {
		return err
	}
	v, err = portVLANs(n.host, actual.Attrs().Index)
	if err != nil {
		return err
	}
	expected := b.Before
	expected.Alias = r.Output.Alias
	if state(actual) != expected || !slices.Equal(v, b.BeforeVLANs) {
		return fmt.Errorf("E_BRIDGE_RESTORE_POSTCONDITION")
	}
	if err := n.host.LinkSetAlias(actual, b.Before.Alias); err != nil {
		return err
	}
	return save()
}

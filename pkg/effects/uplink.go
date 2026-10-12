// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

// Each VLAN slot has a durable before-image and a set of attachment owners.
// The bridge and uplink remain administrator-owned; only this tagged VID is mutated.
type UplinkLease struct {
	Key         string    `json:"key"`
	Bridge      LinkState `json:"bridge"`
	BridgeIndex int       `json:"bridgeIndex"`
	Link        LinkState `json:"link"`
	LinkIndex   int       `json:"linkIndex"`
	PCI         string    `json:"pci"`
	VLAN        PortVLAN  `json:"vlan"`
	Before      *PortVLAN `json:"before,omitempty"`
}
type uplinkVLANState struct {
	Lease  UplinkLease     `json:"lease"`
	Owners map[string]bool `json:"owners"`
	Phase  string          `json:"phase"`
}

const uplinkActive = "Active"
const uplinkRemoving = "Removing"
const uplinkReleased = "Released"

func sameUplink(a, b UplinkLease) bool {
	a.Before, b.Before = nil, nil
	return topology.Digest(a) == topology.Digest(b)
}
func (n *Native) captureUplink(input Handle, c topology.Config) ([]UplinkLease, error) {
	if _, err := n.representor(input); err != nil {
		return nil, err
	}
	b, err := n.allowedBridge(input.AllowedHostBridges, c)
	if err != nil {
		return nil, err
	}
	for _, g := range input.AllowedHostBridges {
		if g.Name != c.Bridge {
			continue
		}
		for _, u := range g.Uplinks {
			if u.Name != c.Uplink {
				continue
			}
			l, err := n.host.LinkByName(u.Name)
			if err != nil {
				return nil, err
			}
			pci, err := PCIOwner("/sys", filepath.Join("/sys/class/net", u.Name, "device"))
			if err != nil || pci != u.PCIAddress || l.Attrs().Alias != u.Alias || l.Attrs().MasterIndex != b.Index || !state(l).Up {
				return nil, fmt.Errorf("E_UPLINK_GRANT_OR_IDENTITY")
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
				if link.Attrs().Alias == u.Alias {
					aliases++
				}
			}
			if aliases != 1 || ProtectedLinks(links, routes)[l.Attrs().Index] {
				return nil, fmt.Errorf("E_UPLINK_PROTECTED_OR_AMBIGUOUS")
			}
			var vids []PortVLAN
			for _, v := range desiredPortVLANs(c, *b.VlanDefaultPVID) {
				if !slices.Contains(u.VLANs, int(v.ID)) {
					return nil, fmt.Errorf("E_UPLINK_VLAN_GRANT")
				}
				vids = append(vids, PortVLAN{ID: v.ID})
			}
			return n.captureUplinkSlots(b, l, u.PCIAddress, vids)
		}
	}
	return nil, fmt.Errorf("E_UPLINK_GRANT")
}

// Linux primitives are separately checked with temporary bridge/dummy ports;
// production callers always validate the PCI/policy grant above first.
func (n *Native) captureUplinkSlots(b, l netlink.Link, pci string, vids []PortVLAN) ([]UplinkLease, error) {
	if n.UplinkStore == nil || len(vids) == 0 {
		return nil, fmt.Errorf("E_UPLINK_JOURNAL_OR_VLANS")
	}
	current, err := portVLANs(n.host, l.Attrs().Index)
	if err != nil {
		return nil, err
	}
	leases := make([]UplinkLease, 0, len(vids))
	for _, v := range vids {
		if v.ID == 0 || v.ID > 4094 || v.Flags != 0 {
			return nil, fmt.Errorf("E_UPLINK_TAGGED_VLAN_ONLY")
		}
		x := UplinkLease{Bridge: state(b), BridgeIndex: b.Attrs().Index, Link: state(l), LinkIndex: l.Attrs().Index, PCI: pci, VLAN: v}
		x.Key = "uplink/" + topology.Digest(x)
		for _, actual := range current {
			if actual.ID != v.ID {
				continue
			}
			if actual != v {
				return nil, fmt.Errorf("E_UPLINK_FOREIGN_VLAN_FLAGS")
			}
			before := actual
			x.Before = &before
		}
		var saved uplinkVLANState
		exists, err := n.UplinkStore.Get(x.Key, &saved)
		if err != nil {
			return nil, err
		}
		if exists && saved.Phase != uplinkReleased {
			if !sameUplink(x, saved.Lease) || saved.Phase == uplinkRemoving {
				return nil, fmt.Errorf("E_UPLINK_RESERVED")
			}
			x.Before = saved.Lease.Before
		}
		leases = append(leases, x)
	}
	return leases, nil
}
func (n *Native) uplinkAnchor(x UplinkLease) (netlink.Link, error) {
	b, err := n.host.LinkByIndex(x.BridgeIndex)
	if err != nil {
		return nil, err
	}
	l, err := n.host.LinkByIndex(x.LinkIndex)
	if err != nil {
		return nil, err
	}
	if b.Type() != "bridge" || state(b) != x.Bridge || state(l) != x.Link || l.Attrs().MasterIndex != b.Attrs().Index {
		return nil, fmt.Errorf("E_UPLINK_INCARNATION_OR_STATE")
	}
	return l, nil
}
func (n *Native) uplinkVID(x UplinkLease) (*PortVLAN, error) {
	current, err := portVLANs(n.host, x.LinkIndex)
	if err != nil {
		return nil, err
	}
	for _, v := range current {
		if v.ID == x.VLAN.ID {
			return &v, nil
		}
	}
	return nil, nil
}
func (n *Native) uplinkRecord(x UplinkLease) (uplinkVLANState, bool, error) {
	var r uplinkVLANState
	if n.UplinkStore == nil {
		return r, false, fmt.Errorf("E_UPLINK_JOURNAL")
	}
	found, err := n.UplinkStore.Get(x.Key, &r)
	if err == nil && found && !sameUplink(x, r.Lease) {
		err = fmt.Errorf("E_UPLINK_LEDGER_IDENTITY")
	}
	return r, found, err
}
func (n *Native) acquireUplink(x UplinkLease, owner string) error {
	if !strings.HasPrefix(owner, "dra-mn:") {
		return fmt.Errorf("E_UPLINK_OWNER")
	}
	l, err := n.uplinkAnchor(x)
	if err != nil {
		return err
	}
	r, exists, err := n.uplinkRecord(x)
	if err != nil {
		return err
	}
	actual, err := n.uplinkVID(x)
	if err != nil {
		return err
	}
	if actual != nil && *actual != x.VLAN {
		return fmt.Errorf("E_UPLINK_FOREIGN_VLAN_FLAGS")
	}
	if !exists || r.Phase == uplinkReleased {
		// A new lease must retain exactly the captured administrator before-image.
		if (actual == nil) != (x.Before == nil) {
			return fmt.Errorf("E_UPLINK_BEFORE_IMAGE_CHANGED")
		}
		r = uplinkVLANState{Lease: x, Owners: map[string]bool{}, Phase: Applying}
	} else if r.Phase == uplinkRemoving || r.Owners == nil {
		return fmt.Errorf("E_UPLINK_RESERVED")
	}
	if r.Phase == uplinkActive && actual == nil {
		return fmt.Errorf("E_UPLINK_OWNED_VLAN_REMOVED")
	}
	r.Owners[owner] = true
	if err := n.UplinkStore.Put(x.Key, r); err != nil {
		return err
	}
	if actual == nil {
		if err := n.host.BridgeVlanAdd(l, x.VLAN.ID, false, false, false, false); err != nil {
			return err
		}
	}
	actual, err = n.uplinkVID(x)
	if err != nil {
		return err
	}
	if actual == nil || *actual != x.VLAN {
		return fmt.Errorf("E_UPLINK_POSTCONDITION")
	}
	r.Phase = uplinkActive
	return n.UplinkStore.Put(x.Key, r)
}
func (n *Native) foreignUplinkDependency(x UplinkLease) error {
	links, err := n.host.LinkList()
	if err != nil {
		return err
	}
	for _, l := range links {
		if l.Attrs().Index == x.LinkIndex || l.Attrs().MasterIndex != x.BridgeIndex {
			continue
		}
		vlans, err := portVLANs(n.host, l.Attrs().Index)
		if err != nil {
			return err
		}
		for _, v := range vlans {
			if v.ID == x.VLAN.ID {
				return fmt.Errorf("E_UPLINK_FOREIGN_PORT_DEPENDENCY")
			}
		}
	}
	return nil
}
func (n *Native) releaseUplink(x UplinkLease, owner string) error {
	r, exists, err := n.uplinkRecord(x)
	if err != nil {
		return err
	}
	if !exists || r.Phase == uplinkReleased || !r.Owners[owner] {
		return nil
	}
	l, err := n.uplinkAnchor(x)
	if err != nil {
		return err
	}
	actual, err := n.uplinkVID(x)
	if err != nil {
		return err
	}
	if actual != nil && *actual != x.VLAN {
		return fmt.Errorf("E_UPLINK_FOREIGN_VLAN_FLAGS")
	}
	if len(r.Owners) > 1 {
		if actual == nil {
			return fmt.Errorf("E_UPLINK_OWNED_VLAN_REMOVED")
		}
		delete(r.Owners, owner)
		return n.UplinkStore.Put(x.Key, r)
	}
	if r.Lease.Before != nil {
		if actual == nil {
			return fmt.Errorf("E_UPLINK_ADMIN_VLAN_REMOVED")
		}
	} else {
		if actual == nil && r.Phase == uplinkActive {
			return fmt.Errorf("E_UPLINK_OWNED_VLAN_REMOVED")
		}
		if actual != nil {
			if err := n.foreignUplinkDependency(x); err != nil {
				return err
			}
		}
		r.Phase = uplinkRemoving
		if err := n.UplinkStore.Put(x.Key, r); err != nil {
			return err
		}
		if actual != nil {
			if err := n.host.BridgeVlanDel(l, x.VLAN.ID, false, false, false, false); err != nil {
				return err
			}
		}
		actual, err = n.uplinkVID(x)
		if err != nil {
			return err
		}
		if actual != nil {
			return fmt.Errorf("E_UPLINK_RELEASE_POSTCONDITION")
		}
	}
	delete(r.Owners, owner)
	r.Phase = uplinkReleased
	return n.UplinkStore.Put(x.Key, r)
}
func (n *Native) uplinkRecordGrant(r Record) error {
	current, err := n.captureUplink(r.Output, r.Effect.Parameters)
	if err != nil {
		return err
	}
	if len(current) != len(r.Uplinks) || r.UplinkOwner == "" {
		return fmt.Errorf("E_UPLINK_SAVED_GRANT")
	}
	for i, x := range current {
		if !sameUplink(x, r.Uplinks[i]) {
			return fmt.Errorf("E_UPLINK_SAVED_IDENTITY")
		}
	}
	return nil
}
func (n *Native) applyUplink(r *Record) error {
	if err := n.uplinkRecordGrant(*r); err != nil {
		return err
	}
	for _, x := range r.Uplinks {
		if err := n.acquireUplink(x, r.UplinkOwner); err != nil {
			return err
		}
	}
	return nil
}
func (n *Native) observeUplink(r Record) error {
	if err := n.uplinkRecordGrant(r); err != nil {
		return err
	}
	for _, x := range r.Uplinks {
		saved, exists, err := n.uplinkRecord(x)
		if err != nil {
			return err
		}
		actual, err := n.uplinkVID(x)
		if err != nil {
			return err
		}
		if !exists || saved.Phase != uplinkActive || !saved.Owners[r.UplinkOwner] || actual == nil || *actual != x.VLAN {
			return fmt.Errorf("E_UPLINK_NOT_READY")
		}
	}
	return nil
}
func (n *Native) restoreUplink(r *Record) error {
	// Revalidate immutable policy/PCI authority without recapturing a pending
	// Removing lease, which must be allowed to finish after a lost delete ack.
	if _, err := n.representor(r.Output); err != nil {
		return err
	}
	for _, x := range r.Uplinks {
		pci, err := PCIOwner("/sys", filepath.Join("/sys/class/net", x.Link.Name, "device"))
		if err != nil || pci != x.PCI {
			return fmt.Errorf("E_UPLINK_RESTORE_PCI")
		}
		if err := n.releaseUplink(x, r.UplinkOwner); err != nil {
			return err
		}
	}
	return nil
}

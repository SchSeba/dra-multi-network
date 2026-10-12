// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func addressGrant(inputs map[string]Handle) (Handle, error) {
	h, lease := inputs[InterfacePort], inputs[topology.LeasePort]
	if h.Type != topology.InterfaceType || h.Shared || h.Scope != SandboxScope || !strings.HasPrefix(h.Alias, "dra-mn:") || lease.Type != topology.AddressLeaseType || lease.Lease == nil || lease.Address == "" || lease.Address != lease.Lease.Address {
		return h, fmt.Errorf("E_ADDRESS_SCOPE: require owned private sandbox interface and granted lease")
	}
	addr, err := netlink.ParseAddr(lease.Address)
	if err != nil || addr.IP.To4() == nil {
		return h, fmt.Errorf("E_ADDRESS_LEASE")
	}
	h.Address = lease.Address
	return h, nil
}
func (n *Native) captureAddress(inputs map[string]Handle) (Handle, error) {
	h, err := addressGrant(inputs)
	if err != nil {
		return h, err
	}
	link, err := n.link(h)
	if err != nil {
		return h, err
	}
	addresses, err := n.sandbox.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return h, err
	}
	wanted, _ := netlink.ParseAddr(h.Address)
	for _, addr := range addresses {
		if addr.IP.Equal(wanted.IP) {
			return h, fmt.Errorf("E_ADDRESS_FOREIGN: address present before owned intent")
		}
	}
	return h, nil
}
func (n *Native) applyAddress(r Record) error {
	h, err := addressGrant(r.Inputs)
	if err != nil {
		return err
	}
	if h.Address != r.Output.Address {
		return fmt.Errorf("E_ADDRESS_IDENTITY")
	}
	l, err := n.link(r.Output)
	if err != nil {
		return err
	}
	addr, err := netlink.ParseAddr(r.Output.Address)
	if err != nil {
		return err
	}
	if err := n.sandbox.AddrAdd(l, addr); err != nil && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	return nil
}
func (n *Native) observeAddress(r Record) error {
	l, err := n.link(r.Output)
	if err != nil {
		return err
	}
	addresses, err := n.sandbox.AddrList(l, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	wanted, err := netlink.ParseAddr(r.Output.Address)
	if err != nil {
		return err
	}
	for _, addr := range addresses {
		if addr.Equal(*wanted) {
			return nil
		}
	}
	return fmt.Errorf("E_ADDRESS_STATE: allocated address absent")
}
func (n *Native) restoreAddress(r Record) error {
	l, scope, err := n.owned(r.Output.Alias)
	if err != nil {
		return err
	}
	if l == nil {
		return nil
	}
	if scope != SandboxScope || r.Output.IfIndex != 0 && r.Output.IfIndex != l.Attrs().Index {
		return fmt.Errorf("E_ADDRESS_RESTORE_IDENTITY")
	}
	addr, err := netlink.ParseAddr(r.Output.Address)
	if err != nil {
		return err
	}
	addresses, err := n.sandbox.AddrList(l, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for _, actual := range addresses {
		if actual.IP.Equal(addr.IP) && !actual.Equal(*addr) {
			return fmt.Errorf("E_ADDRESS_FOREIGN: address prefix changed")
		}
		if actual.Equal(*addr) {
			return n.sandbox.AddrDel(l, addr)
		}
	}
	return nil
}

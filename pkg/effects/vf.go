// Copyright 2015 CNI authors; Copyright 2017 Intel Corporation;
// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
// Adapted from sriov-cni/pkg/sriov ApplyVFConfig and ResetVFConfig at
// b536073521baf5bfba748e9d3da27ef736f48679. Targets come from DRA grants;
// the synchronous effect journal owns snapshots, retries and restoration.
package effects

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

const (
	pciDeviceAttribute = "device"
	pciVendorAttribute = "vendor"
)

type VFValues struct {
	MAC          string `json:"mac"`
	Trust        bool   `json:"trust"`
	SpoofCheck   bool   `json:"spoofCheck"`
	LinkState    uint32 `json:"linkState"`
	VlanID       int    `json:"vlanId"`
	VlanQoS      int    `json:"vlanQoS"`
	VlanProtocol int    `json:"vlanProtocol"`
	MinTxRate    uint32 `json:"minTxRate"`
	MaxTxRate    uint32 `json:"maxTxRate"`
}
type VFState struct {
	Values       VFValues   `json:"values"`
	Vendor       string     `json:"vendor"`
	Device       string     `json:"device"`
	ParentVendor string     `json:"parentVendor"`
	ParentDevice string     `json:"parentDevice"`
	Link         *LinkState `json:"link,omitempty"`
}

// A read-only snapshot derived from the exclusively allocated PCI function.
// It does not manufacture grants to configure this VF or its siblings.
type PCIVFState struct {
	ParentPCI string  `json:"parentPCI"`
	VFID      int     `json:"vfID"`
	Before    VFState `json:"before"`
}

func pciVFHandle(sysRoot string, h Handle) (*Handle, error) {
	if h.Type != topology.PCIFunctionType || h.Scope != HostScope || h.Shared || h.Alias != "" || !pciAddress.MatchString(h.PCIAddress) {
		return nil, fmt.Errorf("E_PCI_GRANT")
	}
	function, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "bus/pci/devices", h.PCIAddress))
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Join(function, "physfn"))
	if os.IsNotExist(err) {
		return nil, nil // A physical function has no VF administrative state.
	}
	if err != nil || !pciAddress.MatchString(filepath.Base(parent)) {
		return nil, fmt.Errorf("E_VF_PARENT_IDENTITY: %w", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	index := -1
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "virtfn") {
			continue
		}
		vf, err := filepath.EvalSymlinks(filepath.Join(parent, entry.Name()))
		if err != nil {
			return nil, err
		}
		if vf != function {
			continue
		}
		id, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "virtfn"))
		if err != nil || id < 0 || id > 4095 || index != -1 {
			return nil, fmt.Errorf("E_VF_INDEX_IDENTITY")
		}
		index = id
	}
	if index == -1 || h.ParentPCI != "" && (h.ParentPCI != filepath.Base(parent) || h.VFID != index) {
		return nil, fmt.Errorf("E_VF_INDEX_IDENTITY")
	}
	h.ParentPCI, h.VFID = filepath.Base(parent), index
	if _, err := vfParent(sysRoot, h); err != nil {
		return nil, err
	}
	return &h, nil
}

func vfParent(sysRoot string, h Handle) (string, error) {
	if h.Scope != HostScope || h.Shared || h.Alias != "" || (h.Type != topology.InterfaceType && h.Type != topology.PCIFunctionType) || !pciAddress.MatchString(h.PCIAddress) || !pciAddress.MatchString(h.ParentPCI) || h.VFID < 0 || h.VFID > 4095 {
		return "", fmt.Errorf("E_VF_GRANT")
	}
	parent, err := vfParentPCI(sysRoot, h.PCIAddress, h.ParentPCI, h.VFID)
	if err != nil {
		return "", err
	}
	interfaces, err := os.ReadDir(filepath.Join(parent, "net"))
	if err != nil || len(interfaces) != 1 {
		return "", fmt.Errorf("E_PF_REDISCOVERY: require one verified PF netdevice")
	}
	return interfaces[0].Name(), nil
}

// Both endpoint administration and host representor lookup require the same
// bidirectional VF/PF association, including canonical paths rather than BDF
// basenames copied onto unrelated symlink targets.
func vfParentPCI(sysRoot, vfPCI, pfPCI string, vfID int) (string, error) {
	if !pciAddress.MatchString(vfPCI) || !pciAddress.MatchString(pfPCI) || vfPCI == pfPCI || vfID < 0 || vfID > 65535 {
		return "", fmt.Errorf("E_VF_PARENT_IDENTITY")
	}
	base := filepath.Join(sysRoot, "bus/pci/devices")
	parent, err := filepath.EvalSymlinks(filepath.Join(base, vfPCI, "physfn"))
	if err != nil {
		return "", err
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(base, pfPCI))
	if err != nil || parent != expected {
		return "", fmt.Errorf("E_VF_PARENT_IDENTITY")
	}
	vf, err := filepath.EvalSymlinks(filepath.Join(parent, "virtfn"+strconv.Itoa(vfID)))
	if err != nil {
		return "", err
	}
	expected, err = filepath.EvalSymlinks(filepath.Join(base, vfPCI))
	if err != nil || vf != expected {
		return "", fmt.Errorf("E_VF_INDEX_IDENTITY")
	}
	return parent, nil
}
func (n *Native) readVF(h Handle) (netlink.Link, VFValues, error) {
	name, err := vfParent("/sys", h)
	if err != nil {
		return nil, VFValues{}, err
	}
	pf, err := n.link(Handle{Type: topology.InterfaceType, Scope: HostScope, Name: name, PCIAddress: h.ParentPCI})
	if err != nil {
		return nil, VFValues{}, err
	}
	for _, vf := range pf.Attrs().Vfs {
		if vf.ID != h.VFID {
			continue
		}
		if len(vf.Mac) != 6 || vf.Trust > 1 || vf.LinkState > netlink.VF_LINK_STATE_DISABLE {
			return nil, VFValues{}, fmt.Errorf("E_VF_STATE_PROFILE")
		}
		proto := vf.VlanProto
		if proto == 0 {
			proto = unix.ETH_P_8021Q
		}
		maxRate := vf.MaxTxRate
		if vf.TxRate < 0 || vf.TxRate > 1000000 {
			return nil, VFValues{}, fmt.Errorf("E_VF_RATE_PROFILE")
		}
		if maxRate == 0 && vf.TxRate > 0 {
			maxRate = uint32(vf.TxRate)
		}
		return pf, VFValues{MAC: vf.Mac.String(), Trust: vf.Trust == 1, SpoofCheck: vf.Spoofchk, LinkState: vf.LinkState, VlanID: vf.Vlan, VlanQoS: vf.Qos, VlanProtocol: proto, MinTxRate: vf.MinTxRate, MaxTxRate: maxRate}, nil
	}
	return nil, VFValues{}, fmt.Errorf("E_VF_INDEX_ABSENT")
}
func vfTarget(before VFValues, config topology.VFSettings) VFValues {
	target := before
	if config.MAC != "" {
		mac, _ := net.ParseMAC(config.MAC)
		target.MAC = mac.String()
	}
	if config.Trust != nil {
		target.Trust = *config.Trust
	}
	if config.SpoofCheck != nil {
		target.SpoofCheck = *config.SpoofCheck
	}
	if config.LinkState != "" {
		target.LinkState = map[string]uint32{"auto": netlink.VF_LINK_STATE_AUTO, "enable": netlink.VF_LINK_STATE_ENABLE, "disable": netlink.VF_LINK_STATE_DISABLE}[config.LinkState]
	}
	if config.VlanID != nil {
		target.VlanID = *config.VlanID
	}
	if config.VlanQoS != nil {
		target.VlanQoS = *config.VlanQoS
	}
	if config.VlanProtocol != "" {
		target.VlanProtocol = map[string]int{"802.1Q": unix.ETH_P_8021Q, "802.1ad": unix.ETH_P_8021AD}[config.VlanProtocol]
	}
	if config.MinTxRate != nil {
		target.MinTxRate = *config.MinTxRate
	}
	if config.MaxTxRate != nil {
		target.MaxTxRate = *config.MaxTxRate
	}
	return target
}
func vfMayReconcile(current, before, target VFValues) error {
	fields := func(v VFValues) map[string]json.RawMessage {
		data, _ := json.Marshal(v)
		var result map[string]json.RawMessage
		_ = json.Unmarshal(data, &result)
		return result
	}
	original, desired := fields(before), fields(target)
	for name, value := range fields(current) {
		if string(value) != string(original[name]) && string(value) != string(desired[name]) {
			return fmt.Errorf("E_EXTERNAL_VF_SETTING: %s", name)
		}
	}
	return nil
}
func (n *Native) captureVF(h Handle, config *topology.VFSettings) (*VFState, error) {
	if config == nil {
		return nil, fmt.Errorf("E_VF_CONFIG")
	}
	if err := topology.ValidateVFSettings(*config); err != nil {
		return nil, err
	}
	for _, field := range config.Fields() {
		if !slices.Contains(h.AllowedVFSettings, field) {
			return nil, fmt.Errorf("E_VF_SETTING_DENIED: %s", field)
		}
	}
	_, current, err := n.readVF(h)
	if err != nil {
		return nil, err
	}
	target := vfTarget(current, *config)
	if target.MaxTxRate != 0 && target.MinTxRate > target.MaxTxRate {
		return nil, fmt.Errorf("E_VF_RATE")
	}
	return n.snapshotVF(h, current)
}

func (n *Native) snapshotVF(h Handle, current VFValues) (*VFState, error) {
	before := &VFState{Values: current}
	var err error
	for _, item := range []struct {
		pci, field string
		dest       *string
	}{{h.PCIAddress, pciVendorAttribute, &before.Vendor}, {h.PCIAddress, pciDeviceAttribute, &before.Device}, {h.ParentPCI, pciVendorAttribute, &before.ParentVendor}, {h.ParentPCI, pciDeviceAttribute, &before.ParentDevice}} {
		*item.dest, err = readPCI(item.pci, item.field)
		if err != nil {
			return nil, err
		}
	}
	driver, err := pciDriver(h.PCIAddress)
	if err != nil {
		return nil, err
	}
	if driver != topology.VFIODriver {
		link, err := n.link(h)
		if err != nil {
			return nil, err
		}
		original := state(link)
		if original.Alias != "" || original.MasterIndex != 0 {
			return nil, fmt.Errorf("E_EXTERNAL_VF_OWNER")
		}
		before.Link = &original
	}
	return before, nil
}

func (n *Native) snapshotPCIVF(h Handle) (*PCIVFState, error) {
	function, err := pciVFHandle("/sys", h)
	if err != nil || function == nil {
		return nil, err
	}
	_, current, err := n.readVF(*function)
	if err != nil {
		return nil, err
	}
	before, err := n.snapshotVF(*function, current)
	if err != nil {
		return nil, err
	}
	return &PCIVFState{ParentPCI: function.ParentPCI, VFID: function.VFID, Before: *before}, nil
}

func (n *Native) verifyPCIVF(pci string, before *PCIVFState) error {
	if before == nil { // Existing journals predate this additional snapshot.
		return nil
	}
	h := Handle{Type: topology.PCIFunctionType, Scope: HostScope, PCIAddress: pci, ParentPCI: before.ParentPCI, VFID: before.VFID}
	if err := verifyVFIdentity(h, before.Before); err != nil {
		return err
	}
	_, current, err := n.readVF(h)
	if err != nil {
		return err
	}
	if current != before.Before.Values {
		return fmt.Errorf("E_VF_ADMIN_CHANGED: retain binding/reset scope; no grant to overwrite administrative state")
	}
	return nil
}
func verifyVFIdentity(h Handle, before VFState) error {
	if _, err := vfParent("/sys", h); err != nil {
		return err
	}
	for _, item := range []struct{ pci, field, expected string }{{h.PCIAddress, pciVendorAttribute, before.Vendor}, {h.PCIAddress, pciDeviceAttribute, before.Device}, {h.ParentPCI, pciVendorAttribute, before.ParentVendor}, {h.ParentPCI, pciDeviceAttribute, before.ParentDevice}} {
		current, err := readPCI(item.pci, item.field)
		if err != nil || current != item.expected {
			return fmt.Errorf("E_VF_INCARNATION: %s", item.field)
		}
	}
	return nil
}
func (n *Native) observeVF(r Record) error {
	if r.VF == nil || r.Effect.Parameters.VF == nil {
		return fmt.Errorf("E_VF_BEFORE_IMAGE")
	}
	h := r.Inputs[topology.DevicePort]
	if err := verifyVFIdentity(h, *r.VF); err != nil {
		return err
	}
	_, current, err := n.readVF(h)
	if err != nil {
		return err
	}
	if current != vfTarget(r.VF.Values, *r.Effect.Parameters.VF) {
		return fmt.Errorf("E_VF_POSTCONDITION")
	}
	return nil
}
func (n *Native) applyVF(r *Record, restore bool) error {
	if r.VF == nil || r.Effect.Parameters.VF == nil {
		return fmt.Errorf("E_VF_BEFORE_IMAGE")
	}
	h := r.Inputs[topology.DevicePort]
	if err := verifyVFIdentity(h, *r.VF); err != nil {
		return err
	}
	pf, current, err := n.readVF(h)
	if err != nil {
		return err
	}
	config := *r.Effect.Parameters.VF
	desired := vfTarget(r.VF.Values, config)
	if err := vfMayReconcile(current, r.VF.Values, desired); err != nil {
		return err
	}
	target := desired
	if restore {
		target = r.VF.Values
	}
	var vfLink netlink.Link
	if r.VF.Link != nil {
		vfLink, err = n.link(h)
		if err != nil {
			return err
		}
		attrs := vfLink.Attrs()
		if attrs.Alias != "" || attrs.MasterIndex != 0 || attrs.MTU != r.VF.Link.MTU {
			return fmt.Errorf("E_EXTERNAL_VF_OWNER")
		}
		if config.MAC != "" && attrs.HardwareAddr.String() != r.VF.Link.MAC && attrs.HardwareAddr.String() != desired.MAC {
			return fmt.Errorf("E_EXTERNAL_VF_MAC")
		}
		quiescent := h
		quiescent.Type = topology.InterfaceType
		if err := n.Preflight(Execution{Roots: map[string]Handle{"vf": quiescent}}); err != nil {
			return err
		}
	}
	// Each setting is idempotent and read back below. Lost acknowledgement may
	// leave a mixture of the before-image and desired values, never fresh targets.
	if current.VlanID != target.VlanID || current.VlanQoS != target.VlanQoS || current.VlanProtocol != target.VlanProtocol {
		if err := n.host.LinkSetVfVlanQosProto(pf, h.VFID, target.VlanID, target.VlanQoS, target.VlanProtocol); err != nil {
			return err
		}
	}
	if current.MAC != target.MAC {
		mac, err := net.ParseMAC(target.MAC)
		if err != nil {
			return err
		}
		if err := n.host.LinkSetVfHardwareAddr(pf, h.VFID, mac); err != nil {
			return err
		}
	}
	if current.MinTxRate != target.MinTxRate || current.MaxTxRate != target.MaxTxRate {
		if err := n.host.LinkSetVfRate(pf, h.VFID, int(target.MinTxRate), int(target.MaxTxRate)); err != nil {
			return err
		}
	}
	if current.SpoofCheck != target.SpoofCheck {
		if err := n.host.LinkSetVfSpoofchk(pf, h.VFID, target.SpoofCheck); err != nil {
			return err
		}
	}
	if current.Trust != target.Trust {
		if err := n.host.LinkSetVfTrust(pf, h.VFID, target.Trust); err != nil {
			return err
		}
	}
	if current.LinkState != target.LinkState {
		if err := n.host.LinkSetVfState(pf, h.VFID, target.LinkState); err != nil {
			return err
		}
	}
	if vfLink != nil && config.MAC != "" {
		mac := desired.MAC
		if restore {
			mac = r.VF.Link.MAC
		}
		if vfLink.Attrs().HardwareAddr.String() != mac {
			address, err := net.ParseMAC(mac)
			if err != nil {
				return err
			}
			if err := n.host.LinkSetDown(vfLink); err != nil {
				return err
			}
			if err := n.host.LinkSetHardwareAddr(vfLink, address); err != nil {
				return err
			}
		}
		if r.VF.Link.Up {
			if err := n.host.LinkSetUp(vfLink); err != nil {
				return err
			}
		} else if err := n.host.LinkSetDown(vfLink); err != nil {
			return err
		}
		observed, err := n.link(h)
		if err != nil {
			return err
		}
		if observed.Attrs().HardwareAddr.String() != mac || (observed.Attrs().Flags&net.FlagUp != 0) != r.VF.Link.Up {
			return fmt.Errorf("E_VF_EFFECTIVE_MAC_POSTCONDITION")
		}
	}
	_, observed, err := n.readVF(h)
	if err != nil {
		return err
	}
	if observed != target {
		return fmt.Errorf("E_VF_POSTCONDITION")
	}
	return nil
}

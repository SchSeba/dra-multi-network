// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

const EswitchProfile = "ethernet-pf-vfs-v1"

type EswitchGrant struct {
	PCI          string   `json:"pci"`
	PolicyUID    string   `json:"policyUID"`
	Profile      string   `json:"profile"`
	AllowedModes []string `json:"allowedModes"`
}

type EswitchFunction struct {
	PCI      string     `json:"pci"`
	Driver   string     `json:"driver"`
	Override string     `json:"override"`
	Group    string     `json:"group,omitempty"`
	Link     *LinkState `json:"link,omitempty"`
}

type EswitchPort struct {
	PortIndex  uint32    `json:"portIndex"`
	Flavor     uint16    `json:"flavor"`
	Controller *uint32   `json:"controller,omitempty"`
	PF         *uint16   `json:"pf,omitempty"`
	VF         *uint16   `json:"vf,omitempty"`
	Link       LinkState `json:"link"`
}

type EswitchSnapshot struct {
	Ports     []EswitchPort                 `json:"ports,omitempty"`
	PCI       string                        `json:"pci"`
	Attrs     netlink.DevlinkDevEswitchAttr `json:"attrs"`
	Functions []EswitchFunction             `json:"functions"`
	VFInfo    []netlink.VfInfo              `json:"vfInfo"`
	VFCount   int                           `json:"vfCount"`
}

// Mode changes can recreate netdevices. Stable PCI/devlink ownership, not old
// ifindexes, is used under the renewed drain fence. No reload/reprobe or reset
// is implicit in this initial Ethernet-only, local single-PF profile.
type EswitchState struct {
	Grant            EswitchGrant    `json:"grant"`
	Before           EswitchSnapshot `json:"before"`
	Target           string          `json:"target"`
	Attempted        bool            `json:"attempted"`
	Completed        bool            `json:"completed"`
	RestoreAttempted bool            `json:"restoreAttempted"`
	Restored         bool            `json:"restored"`
}

func (n *Native) eswitchHandle() (*netlink.Handle, error) {
	return netlink.NewHandleAt(n.hostNS, unix.NETLINK_GENERIC)
}

func (n *Native) eswitchSnapshot(g EswitchGrant) (EswitchSnapshot, error) {
	s := EswitchSnapshot{PCI: g.PCI}
	if !pciAddress.MatchString(g.PCI) || g.PolicyUID == "" || g.Profile != EswitchProfile {
		return s, fmt.Errorf("E_ESWITCH_GRANT")
	}
	base := filepath.Join("/sys/bus/pci/devices", g.PCI)
	owner, err := PCIOwner("/sys", base)
	if err != nil || owner != g.PCI {
		return s, fmt.Errorf("E_ESWITCH_PCI_IDENTITY: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(base, "physfn")); err == nil || !os.IsNotExist(err) {
		return s, fmt.Errorf("E_ESWITCH_PF_REQUIRED")
	}
	rdma, err := DiscoverRDMAInNamespace(n.hostNS)
	if err != nil {
		return s, err
	}
	h, err := n.eswitchHandle()
	if err != nil {
		return s, err
	}
	defer h.Close()
	dev, err := h.DevLinkGetDeviceByName(pciBus, g.PCI)
	if err != nil {
		return s, err
	}
	s.Attrs = dev.Attrs.Eswitch
	if !slices.Contains([]string{"legacy", switchdevMode}, s.Attrs.Mode) || !slices.Contains(g.AllowedModes, s.Attrs.Mode) {
		return s, fmt.Errorf("E_ESWITCH_MODE_OR_GRANT")
	}
	ports, err := h.DevLinkGetAllPortList()
	if err != nil {
		return s, err
	}
	pfLink, err := n.eswitchPFLink(g.PCI, ports)
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(filepath.Join(base, "sriov_numvfs"))
	if err != nil {
		return s, err
	}
	s.VFCount, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || s.VFCount < 0 || s.VFCount > 65535 {
		return s, fmt.Errorf("E_ESWITCH_VF_COUNT")
	}
	functions := []string{g.PCI}
	for id := 0; id < s.VFCount; id++ {
		vf, err := filepath.EvalSymlinks(filepath.Join(base, fmt.Sprintf("virtfn%d", id)))
		if err != nil {
			return s, err
		}
		pci := filepath.Base(vf)
		if _, err := vfParentPCI("/sys", pci, g.PCI, id); err != nil {
			return s, err
		}
		functions = append(functions, pci)
	}
	// Controller siblings and auxiliary/SF/remote scopes need a separate
	// certified profile, not an assumption based on common PCI ancestry.
	devices, err := os.ReadDir("/sys/bus/pci/devices")
	if err != nil {
		return s, err
	}
	for _, device := range devices {
		pci := device.Name()
		if pci != g.PCI && len(pci) == len(g.PCI) && pci[:len(pci)-1] == g.PCI[:len(g.PCI)-1] && !slices.Contains(functions, pci) {
			if _, err := os.Lstat(filepath.Join("/sys/bus/pci/devices", pci, "physfn")); os.IsNotExist(err) {
				return s, fmt.Errorf("E_ESWITCH_CONTROLLER_PROFILE")
			}
		}
	}

	auxiliary, err := os.ReadDir("/sys/bus/auxiliary/devices")
	if err != nil && !os.IsNotExist(err) {
		return s, err
	}
	for _, device := range auxiliary {
		owner, err := PCIOwner("/sys", filepath.Join("/sys/bus/auxiliary/devices", device.Name()))
		if err != nil {
			return s, fmt.Errorf("E_ESWITCH_AUXILIARY_IDENTITY: %w", err)
		}
		if slices.Contains(functions, owner) {
			return s, fmt.Errorf("E_ESWITCH_AUXILIARY_PROFILE")
		}
	}
	for _, pci := range functions {
		if RDMAFunction(pci, rdma) {
			return s, fmt.Errorf("E_ESWITCH_RDMA_PROFILE: registered HCAs require a separately certified complete-domain profile")
		}
		f := EswitchFunction{PCI: pci}
		f.Driver, err = pciDriver(pci)
		if err != nil {
			return s, err
		}
		f.Override, err = driverOverride(pci)
		if err != nil {
			return s, err
		}
		group, groupErr := filepath.EvalSymlinks(filepath.Join("/sys/bus/pci/devices", pci, "iommu_group"))
		if groupErr != nil && !os.IsNotExist(groupErr) {
			return s, groupErr
		}
		if groupErr == nil {
			f.Group = filepath.Base(group)
			peers, err := os.ReadDir(filepath.Join(group, "devices"))
			if err != nil {
				return s, err
			}
			for _, peer := range peers {
				if !slices.Contains(functions, peer.Name()) {
					return s, fmt.Errorf("E_ESWITCH_IOMMU_PEER")
				}
			}
			if err := noDeviceUsers(f.Group); err != nil {
				return s, err
			}
		}
		var link netlink.Link
		if pci == g.PCI {
			link = pfLink
		} else {
			entries, err := os.ReadDir(filepath.Join("/sys/bus/pci/devices", pci, "net"))
			if err != nil && !os.IsNotExist(err) {
				return s, err
			}
			if len(entries) > 1 {
				return s, fmt.Errorf("E_ESWITCH_VF_ENDPOINT_PROFILE")
			}
			if len(entries) == 1 {
				link, err = n.host.LinkByName(entries[0].Name())
				if err != nil {
					return s, err
				}
			}
			if len(entries) == 0 && f.Driver != "" && f.Driver != topology.VFIODriver {
				return s, fmt.Errorf("E_ESWITCH_ENDPOINT_NOT_IN_HOST")
			}
		}
		if link != nil {
			device, err := PCIOwner("/sys", filepath.Join("/sys/class/net", link.Attrs().Name, "device"))
			if err != nil || device != pci {
				return s, fmt.Errorf("E_ESWITCH_ENDPOINT_PCI")
			}
			if err := n.quiescentEswitchLink(link); err != nil {
				return s, err
			}
			before := state(link)
			f.Link = &before
		}
		s.Functions = append(s.Functions, f)
	}
	s.VFInfo = pfLink.Attrs().Vfs
	if len(s.VFInfo) != s.VFCount {
		return s, fmt.Errorf("E_ESWITCH_VF_ADMIN_BEFORE_IMAGE")
	}
	slices.SortFunc(s.VFInfo, func(a, b netlink.VfInfo) int { return a.ID - b.ID })
	for i, vf := range s.VFInfo {
		if vf.ID != i {
			return s, fmt.Errorf("E_ESWITCH_VF_ADMIN_BEFORE_IMAGE")
		}
	}
	for _, port := range ports {
		if port == nil || port.DeviceName != g.PCI {
			continue
		}
		if port.BusName != pciBus || port.PortType != nl.DEVLINK_PORT_TYPE_ETH {
			return s, fmt.Errorf("E_ESWITCH_CONTROLLER_PROFILE")
		}
		if port.PortFlavour == physicalPort && int(port.NetdevIfIndex) != pfLink.Attrs().Index {
			return s, fmt.Errorf("E_ESWITCH_PHYSICAL_PORT_PROFILE")
		}
		if port.PortFlavour != physicalPort && (!localPCIPort(port) || *port.PfNumber != uint16(g.PCI[len(g.PCI)-1]-'0') || port.PortFlavour != pciPFPort && port.PortFlavour != pciVFPort) {
			return s, fmt.Errorf("E_ESWITCH_CONTROLLER_PROFILE")
		}
		if port.PortFlavour == pciVFPort && (port.VfNumber == nil || int(*port.VfNumber) >= s.VFCount) {
			return s, fmt.Errorf("E_ESWITCH_VF_PORT_SCOPE")
		}
		if port.PortFlavour == pciPFPort && int(port.NetdevIfIndex) != pfLink.Attrs().Index {
			return s, fmt.Errorf("E_ESWITCH_PF_PORT_SCOPE")
		}
		l, err := n.host.LinkByIndex(int(port.NetdevIfIndex))
		if err != nil || l.Attrs().Name != port.NetdeviceName {
			return s, fmt.Errorf("E_ESWITCH_PORT_INCARNATION")
		}

		if err := n.quiescentEswitchLink(l); err != nil {
			return s, err
		}
		if port.PortFlavour == pciVFPort {
			// This initial mode profile never destroys customized administrator
			// representors. A wider profile needs explicit port restoration grants.
			assignment, err := os.ReadFile(filepath.Join("/sys/class/net", l.Attrs().Name, "addr_assign_type"))
			if err != nil || strings.TrimSpace(string(assignment)) == "3" || l.Attrs().Alias != "" || l.Attrs().MTU != pfLink.Attrs().MTU {
				return s, fmt.Errorf("E_ESWITCH_CUSTOM_REPRESENTOR_PROFILE")
			}
		}
		s.Ports = append(s.Ports, EswitchPort{PortIndex: port.PortIndex, Flavor: port.PortFlavour, Controller: port.ControllerNumber, PF: port.PfNumber, VF: port.VfNumber, Link: state(l)})
	}
	if s.Attrs.Mode == switchdevMode {
		for id, pci := range functions[1:] {
			identity, err := RepresentorForVF("/sys", ports, pci, g.PCI, id)
			if err != nil || identity == nil {
				return s, fmt.Errorf("E_ESWITCH_REDISCOVERY: %w", err)
			}
		}
	}
	return s, nil
}

func (n *Native) quiescentEswitchLink(l netlink.Link) error {
	if err := n.preflightLinkUsers(l, false, 0); err != nil {
		return err
	}
	if state(l).Up || strings.HasPrefix(l.Attrs().Alias, "dra-mn:") {
		return fmt.Errorf("E_ESWITCH_LINK_NOT_QUIESCENT: links must be down and unowned")
	}
	return nil
}

func (n *Native) eswitchPFLink(pci string, ports []*netlink.DevlinkPort) (netlink.Link, error) {
	entries, err := os.ReadDir(filepath.Join("/sys/bus/pci/devices", pci, "net"))
	if err != nil {
		return nil, err
	}
	if len(entries) == 1 {
		return n.host.LinkByName(entries[0].Name())
	}
	var result netlink.Link
	for _, port := range ports {
		if port == nil || port.BusName != pciBus || port.DeviceName != pci || port.PortFlavour != physicalPort || port.PortType != nl.DEVLINK_PORT_TYPE_ETH {
			continue
		}
		if result != nil {
			return nil, fmt.Errorf("E_ESWITCH_UPLINK_AMBIGUOUS")
		}
		if err := verifyPortNetdevice("/sys", port); err != nil {
			return nil, err
		}
		result, err = n.host.LinkByIndex(int(port.NetdevIfIndex))
		if err != nil {
			return nil, err
		}
	}
	if result == nil {
		return nil, fmt.Errorf("E_ESWITCH_UPLINK_IDENTITY")
	}
	return result, nil
}

func sameEswitchDomain(a, b EswitchSnapshot) bool {
	// A mode change may replace ifindexes; snapshots intentionally do not use
	// them as authority. PCI ownership and endpoint/admin state must match.
	a.Attrs.Mode, b.Attrs.Mode = "", ""
	// Mode-dependent default representors are rediscovered and certified above;
	// their recreated names/port indexes are diagnostic before-images, not authority.
	a.Ports, b.Ports = nil, nil
	return topology.Digest(a) == topology.Digest(b)
}

func issueEswitch(s *EswitchState, current EswitchSnapshot, restore bool, save func() error, set func(string) error) error {
	if s.Grant.Profile != EswitchProfile || s.Grant.PCI != s.Before.PCI || s.Grant.PolicyUID == "" || !slices.Contains(s.Grant.AllowedModes, s.Target) || !slices.Contains(s.Grant.AllowedModes, s.Before.Attrs.Mode) || !slices.Contains([]string{"legacy", switchdevMode}, s.Target) {
		return fmt.Errorf("E_ESWITCH_SAVED_GRANT")
	}
	if !sameEswitchDomain(s.Before, current) {
		return fmt.Errorf("E_ESWITCH_BEFORE_IMAGE_CHANGED")
	}
	mode, attempted, completed := s.Target, &s.Attempted, &s.Completed
	original := s.Before.Attrs.Mode
	if restore {
		mode, original, attempted, completed = s.Before.Attrs.Mode, s.Target, &s.RestoreAttempted, &s.Restored
	}
	if current.Attrs.Mode == mode {
		*completed = true
		return save()
	}
	if current.Attrs.Mode != original {
		return fmt.Errorf("E_ESWITCH_FOREIGN_MODE")
	}
	if *attempted || *completed {
		return fmt.Errorf("E_ESWITCH_UNCERTAIN: never repeat an unacknowledged mode transition")
	}
	*attempted = true
	if err := save(); err != nil {
		return err
	}
	return set(mode) // A fresh snapshot, not the acknowledgement, proves completion.
}

func (n *Native) ReconcileEswitch(s *EswitchState, restore bool, save func() error) error {
	current, err := n.eswitchSnapshot(s.Grant)
	if err != nil {
		return err
	}
	if err := issueEswitch(s, current, restore, save, func(mode string) error {
		h, err := n.eswitchHandle()
		if err != nil {
			return err
		}
		defer h.Close()
		dev, err := h.DevLinkGetDeviceByName(pciBus, s.Grant.PCI)
		if err != nil {
			return err
		}
		return h.DevLinkSetEswitchMode(dev, mode)
	}); err != nil {
		return err
	}
	current, err = n.eswitchSnapshot(s.Grant)
	if err != nil {
		return err
	}
	return issueEswitch(s, current, restore, save, func(string) error { return fmt.Errorf("E_ESWITCH_POSTCONDITION") })
}

func (n *Native) CaptureEswitch(g EswitchGrant, target string) (*EswitchState, error) {
	if !slices.Contains(g.AllowedModes, target) {
		return nil, fmt.Errorf("E_ESWITCH_TARGET_GRANT")
	}
	before, err := n.eswitchSnapshot(g)
	if err != nil {
		return nil, err
	}
	return &EswitchState{Grant: g, Before: before, Target: target}, nil
}

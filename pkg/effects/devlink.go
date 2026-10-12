// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/k8snetworkplumbingwg/sriovnet"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

const pciBus = "pci"
const switchdevMode = "switchdev"
const physicalPort = nl.DEVLINK_PORT_FLAVOUR_PHYSICAL //nolint:misspell // Linux UAPI spelling.

const parentDirectory = ".."
const pciVFPort = nl.DEVLINK_PORT_FLAVOUR_PCI_VF //nolint:misspell // Linux UAPI spelling.
const pciPFPort = nl.DEVLINK_PORT_FLAVOUR_PCI_PF //nolint:misspell // Linux UAPI spelling.

// RepresentorIdentity separates the VF endpoint from its host-side port.
// Names and ifindexes are observations; the devlink tuple and bidirectional
// sysfs VF/PF association establish the mapping. This is not an effect grant.
type RepresentorIdentity struct {
	Bus        string `json:"bus"`
	Device     string `json:"device"`
	PortIndex  uint32 `json:"portIndex"`
	Controller uint32 `json:"controller"`
	PFNumber   uint16 `json:"pfNumber"`
	VFNumber   uint16 `json:"vfNumber"`
	PFPCI      string `json:"pfPCI"`
	VFPCI      string `json:"vfPCI"`
	Name       string `json:"name"`
	IfIndex    uint32 `json:"ifIndex"`
}

// HostDevlinkPorts uses a socket in the caller's retained host namespace.
// Absence of the kernel family is an empty inventory. Query failures of an
// existing family, including interrupted dumps, must never become absence.
func HostDevlinkPorts() ([]*netlink.DevlinkPort, error) {
	h, err := netlink.NewHandle(unix.NETLINK_GENERIC)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	if _, err := h.GenlFamilyGet(nl.GENL_DEVLINK_NAME); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EPROTONOSUPPORT) || errors.Is(err, unix.EAFNOSUPPORT) {
			return nil, nil
		}
		return nil, err
	}
	return h.DevLinkGetAllPortList()
}

// An empty mode is deliberately not switchdev: the netlink helper does not
// surface every optional eswitch-query failure, so absence cannot authorize it.
func HostEswitchMode(bus, device string) (string, error) {
	h, err := netlink.NewHandle(unix.NETLINK_GENERIC)
	if err != nil {
		return "", err
	}
	defer h.Close()
	dev, err := h.DevLinkGetDeviceByName(bus, device)
	if err != nil {
		return "", err
	}
	return dev.Attrs.Eswitch.Mode, nil
}

// DiscoverVFRepresentor reuses sriovnet's device-scoped lookup and checks its
// result against the complete devlink dump and bidirectional PCI association.
// A helper fallback never authorizes a port lacking the explicit devlink tuple.
func DiscoverVFRepresentor(ports []*netlink.DevlinkPort, vfPCI, pfPCI string, vfID int) (*RepresentorIdentity, error) {
	identity, err := RepresentorForVF("/sys", ports, vfPCI, pfPCI, vfID)
	if err != nil || identity == nil {
		return identity, err
	}
	name, err := sriovnet.GetVfRepresentor(pfPCI, vfID)
	if err != nil {
		return nil, err
	}
	if name != identity.Name {
		return nil, fmt.Errorf("E_REPRESENTOR_LOOKUP_CHANGED")
	}
	return identity, nil
}

// RepresentorForVF requires an explicit local PCI devlink device and port
// tuple. Remote controllers, SFs and interface-name conventions are not proof.
func RepresentorForVF(sysRoot string, ports []*netlink.DevlinkPort, vfPCI, pfPCI string, vfID int) (*RepresentorIdentity, error) {
	if !pciAddress.MatchString(vfPCI) || !pciAddress.MatchString(pfPCI) || vfPCI == pfPCI || vfID < 0 || vfID > 65535 {
		return nil, fmt.Errorf("E_REPRESENTOR_ALLOCATION_IDENTITY")
	}
	var result *RepresentorIdentity
	for _, port := range ports {
		if port == nil || port.PortFlavour != pciVFPort || port.DeviceName != pfPCI || port.VfNumber == nil || int(*port.VfNumber) != vfID || !localPCIPort(port) || *port.PfNumber != uint16(pfPCI[len(pfPCI)-1]-'0') {
			continue
		}
		if result != nil {
			return nil, fmt.Errorf("E_REPRESENTOR_AMBIGUOUS: duplicate VF port")
		}
		for _, other := range ports {
			if other != nil && other != port && (other.NetdeviceName == port.NetdeviceName || other.NetdevIfIndex == port.NetdevIfIndex || other.BusName == port.BusName && other.DeviceName == port.DeviceName && other.PortIndex == port.PortIndex) {
				return nil, fmt.Errorf("E_REPRESENTOR_AMBIGUOUS: shared netdevice or port identity")
			}
		}
		if err := verifyPortNetdevice(sysRoot, port); err != nil {
			return nil, err
		}
		if _, err := vfParentPCI(sysRoot, vfPCI, pfPCI, vfID); err != nil {
			return nil, err
		}
		result = &RepresentorIdentity{Bus: port.BusName, Device: port.DeviceName, PortIndex: port.PortIndex, Controller: *port.ControllerNumber, PFNumber: *port.PfNumber, VFNumber: *port.VfNumber, PFPCI: pfPCI, VFPCI: vfPCI, Name: port.NetdeviceName, IfIndex: port.NetdevIfIndex}
	}
	return result, nil
}

func localPCIPort(p *netlink.DevlinkPort) bool {
	return p.BusName == pciBus && pciAddress.MatchString(p.DeviceName) && p.PortType == nl.DEVLINK_PORT_TYPE_ETH && p.PfNumber != nil && p.ControllerNumber != nil && *p.ControllerNumber == 0 && p.External != nil && !*p.External && validNetdeviceName(p.NetdeviceName) && p.NetdevIfIndex != 0 && p.NetdevIfIndex <= math.MaxInt32
}

func validNetdeviceName(name string) bool {
	return name != "" && name != "." && name != parentDirectory && len(name) <= 15 && !strings.ContainsAny(name, "/\x00\n\r\t ")
}

func verifyPortNetdevice(sysRoot string, p *netlink.DevlinkPort) error {
	base := filepath.Join(sysRoot, "class/net", p.NetdeviceName)
	for field, expected := range map[string]uint32{"ifindex": p.NetdevIfIndex, "type": 1} {
		data, err := os.ReadFile(filepath.Join(base, field))
		if err != nil {
			return err
		}
		n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
		if err != nil || uint32(n) != expected {
			return fmt.Errorf("E_REPRESENTOR_NETDEVICE: %s %s", p.NetdeviceName, field)
		}
	}
	return nil
}

// DevlinkPortOwners returns all observed local PF owners on a controller,
// including the eswitch device. Management protection must cover the controller
// even when a representor has no PCI device symlink of its own.
func DevlinkPortOwners(sysRoot string, ports []*netlink.DevlinkPort, name string, index int) ([]string, error) {
	if index <= 0 || index > math.MaxInt32 {
		return nil, fmt.Errorf("E_DEVLINK_NETDEVICE_IDENTITY")
	}
	owners := map[string]bool{}
	for _, port := range ports {
		if port == nil || port.NetdeviceName != name && port.NetdevIfIndex != uint32(index) {
			continue
		}
		if port.NetdeviceName != name || port.NetdevIfIndex != uint32(index) {
			return nil, fmt.Errorf("E_DEVLINK_NETDEVICE_IDENTITY: protected %s", name)
		}
		if port.BusName != pciBus || !pciAddress.MatchString(port.DeviceName) {
			return nil, fmt.Errorf("E_DEVLINK_CONTROLLER_SCOPE: protected %s", name)
		}
		owner, err := PCIOwner(sysRoot, filepath.Join(sysRoot, "bus/pci/devices", port.DeviceName))
		if err != nil || owner != port.DeviceName {
			return nil, fmt.Errorf("E_DEVLINK_CONTROLLER_IDENTITY: protected %s: %w", name, err)
		}
		owners[owner] = true
		for _, pf := range ports {
			if pf == nil || pf.BusName != port.BusName || pf.DeviceName != port.DeviceName || pf.PortFlavour != pciPFPort {
				continue
			}
			if !localPCIPort(pf) {
				return nil, fmt.Errorf("E_DEVLINK_CONTROLLER_SCOPE: incomplete local PF identity")
			}
			if err := verifyPortNetdevice(sysRoot, pf); err != nil {
				return nil, err
			}
			owner, err := PCIOwner(sysRoot, filepath.Join(sysRoot, "class/net", pf.NetdeviceName, "device"))
			if err != nil || owner == "" {
				return nil, fmt.Errorf("E_DEVLINK_CONTROLLER_IDENTITY: %w", err)
			}
			owners[owner] = true
		}
	}
	var result []string
	for owner := range owners {
		result = append(result, owner)
	}
	slices.Sort(result)
	return result, nil
}

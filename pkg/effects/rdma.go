// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

const rdmaVerbsClass = "infiniband_verbs"
const rdmaMADClass = "infiniband_mad"
const rdmaExclusiveMode = "exclusive"

// RDMADevice describes observations, not a grant to move or expose the HCA.
// PCI scope and namespace isolation must be verified separately at allocation.
type RDMADevice struct {
	Index           uint32             `json:"index,omitempty"`
	Namespace       *NamespaceIdentity `json:"namespace,omitempty"`
	NamespaceMode   string             `json:"namespaceMode,omitempty"`
	Name            string             `json:"name"`
	PCI             string             `json:"pci"`
	NodeGUID        string             `json:"nodeGUID"`
	SystemImageGUID string             `json:"systemImageGUID"`
	Ports           []RDMAPort         `json:"ports"`
	NetDevices      []string           `json:"netDevices,omitempty"`
	RelatedPCI      []string           `json:"relatedPCI"`
	CharDevices     []RDMAChar         `json:"charDevices,omitempty"`
}

type RDMAPort struct {
	Number     uint32            `json:"number"`
	LinkLayer  string            `json:"linkLayer"`
	State      string            `json:"state"`
	PKeys      map[uint32]uint16 `json:"pkeys"`
	NetDevices []string          `json:"netDevices,omitempty"`
}

type RDMAChar struct {
	Name  string `json:"name"`
	Class string `json:"class"`
	Major uint32 `json:"major"`
	Minor uint32 `json:"minor"`
	Port  uint32 `json:"port,omitempty"`
}

func DiscoverRDMA() ([]RDMADevice, error) {
	ns, err := netns.Get()
	if err != nil {
		return nil, err
	}
	defer ns.Close()
	return DiscoverRDMAInNamespace(ns)
}

// Use an explicitly scoped RDMA socket and a fresh sysfs mount. The caller's
// /sys can belong to another network namespace after an HCA handoff.
func DiscoverRDMAInNamespace(ns netns.NsHandle) ([]RDMADevice, error) {
	return discoverRDMAInNamespace(ns, nil)
}

func discoverRDMAInNamespace(ns netns.NsHandle, ownedNetdevices map[string]string) ([]RDMADevice, error) {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(ns), &fs); err != nil {
		return nil, err
	}
	if fs.Type != unix.NSFS_MAGIC {
		return nil, fmt.Errorf("E_NAMESPACE_TYPE: not a namespace filesystem")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(ns), &stat); err != nil {
		return nil, err
	}
	h, err := netlink.NewHandleAt(ns, unix.NETLINK_RDMA)
	if err != nil {
		// No RDMA subsystem is a valid empty inventory, a failed query of an
		// existing subsystem is not. Do not mask permission or I/O failures.
		if errors.Is(err, unix.EPROTONOSUPPORT) || errors.Is(err, unix.EAFNOSUPPORT) {
			entries, sysErr := os.ReadDir("/sys/class/infiniband")
			if os.IsNotExist(sysErr) || sysErr == nil && len(entries) == 0 {
				return nil, nil
			}
			return nil, errors.Join(err, sysErr)
		}
		return nil, err
	}
	defer h.Close()
	if err := h.SetSocketTimeout(time.Second); err != nil {
		return nil, err
	}
	links, err := h.RdmaLinkList()
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, nil
	}
	mode, err := h.RdmaSystemGetNetnsMode()
	if err != nil {
		return nil, err
	}
	var devices []RDMADevice
	err = withRDMASysfs(ns, func(root string) error {
		for _, link := range links {
			if link == nil || link.Attrs.Name != filepath.Base(link.Attrs.Name) || link.Attrs.Name == "." || link.Attrs.Name == ".." {
				return fmt.Errorf("E_RDMA_NETLINK_IDENTITY")
			}
			// Do not turn an HCA absent from this sysfs view into software RDMA
			// or an empty physical inventory. Software devices have a real
			// device path whose ancestors contain no PCI function.
			if _, err := PCIOwner(root, filepath.Join(root, "class/infiniband", link.Attrs.Name, "device")); err != nil {
				return fmt.Errorf("E_RDMA_NAMESPACE_IDENTITY: %s: %w", link.Attrs.Name, err)
			}
		}
		var err error
		if len(ownedNetdevices) == 0 {
			devices, err = discoverRDMA(root)
		} else {
			devices, err = discoverRDMAWithNetdevices(root, ownedNetdevices)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	for i := range devices {
		devices[i].NamespaceMode = mode
	}
	identity := NamespaceIdentity{Device: stat.Dev, Inode: stat.Ino}
	if err := matchRDMALinks(devices, links, identity); err != nil {
		return nil, err
	}
	return devices, nil
}

// A short-lived locked thread owns its private mount namespace. Exiting it
// restores neither namespace onto another Go goroutine and leaves no mounts.
func withRDMASysfs(ns netns.NsHandle, read func(string) error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		done <- func() (result error) {
			if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
				return err
			}
			if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
				return err
			}
			if err := netns.Set(ns); err != nil {
				return err
			}
			root, err := os.MkdirTemp("", "dra-mn-rdma-sysfs-")
			if err != nil {
				return err
			}
			defer func() { result = errors.Join(result, os.Remove(root)) }()
			if err := unix.Mount("sysfs", root, "sysfs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
				return err
			}
			defer func() { result = errors.Join(result, unix.Unmount(root, 0)) }()
			return read(root)
		}()
	}()
	return <-done
}

func matchRDMALinks(devices []RDMADevice, links []*netlink.RdmaLink, ns NamespaceIdentity) error {
	byName := map[string]*netlink.RdmaLink{}
	indexes := map[uint32]bool{}
	for _, link := range links {
		if link == nil || link.Attrs.Name == "" || byName[link.Attrs.Name] != nil || indexes[link.Attrs.Index] {
			return fmt.Errorf("E_RDMA_NETLINK_IDENTITY")
		}
		byName[link.Attrs.Name], indexes[link.Attrs.Index] = link, true
	}
	for i := range devices {
		d := &devices[i]
		link := byName[d.Name]
		if link == nil {
			return fmt.Errorf("E_RDMA_NAMESPACE_IDENTITY: %s", d.Name)
		}
		// netlink's GUID formatter writes the native uint64 in little-endian
		// order; sysfs displays the canonical big-endian GUID.
		node, nodeErr := netlinkRDMAGUID(link.Attrs.NodeGuid)
		system, systemErr := netlinkRDMAGUID(link.Attrs.SysImageGuid)
		if nodeErr != nil || systemErr != nil || node != d.NodeGUID || system != d.SystemImageGUID || int(link.Attrs.NumPorts) != len(d.Ports) {
			return fmt.Errorf("E_RDMA_NETLINK_IDENTITY: %s", d.Name)
		}
		d.Index, d.Namespace = link.Attrs.Index, &ns
	}
	return nil
}

func netlinkRDMAGUID(value string) (string, error) {
	data, err := hex.DecodeString(strings.ReplaceAll(value, ":", ""))
	if err != nil || len(data) != 8 {
		return "", fmt.Errorf("E_RDMA_NETLINK_GUID")
	}
	slices.Reverse(data)
	return hex.EncodeToString(data), nil
}

// RDMAFunction includes observed cross-function netdevice dependencies. A
// missing direct HCA owner on a function does not make that function isolated.
func RDMAFunction(pci string, devices []RDMADevice) bool {
	for _, device := range devices {
		if device.PCI == pci || slices.Contains(device.RelatedPCI, pci) {
			return true
		}
	}
	return false
}

func pciRDMAScope(pci string, devices []RDMADevice) error {
	if RDMAFunction(pci, devices) {
		return fmt.Errorf("E_PCI_RDMA_SCOPE: exclusive mutation requires a certified complete HCA contract")
	}
	return nil
}

func ethernetScope(h Handle) error {
	if h.RDMA != nil {
		// Only the recorded namespace effect delegates its exclusive Linux
		// interface. Public configs never construct allocation handles.
		return validateRoCEGrant(h, topology.RoCESinglePort)
	}
	devices, err := DiscoverRDMA()
	if err != nil {
		return err
	}
	return pciRDMAScope(h.PCIAddress, devices)
}

func sysText(path string) (string, error) {
	data, err := os.ReadFile(path)
	return strings.TrimSpace(string(data)), err
}

func rdmaGUID(path string) (string, error) {
	value, err := sysText(path)
	if err != nil {
		return "", err
	}
	value = strings.ToLower(strings.ReplaceAll(value, ":", ""))
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 8 {
		return "", fmt.Errorf("E_RDMA_GUID: %s", path)
	}
	return value, nil
}

// PCIOwner verifies the closest physical function, including auxiliary devices.
// A software device has no PCI owner; a failed query is returned as an error.
func PCIOwner(sysRoot, devicePath string) (string, error) {
	path, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return "", err
	}
	for current := path; current != filepath.Dir(current); current = filepath.Dir(current) {
		pci := filepath.Base(current)
		if !pciAddress.MatchString(pci) {
			continue
		}
		canonical, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "bus/pci/devices", pci))
		if err != nil || canonical != current {
			return "", fmt.Errorf("E_RDMA_PCI_IDENTITY: %s: %w", devicePath, err)
		}
		return pci, nil
	}
	return "", nil
}

func discoverRDMA(sysRoot string) ([]RDMADevice, error) {
	return discoverRDMAWithNetdevices(sysRoot, nil)
}

func discoverRDMAWithNetdevices(sysRoot string, ownedNetdevices map[string]string) ([]RDMADevice, error) {
	entries, err := os.ReadDir(filepath.Join(sysRoot, "class/infiniband"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var devices []RDMADevice
	for _, entry := range entries {
		path := filepath.Join(sysRoot, "class/infiniband", entry.Name())
		pci, err := PCIOwner(sysRoot, filepath.Join(path, "device"))
		if err != nil {
			return nil, fmt.Errorf("E_RDMA_DISCOVERY: %s: %w", entry.Name(), err)
		}
		if pci == "" { // Physical allocation profiles do not expose software RDMA.
			continue
		}
		device := RDMADevice{Name: entry.Name(), PCI: pci, RelatedPCI: []string{pci}}
		if device.NodeGUID, err = rdmaGUID(filepath.Join(path, "node_guid")); err != nil {
			return nil, err
		}
		if device.SystemImageGUID, err = rdmaGUID(filepath.Join(path, "sys_image_guid")); err != nil {
			return nil, err
		}
		if err := discoverRDMAPorts(path, &device); err != nil {
			return nil, err
		}
		if err := discoverRDMANetDevices(sysRoot, &device, ownedNetdevices); err != nil {
			return nil, err
		}
		if err := discoverRDMAChars(sysRoot, &device); err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func discoverRDMAPorts(path string, device *RDMADevice) error {
	ports, err := os.ReadDir(filepath.Join(path, "ports"))
	if err != nil || len(ports) == 0 {
		return fmt.Errorf("E_RDMA_PORTS: %s: %w", device.Name, err)
	}
	for _, entry := range ports {
		number, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || number == 0 {
			return fmt.Errorf("E_RDMA_PORT_ID: %s", entry.Name())
		}
		portPath := filepath.Join(path, "ports", entry.Name())
		port := RDMAPort{Number: uint32(number), PKeys: map[uint32]uint16{}}
		layer, err := sysText(filepath.Join(portPath, "link_layer"))
		if err != nil || (layer != "Ethernet" && layer != "InfiniBand") {
			return fmt.Errorf("E_RDMA_LINK_LAYER: %s: %w", layer, err)
		}
		port.LinkLayer = strings.ToLower(layer)
		if port.State, err = sysText(filepath.Join(portPath, "state")); err != nil || port.State == "" {
			return fmt.Errorf("E_RDMA_PORT_STATE: %s: %w", portPath, err)
		}
		keys, err := os.ReadDir(filepath.Join(portPath, "pkeys"))
		if err != nil {
			return err
		}
		for _, key := range keys {
			index, err := strconv.ParseUint(key.Name(), 10, 32)
			if err != nil {
				return fmt.Errorf("E_RDMA_PKEY_INDEX: %w", err)
			}
			value, err := sysText(filepath.Join(portPath, "pkeys", key.Name()))
			if err != nil {
				return err
			}
			n, err := strconv.ParseUint(value, 0, 16)
			if err != nil {
				return fmt.Errorf("E_RDMA_PKEY: %w", err)
			}
			port.PKeys[uint32(index)] = uint16(n)
		}
		ndevPath := filepath.Join(portPath, "gid_attrs/ndevs")
		ndevs, err := os.ReadDir(ndevPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, ndev := range ndevs {
			if _, err := strconv.ParseUint(ndev.Name(), 10, 32); err != nil {
				return fmt.Errorf("E_RDMA_GID_INDEX: %w", err)
			}
			name, err := sysText(filepath.Join(ndevPath, ndev.Name()))
			// Kernel sysfs returns EINVAL for an empty/unassociated GID slot.
			if errors.Is(err, unix.EINVAL) {
				continue
			}
			if err != nil {
				return err
			}
			if name != "" {
				port.NetDevices = append(port.NetDevices, name)
			}
		}
		slices.Sort(port.NetDevices)
		port.NetDevices = slices.Compact(port.NetDevices)
		device.NetDevices = append(device.NetDevices, port.NetDevices...)
		device.Ports = append(device.Ports, port)
	}
	slices.SortFunc(device.Ports, func(a, b RDMAPort) int { return int(a.Number) - int(b.Number) })
	return nil
}

func discoverRDMANetDevices(sysRoot string, device *RDMADevice, ownedNetdevices map[string]string) error {
	entries, err := os.ReadDir(filepath.Join(sysRoot, "class/net"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(sysRoot, "class/net", entry.Name(), "device")
		pci, err := PCIOwner(sysRoot, path)
		if os.IsNotExist(err) { // Virtual links have no device symlink.
			continue
		}
		if err != nil {
			return err
		}
		if pci == device.PCI {
			device.NetDevices = append(device.NetDevices, entry.Name())
		}
	}
	slices.Sort(device.NetDevices)
	device.NetDevices = slices.Compact(device.NetDevices)
	for _, name := range device.NetDevices {
		if name != filepath.Base(name) || name == "." || name == ".." || len(name) > 15 {
			return fmt.Errorf("E_RDMA_NETDEVICE: invalid name %q", name)
		}
		pci, err := PCIOwner(sysRoot, filepath.Join(sysRoot, "class/net", name, "device"))
		if os.IsNotExist(err) && pciAddress.MatchString(ownedNetdevices[name]) {
			// During a recorded handoff a GID can still refer to the owned
			// interface in another retained namespace. Its PCI identity comes
			// only from the runner's verified alias/ifindex, never a name lookup.
			pci, err = ownedNetdevices[name], nil
		}
		if err != nil || pci == "" {
			return fmt.Errorf("E_RDMA_NETDEVICE_OWNER: %s: %w", name, err)
		}
		device.RelatedPCI = append(device.RelatedPCI, pci)
	}
	slices.Sort(device.RelatedPCI)
	device.RelatedPCI = slices.Compact(device.RelatedPCI)
	return nil
}

func discoverRDMAChars(sysRoot string, device *RDMADevice) error {
	for _, class := range []string{rdmaVerbsClass, rdmaMADClass, "infiniband_cm"} {
		entries, err := os.ReadDir(filepath.Join(sysRoot, "class", class))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(sysRoot, "class", class, entry.Name())
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink == 0 && !info.IsDir() { // e.g. abi_version.
				continue
			}
			if class == "infiniband_cm" && entry.Name() == "rdma_cm" { // Common control node is not private HCA access.
				continue
			}
			owner, err := sysText(filepath.Join(path, "ibdev"))
			if err != nil {
				return err
			}
			if owner != device.Name {
				continue
			}
			// Character classes are not a namespace ownership authority. Two
			// HCAs in different namespaces can share a name; verify the physical
			// parent, including the auxiliary device rather than just its BDF.
			parent, err := filepath.EvalSymlinks(filepath.Join(path, "device"))
			if err != nil {
				return err
			}
			hcaParent, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "class/infiniband", device.Name, "device"))
			if err != nil || parent != hcaParent {
				return fmt.Errorf("E_RDMA_CHAR_OWNER: %s: %w", path, err)
			}
			value, err := sysText(filepath.Join(path, "dev"))
			if err != nil {
				return err
			}
			majorText, minorText, ok := strings.Cut(value, ":")
			major, majorErr := strconv.ParseUint(majorText, 10, 32)
			minor, minorErr := strconv.ParseUint(minorText, 10, 32)
			if !ok || majorErr != nil || minorErr != nil || major == 0 {
				return fmt.Errorf("E_RDMA_CHAR_IDENTITY: %s", path)
			}
			canonical, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			byNumber, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "dev/char", value))
			if err != nil || byNumber != canonical {
				return fmt.Errorf("E_RDMA_CHAR_IDENTITY: %s: %w", path, err)
			}
			char := RDMAChar{Name: entry.Name(), Class: class, Major: uint32(major), Minor: uint32(minor)}
			if class == rdmaMADClass {
				value, err := sysText(filepath.Join(path, "port"))
				if err != nil {
					return err
				}
				number, err := strconv.ParseUint(value, 10, 32)
				if err != nil || !slices.ContainsFunc(device.Ports, func(port RDMAPort) bool { return uint64(port.Number) == number }) {
					return fmt.Errorf("E_RDMA_CHAR_PORT: %s", path)
				}
				char.Port = uint32(number)
			}
			device.CharDevices = append(device.CharDevices, char)
		}
	}
	return nil
}

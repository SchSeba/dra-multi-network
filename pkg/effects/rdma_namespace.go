// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
// Namespace transport follows dranet/pkg/driver/rdmadevice.go; identity,
// quiescence, client rediscovery and recovery are owned by this runner.
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
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type RDMAState struct {
	Before           RDMADevice  `json:"before"`
	Driver           string      `json:"driver"`
	Override         string      `json:"override"`
	Vendor           string      `json:"vendor"`
	Device           string      `json:"device"`
	Kernel           Record      `json:"kernel"`
	Connected        bool        `json:"connected"`
	RestoreAttempted bool        `json:"restoreAttempted"`
	RestoredDevice   *RDMADevice `json:"restoredDevice,omitempty"`
	Profile          string      `json:"profile,omitempty"`
	MADAccess        bool        `json:"madAccess,omitempty"`
	IPoIB            *IPoIBState `json:"ipoib,omitempty"`
	IPoIBRestore     *Handle     `json:"ipoibRestore,omitempty"`
}

func validateRoCEGrant(h Handle, profile string) error {
	if profile != topology.RoCESinglePort || !slices.Contains(h.AllowedRDMAProfiles, profile) || h.Shared || h.RDMA == nil || h.PCIAddress != h.RDMA.PCI || !pciAddress.MatchString(h.PCIAddress) || (h.Type != topology.RDMAType && h.Type != topology.InterfaceType) {
		return fmt.Errorf("E_RDMA_GRANT: certified exclusive RoCE allocation required")
	}
	d := h.RDMA
	if d.Namespace == nil || d.NodeGUID == "" || d.SystemImageGUID == "" || len(d.RelatedPCI) != 1 || d.RelatedPCI[0] != h.PCIAddress || len(d.Ports) != 1 || d.Ports[0].Number != 1 || d.Ports[0].LinkLayer != topology.Ethernet || len(d.NetDevices) != 1 || d.NetDevices[0] != h.Name {
		return fmt.Errorf("E_RDMA_SCOPE: require one complete function, one Ethernet port and one kernel interface")
	}
	return nil
}

func sameRDMAIdentity(a, b RDMADevice) bool {
	if a.Index != b.Index || a.Name != b.Name || a.PCI != b.PCI || a.NodeGUID != b.NodeGUID || a.SystemImageGUID != b.SystemImageGUID || !slices.Equal(a.RelatedPCI, b.RelatedPCI) || len(a.Ports) != len(b.Ports) {
		return false
	}
	for i := range a.Ports {
		if a.Ports[i].Number != b.Ports[i].Number || a.Ports[i].LinkLayer != b.Ports[i].LinkLayer || topology.Digest(a.Ports[i].PKeys) != topology.Digest(b.Ports[i].PKeys) {
			return false
		}
	}
	return true
}

// A missing direct owner is not enough: any observed related function also
// participates in this namespace operation's exclusive hardware scope.
func completeRoCEFunction(devices []RDMADevice, expected RDMADevice) error {
	owners := 0
	for _, d := range devices {
		if d.PCI == expected.PCI || slices.Contains(d.RelatedPCI, expected.PCI) {
			owners++
			if !sameRDMAIdentity(d, expected) {
				return fmt.Errorf("E_RDMA_SCOPE: HCA identity or dependency scope changed")
			}
		}
	}
	if owners != 1 {
		return fmt.Errorf("E_RDMA_SCOPE: require the complete single-HCA function")
	}
	return nil
}

func (n *Native) captureRDMAAllocation(h Handle, c topology.Config) (*RDMAState, error) {
	if h.Type != topology.RDMAType || h.Scope != HostScope {
		return nil, fmt.Errorf("E_RDMA_GRANT: namespace handoff needs a host allocation and retained sandbox")
	}
	if err := validateRDMAConfigGrant(h, c); err != nil {
		return nil, err
	}
	devices, err := DiscoverRDMAInNamespace(n.hostNS)
	if err != nil {
		return nil, err
	}
	if err := completeRoCEFunction(devices, *h.RDMA); err != nil {
		return nil, err
	}
	current, err := rdmaIdentityIn(n.hostNS, *h.RDMA)
	if err != nil || current == nil || current.Namespace == nil || *current.Namespace != *h.RDMA.Namespace {
		return nil, errors.Join(fmt.Errorf("E_RDMA_ALLOCATION_IDENTITY"), err)
	}
	if !slices.Equal(current.NetDevices, h.RDMA.NetDevices) {
		return nil, fmt.Errorf("E_RDMA_NETDEVICE_IDENTITY")
	}
	if err := noEnabledVFs("/sys", h.PCIAddress); err != nil {
		return nil, err
	}
	result := &RDMAState{Before: *current, Profile: c.RDMAProfile, MADAccess: c.MADAccess}
	if c.RDMAProfile == topology.IPoIBSinglePort {
		result.IPoIB, err = captureIPoIB(n.hostNS, n.host, *current)
		if err != nil {
			return nil, err
		}
		if result.IPoIB.Link.Alias != "" {
			return nil, fmt.Errorf("E_IPOIB_OWNER: interface already owned")
		}
		if err := result.IPoIB.validateMTU(c.MTU); err != nil {
			return nil, err
		}
		link, err := n.host.LinkByName(result.IPoIB.Link.Name)
		if err != nil {
			return nil, err
		}
		if err := n.preflightLinkUsers(link, false, 0); err != nil {
			return nil, err
		}
	} else if c.RDMAProfile == topology.RoCESinglePort {
		link, err := n.link(h)
		if err != nil {
			return nil, err
		}
		before := state(link)
		if link.Type() != kernelDeviceType || before.Alias != "" || before.MasterIndex != 0 {
			return nil, fmt.Errorf("E_RDMA_KERNEL_OWNER: require an unowned PCI Ethernet interface")
		}
		result.Kernel.Before = &before
		if err := n.preflightHostLink(h); err != nil {
			return nil, err
		}
	}
	if result.Driver, err = pciDriver(h.PCIAddress); err != nil || !slices.Contains(h.AllowedDrivers, result.Driver) {
		return nil, errors.Join(fmt.Errorf("E_RDMA_DRIVER_GRANT"), err)
	}
	if result.Override, err = driverOverride(h.PCIAddress); err != nil || result.Override != "" && !slices.Contains(h.AllowedDrivers, result.Override) {
		return nil, errors.Join(fmt.Errorf("E_RDMA_OVERRIDE_GRANT"), err)
	}
	if result.Vendor, err = readPCI(h.PCIAddress, pciVendorAttribute); err != nil {
		return nil, err
	}
	if result.Device, err = readPCI(h.PCIAddress, pciDeviceAttribute); err != nil {
		return nil, err
	}
	if _, err := rdmaProfileAccessNodes(result.Before, c.MADAccess); err != nil {
		return nil, err
	}
	if err := noRDMAUsers(n.hostNS, result.Before); err != nil {
		return nil, err
	}
	return result, nil
}

func (n *Native) captureRDMA(h Handle, c topology.Config) (*RDMAState, error) {
	if n.sandbox == nil {
		return nil, fmt.Errorf("E_RDMA_GRANT: retained sandbox required")
	}
	result, err := n.captureRDMAAllocation(h, c)
	if err != nil {
		return nil, err
	}
	if err := emptyRDMANamespace(n.sandboxNS); err != nil {
		return nil, err
	}
	return result, nil
}

// RoCEAllocationScope is the same complete-HCA certificate used by publication
// and capture. Observations never authorize a partial port/function allocation.
func RoCEAllocationScope(h Handle, devices []RDMADevice) error {
	return RDMAAllocationScope(h, devices, topology.RoCESinglePort)
}

func RDMAAllocationScope(h Handle, devices []RDMADevice, profile string) error {
	if err := validateRDMAConfigGrant(h, topology.Config{RDMAProfile: profile}); err != nil {
		return err
	}
	if h.RDMA.NamespaceMode != rdmaExclusiveMode {
		return fmt.Errorf("E_RDMA_NAMESPACE_MODE: exclusive mode required")
	}
	verbs := 0
	for _, node := range h.RDMA.CharDevices {
		if node.Class == rdmaVerbsClass {
			verbs++
		}
	}
	if verbs != 1 {
		return fmt.Errorf("E_RDMA_ACCESS_SCOPE: exactly one private verbs node required")
	}
	if h.AllowedMADAccess {
		if _, err := rdmaMADChar(*h.RDMA); err != nil {
			return err
		}
	}
	return completeRoCEFunction(devices, *h.RDMA)
}

// CheckRoCEAllocation performs preparation's read-only hardware checks. The
// mutation's full before-image is captured again and journaled at Connect.
func CheckRoCEAllocation(h Handle) error {
	return CheckRDMAAllocation(h, topology.Config{RDMAProfile: topology.RoCESinglePort})
}

func CheckRDMAAllocation(h Handle, config topology.Config) error {
	n, err := OpenHostNative()
	if err != nil {
		return err
	}
	defer n.Close()
	captured, err := n.captureRDMAAllocation(h, config)
	if err != nil {
		return err
	}
	_, err = rdmaProfileAccessNodes(captured.Before, config.MADAccess)
	return err
}

func rdmaIdentityIn(ns netns.NsHandle, expected RDMADevice) (*RDMADevice, error) {
	return rdmaIdentityWithNetdevices(ns, expected, nil)
}

func rdmaIdentityWithNetdevices(ns netns.NsHandle, expected RDMADevice, ownedNetdevices map[string]string) (*RDMADevice, error) {
	devices, err := discoverRDMAInNamespace(ns, ownedNetdevices)
	if err != nil {
		return nil, err
	}
	var found *RDMADevice
	for i := range devices {
		d := &devices[i]
		if d.PCI != expected.PCI && d.Name != expected.Name && d.Index != expected.Index {
			continue
		}
		if found != nil || !sameRDMAIdentity(*d, expected) {
			return nil, fmt.Errorf("E_RDMA_INCARNATION: conflicting HCA locator")
		}
		found = d
	}
	if found != nil {
		if err := completeRoCEFunction(devices, expected); err != nil {
			return nil, err
		}
	}
	return found, nil
}

func (n *Native) rdmaLocation(ns netns.NsHandle, s RDMAState) (*RDMADevice, error) {
	if topology.IBProfile(s.Profile) {
		return rdmaIdentityIn(ns, s.Before)
	}
	link, _, err := n.owned(s.Kernel.Output.Alias)
	if err != nil {
		return nil, err
	}
	owned := map[string]string{}
	if link != nil {
		if link.Type() != kernelDeviceType || link.Attrs().MasterIndex != 0 || s.Kernel.Output.IfIndex != 0 && s.Kernel.Output.IfIndex != link.Attrs().Index {
			return nil, fmt.Errorf("E_RDMA_KERNEL_INCARNATION")
		}
		owned[link.Attrs().Name] = s.Before.PCI
	}
	return rdmaIdentityWithNetdevices(ns, s.Before, owned)
}

func rdmaSocket(ns netns.NsHandle) (*netlink.Handle, error) {
	h, err := netlink.NewHandleAt(ns, unix.NETLINK_RDMA)
	if err != nil {
		return nil, err
	}
	if err := h.SetSocketTimeout(time.Second); err != nil {
		return nil, errors.Join(err, h.Close())
	}
	return h, nil
}

func emptyRDMANamespace(ns netns.NsHandle) error {
	links, err := exclusiveRDMALinks(ns)
	if err != nil {
		return err
	}
	if len(links) != 0 {
		return fmt.Errorf("E_RDMA_NAMESPACE_SCOPE: sandbox already contains an HCA")
	}
	return nil
}

func exclusiveRDMALinks(ns netns.NsHandle) ([]*netlink.RdmaLink, error) {
	h, err := rdmaSocket(ns)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	mode, err := h.RdmaSystemGetNetnsMode()
	if err != nil || mode != rdmaExclusiveMode {
		return nil, errors.Join(fmt.Errorf("E_RDMA_NAMESPACE_MODE: exclusive mode required; it is never changed automatically"), err)
	}
	links, err := h.RdmaLinkList()
	if err != nil {
		return nil, err
	}
	return links, nil
}

func rdmaQuiescent(resources []*netlink.RdmaResource, expected RDMADevice) error {
	var found *netlink.RdmaResource
	for _, resource := range resources {
		if resource == nil {
			return fmt.Errorf("E_RDMA_QUIESCENCE: missing resource observation")
		}
		if resource.Index == expected.Index || resource.Name == expected.Name {
			if found != nil || resource.Index != expected.Index || resource.Name != expected.Name {
				return fmt.Errorf("E_RDMA_QUIESCENCE: ambiguous resource identity")
			}
			found = resource
		}
	}
	if found == nil || len(found.RdmaResourceSummaryEntries) == 0 {
		return fmt.Errorf("E_RDMA_QUIESCENCE: unavailable tracking is unknown")
	}
	for _, key := range requiredRDMAResources {
		if _, known := found.RdmaResourceSummaryEntries[key]; !known {
			return fmt.Errorf("E_RDMA_QUIESCENCE: missing %s tracking", key)
		}
	}
	for key, count := range found.RdmaResourceSummaryEntries {
		if count != 0 {
			return fmt.Errorf("E_RDMA_ACTIVE: %s=%d", key, count)
		}
	}
	return nil
}

func noRDMAUsers(ns netns.NsHandle, expected RDMADevice) error {
	if err := rdmaDeviceFDsIdle("/proc", expected); err != nil {
		return err
	}
	h, err := rdmaSocket(ns)
	if err != nil {
		return err
	}
	defer h.Close()
	mode, err := h.RdmaSystemGetNetnsMode()
	if err != nil || mode != rdmaExclusiveMode {
		return errors.Join(fmt.Errorf("E_RDMA_NAMESPACE_MODE: exclusive mode required"), err)
	}
	resources, err := h.RdmaResourceList()
	if err != nil {
		return err
	}
	if len(expected.Ports) == 1 && expected.Ports[0].LinkLayer == "infiniband" {
		return ibQuiescent(ns, h, resources, expected)
	}
	return rdmaQuiescent(resources, expected)
}

func rdmaDeviceFDsIdle(procRoot string, device RDMADevice) error {
	processes, err := os.ReadDir(procRoot)
	if err != nil {
		return err
	}
	for _, process := range processes {
		if pid, err := strconv.Atoi(process.Name()); err != nil || pid < 1 {
			continue
		}
		dir := filepath.Join(procRoot, process.Name(), "fd")
		fds, err := os.ReadDir(dir)
		if os.IsNotExist(err) { // A process can exit while its descriptors are queried.
			continue
		}
		if err != nil {
			return fmt.Errorf("E_RDMA_QUIESCENCE: cannot inspect process descriptors: %w", err)
		}
		for _, fd := range fds {
			var stat unix.Stat_t
			if err := unix.Stat(filepath.Join(dir, fd.Name()), &stat); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return fmt.Errorf("E_RDMA_QUIESCENCE: cannot inspect descriptor: %w", err)
			}
			if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
				continue
			}
			for _, node := range device.CharDevices {
				if unix.Major(stat.Rdev) == node.Major && unix.Minor(stat.Rdev) == node.Minor {
					return fmt.Errorf("E_RDMA_ACTIVE: process holds an HCA character device")
				}
			}
		}
	}
	return nil
}

func moveRDMA(source, target netns.NsHandle, expected RDMADevice) error {
	targetFD := int64(target)
	if targetFD < 0 || targetFD > math.MaxUint32 {
		return fmt.Errorf("E_RDMA_NAMESPACE_FD")
	}
	if err := noRDMAUsers(source, expected); err != nil {
		return err
	}
	h, err := rdmaSocket(source)
	if err != nil {
		return err
	}
	defer h.Close()
	link, err := h.RdmaLinkByName(expected.Name)
	if err != nil {
		return err
	}
	copy := []RDMADevice{expected}
	if err := matchRDMALinks(copy, []*netlink.RdmaLink{link}, *expected.Namespace); err != nil || copy[0].Index != expected.Index {
		return errors.Join(fmt.Errorf("E_RDMA_INCARNATION"), err)
	}
	return h.RdmaLinkSetNsFd(link, uint32(targetFD))
}

func (s RDMAState) verifyPhysical() error {
	if err := noEnabledVFs("/sys", s.Before.PCI); err != nil {
		return err
	}
	driver, err := pciDriver(s.Before.PCI)
	if err != nil || driver != s.Driver {
		return errors.Join(fmt.Errorf("E_RDMA_DRIVER_IDENTITY"), err)
	}
	override, err := driverOverride(s.Before.PCI)
	if err != nil || override != s.Override {
		return errors.Join(fmt.Errorf("E_RDMA_OVERRIDE_IDENTITY"), err)
	}
	for key, expected := range map[string]string{pciVendorAttribute: s.Vendor, pciDeviceAttribute: s.Device} {
		value, err := readPCI(s.Before.PCI, key)
		if err != nil || value != expected {
			return errors.Join(fmt.Errorf("E_RDMA_PCI_IDENTITY: %s", key), err)
		}
	}
	return nil
}

func rdmaVerbsNodes(d RDMADevice) ([]DeviceNode, error) {
	var nodes []DeviceNode
	for _, char := range d.CharDevices {
		if char.Class != rdmaVerbsClass {
			continue // MAD and common CM require their own scoped access grants.
		}
		node, err := verifiedRDMAChar(char)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if len(nodes) != 1 {
		return nil, fmt.Errorf("E_RDMA_ACCESS_SCOPE: require exactly one private verbs node")
	}
	return nodes, nil
}

func verifiedRDMAChar(char RDMAChar) (DeviceNode, error) {
	if char.Name != filepath.Base(char.Name) || len(char.Name) == 0 || char.Name[0] == '.' || char.Major == 0 {
		return DeviceNode{}, fmt.Errorf("E_RDMA_DEVICE_NODE")
	}
	path := filepath.Join("/dev/infiniband", char.Name)
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return DeviceNode{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(stat.Rdev) != char.Major || unix.Minor(stat.Rdev) != char.Minor {
		return DeviceNode{}, fmt.Errorf("E_RDMA_DEVICE_NODE: %s", path)
	}
	return DeviceNode{Path: path, Major: int64(char.Major), Minor: int64(char.Minor), Inode: stat.Ino, Filesystem: stat.Dev}, nil
}

// The common CM node is useful only with the verified single-HCA exclusive
// namespace. Its kernel-published rdev and the actual character node must agree.
func rdmaCMNode(sysRoot, path string) (DeviceNode, error) {
	value, err := sysText(filepath.Join(sysRoot, "class/misc/rdma_cm/dev"))
	if err != nil {
		return DeviceNode{}, err
	}
	majorText, minorText, ok := strings.Cut(value, ":")
	major, majorErr := strconv.ParseUint(majorText, 10, 32)
	minor, minorErr := strconv.ParseUint(minorText, 10, 32)
	if !ok || majorErr != nil || minorErr != nil || major == 0 {
		return DeviceNode{}, fmt.Errorf("E_RDMA_CM_IDENTITY")
	}
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return DeviceNode{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(stat.Rdev) != uint32(major) || unix.Minor(stat.Rdev) != uint32(minor) {
		return DeviceNode{}, fmt.Errorf("E_RDMA_CM_IDENTITY")
	}
	return DeviceNode{Path: path, Major: int64(major), Minor: int64(minor), Inode: stat.Ino, Filesystem: stat.Dev}, nil
}
func rdmaAccessNodes(d RDMADevice) ([]DeviceNode, error) {
	nodes, err := rdmaVerbsNodes(d)
	if err != nil {
		return nil, err
	}
	cm, err := rdmaCMNode("/sys", "/dev/infiniband/rdma_cm")
	if err != nil {
		return nil, err
	}
	return append(nodes, cm), nil
}

func (n *Native) applyRDMA(r *Record, save func() error) error {
	if topology.IBProfile(r.Effect.Parameters.RDMAProfile) {
		return n.applyIB(r, save)
	}
	if r.RDMA == nil || r.RDMA.Kernel.Before == nil || r.RDMA.RestoreAttempted {
		return fmt.Errorf("E_RDMA_BEFORE_IMAGE")
	}
	if !topology.ValidIfName(r.Effect.Parameters.IfName) || r.Effect.Parameters.MTU < 576 || r.Effect.Parameters.MTU > 9000 {
		return fmt.Errorf("E_RDMA_CONFIG: invalid interface name or Ethernet MTU")
	}
	s := r.RDMA
	if err := s.verifyPhysical(); err != nil {
		return err
	}
	if s.Connected {
		return n.observeRDMA(*r) // Recovery of a committed outcome never moves it again.
	}
	host, err := n.rdmaLocation(n.hostNS, *s)
	if err != nil {
		return err
	}
	sandbox, err := n.rdmaLocation(n.sandboxNS, *s)
	if err != nil || (host == nil) == (sandbox == nil) {
		return errors.Join(fmt.Errorf("E_RDMA_LOCATION: expected one retained owner"), err)
	}
	if host != nil {
		if err := emptyRDMANamespace(n.sandboxNS); err != nil {
			return err
		}
		if err := noRDMAUsers(n.hostNS, *host); err != nil {
			return err
		}
		if err := n.apply(&s.Kernel, save); err != nil {
			return err
		}
		link, err := n.link(s.Kernel.Output)
		if err != nil {
			return err
		}
		s.Kernel.Output.IfIndex, s.Kernel.State = link.Attrs().Index, Applied
		if err := save(); err != nil {
			return err
		}
		if err := moveRDMA(n.hostNS, n.sandboxNS, *host); err != nil {
			return err
		}
	}
	link, err := n.link(s.Kernel.Output)
	if err != nil {
		return err
	}
	c := r.Effect.Parameters
	if c.MTU < 576 || c.MTU > 9000 {
		return fmt.Errorf("E_RDMA_MTU")
	}
	if err := n.sandbox.LinkSetMTU(link, c.MTU); err != nil {
		return err
	}
	if c.Up {
		err = n.sandbox.LinkSetUp(link)
	} else {
		err = n.sandbox.LinkSetDown(link)
	}
	if err != nil {
		return err
	}
	// A namespace move unregisters/recreates RDMA clients. Only observed new
	// device nodes enter the acknowledgement, never the preparation's old rdev.
	current, nodes, err := waitRDMANodes(n.sandboxNS, s.Before, false)
	if err != nil {
		return err
	}
	if !slices.Equal(current.NetDevices, []string{link.Attrs().Name}) {
		return fmt.Errorf("E_RDMA_NETDEVICE_IDENTITY: HCA does not own the moved interface")
	}
	r.Output = Handle{Type: topology.RDMAType, Scope: SandboxScope, Name: current.Name, PCIAddress: current.PCI, RDMA: current, NamespacePath: n.sandboxPath, Devices: nodes}
	r.Outputs = map[string]Handle{InterfacePort: s.Kernel.Output}
	s.Connected = true
	return save()
}

func waitRDMANodes(ns netns.NsHandle, before RDMADevice, mad bool) (*RDMADevice, []DeviceNode, error) {
	for deadline := time.Now().Add(5 * time.Second); ; {
		current, err := rdmaIdentityIn(ns, before)
		if err != nil || current == nil {
			return nil, nil, errors.Join(fmt.Errorf("E_RDMA_HANDOFF_IDENTITY"), err)
		}
		nodes, err := rdmaProfileAccessNodes(*current, mad)
		if err == nil {
			return current, nodes, nil
		}
		if time.Now().After(deadline) || !os.IsNotExist(err) {
			return nil, nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func VerifyRDMADevice(h Handle) error {
	if h.Type != topology.RDMAType || h.Shared || h.Scope != SandboxScope || h.RDMA == nil || h.RDMA.Namespace == nil || h.NamespacePath == "" || h.PCIAddress != h.RDMA.PCI || h.Name != h.RDMA.Name {
		return fmt.Errorf("E_RDMA_ACCESS_GRANT")
	}
	identity, err := IdentifyNamespace(h.NamespacePath)
	if err != nil || identity != *h.RDMA.Namespace {
		return errors.Join(fmt.Errorf("E_RDMA_NAMESPACE_IDENTITY"), err)
	}
	ns, err := netns.GetFromPath(h.NamespacePath)
	if err != nil {
		return err
	}
	defer ns.Close()
	links, err := exclusiveRDMALinks(ns)
	if err != nil || len(links) != 1 || links[0].Attrs.Index != h.RDMA.Index {
		return errors.Join(fmt.Errorf("E_RDMA_NAMESPACE_SCOPE: expected only the allocated HCA"), err)
	}
	current, err := rdmaIdentityIn(ns, *h.RDMA)
	if err != nil || current == nil || topology.Digest(current.CharDevices) != topology.Digest(h.RDMA.CharDevices) || !slices.Equal(current.NetDevices, h.RDMA.NetDevices) {
		return errors.Join(fmt.Errorf("E_RDMA_ACCESS_IDENTITY"), err)
	}
	nodes, err := rdmaProfileAccessNodes(*current, h.MADAccess)
	if err != nil || !slices.Equal(nodes, h.Devices) {
		return errors.Join(fmt.Errorf("E_RDMA_ACCESS_IDENTITY: device nodes changed"), err)
	}
	return nil
}

func (n *Native) observeRDMA(r Record) error {
	if r.RDMA != nil && topology.IBProfile(r.RDMA.Profile) {
		return n.observeIB(r)
	}
	if r.RDMA == nil || !r.RDMA.Connected {
		return fmt.Errorf("E_RDMA_NOT_CONNECTED")
	}
	if err := r.RDMA.verifyPhysical(); err != nil {
		return err
	}
	if err := VerifyRDMADevice(r.Output); err != nil {
		return err
	}
	link, err := n.link(r.RDMA.Kernel.Output)
	if err != nil {
		return err
	}
	c := r.Effect.Parameters
	if link.Attrs().Name != c.IfName || state(link).MTU != c.MTU || state(link).Up != c.Up || topology.Digest(r.Outputs[InterfacePort]) != topology.Digest(r.RDMA.Kernel.Output) {
		return fmt.Errorf("E_RDMA_KERNEL_POSTCONDITION")
	}
	return nil
}

func (n *Native) restoreRDMA(r *Record, save func() error) error {
	if r.RDMA != nil && topology.IBProfile(r.RDMA.Profile) {
		return n.restoreIB(r, save)
	}
	if r.RDMA == nil || r.RDMA.Kernel.Before == nil {
		return fmt.Errorf("E_RDMA_BEFORE_IMAGE")
	}
	s := r.RDMA
	if err := s.verifyPhysical(); err != nil {
		return err
	}
	host, err := n.rdmaLocation(n.hostNS, *s)
	if err != nil {
		return err
	}
	sandbox, err := n.rdmaLocation(n.sandboxNS, *s)
	if err != nil || (host == nil) == (sandbox == nil) {
		return errors.Join(fmt.Errorf("E_RDMA_RESTORE_LOCATION"), err)
	}
	if sandbox != nil {
		s.RestoreAttempted = true
		if err := save(); err != nil {
			return err
		}
		if err := moveRDMA(n.sandboxNS, n.hostNS, *sandbox); err != nil {
			return err
		}
		host, err = n.rdmaLocation(n.hostNS, *s)
		if err != nil || host == nil {
			return errors.Join(fmt.Errorf("E_RDMA_RESTORE_IDENTITY"), err)
		}
	} else if s.Connected && !s.RestoreAttempted {
		return fmt.Errorf("E_RDMA_RESTORE_UNCERTAIN: committed HCA left its namespace without recorded cleanup")
	}
	if err := noRDMAUsers(n.hostNS, *host); err != nil {
		return err
	}
	s.RestoredDevice = host
	if err := save(); err != nil {
		return err
	}
	// Reuse Linux reverse cleanup under the same recorded exclusive grant.
	ex := Execution{Records: []Record{s.Kernel}}
	if err := n.Restore(&ex, func() error { s.Kernel = ex.Records[0]; return save() }); err != nil {
		return err
	}
	s.Kernel = ex.Records[0]
	link, err := n.link(r.Inputs[topology.DevicePort])
	if err != nil || state(link) != *s.Kernel.Before {
		return errors.Join(fmt.Errorf("E_RDMA_KERNEL_RESTORE"), err)
	}
	return nil
}

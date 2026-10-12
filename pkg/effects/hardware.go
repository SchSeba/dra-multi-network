// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
// PCI binding semantics follow sriov-cni/pkg/utils and the copied host provider.
// Ownership, before-images and device-node validation belong to the DRA journal.
package effects

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

var pciAddress = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
var driverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type DeviceNode struct {
	Path       string `json:"path"`
	Major      int64  `json:"major"`
	Minor      int64  `json:"minor"`
	Inode      uint64 `json:"inode"`
	Filesystem uint64 `json:"filesystem"`
}
type PCIState struct {
	Driver   string      `json:"driver"`
	Target   string      `json:"target,omitempty"`
	Override string      `json:"override"`
	Vendor   string      `json:"vendor"`
	Device   string      `json:"device"`
	Group    string      `json:"group"`
	Link     *LinkState  `json:"link,omitempty"`
	VF       *PCIVFState `json:"vf,omitempty"`
}

// Acknowledged driver-probe state is committed before restoring the saved link.
// An unacknowledged changed MAC cannot authorize cleanup after a restart.
type KernelRestoreState struct {
	Bound    bool   `json:"bound"`
	ProbeMAC string `json:"probeMAC,omitempty"`
}

func SingleFunctionIOMMU(pci string) (string, error) { return singleFunctionIOMMU("/sys", pci) }
func noEnabledVFs(sysRoot, pci string) error {
	if !pciAddress.MatchString(pci) {
		return fmt.Errorf("E_PCI_IDENTITY: malformed PCI address")
	}
	if value, err := os.ReadFile(filepath.Join(sysRoot, "bus/pci/devices", pci, "sriov_numvfs")); err == nil && strings.TrimSpace(string(value)) != "0" {
		return fmt.Errorf("E_PF_VFS_PROFILE: PF binding requires zero enabled VFs")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
func singleFunctionIOMMU(sysRoot, pci string) (string, error) {
	if err := noEnabledVFs(sysRoot, pci); err != nil {
		return "", err
	}
	groupPath, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "bus/pci/devices", pci, "iommu_group"))
	if err != nil {
		return "", fmt.Errorf("E_IOMMU: %w", err)
	}
	group := filepath.Base(groupPath)
	if _, err := strconv.ParseUint(group, 10, 32); err != nil {
		return "", fmt.Errorf("E_IOMMU_IDENTITY")
	}
	entries, err := os.ReadDir(filepath.Join(groupPath, "devices"))
	if err != nil {
		return "", err
	}
	// A certified initial VFIO profile owns the entire one-function IOMMU group.
	// Multi-function groups require a complete-group allocation contract.
	if len(entries) != 1 || entries[0].Name() != pci {
		return "", fmt.Errorf("E_IOMMU_SCOPE: require a complete single-function group")
	}
	return group, nil
}

func readPCI(pci, name string) (string, error) {
	if !pciAddress.MatchString(pci) {
		return "", fmt.Errorf("E_PCI_IDENTITY")
	}
	data, err := os.ReadFile(filepath.Join("/sys/bus/pci/devices", pci, name))
	return strings.TrimSpace(string(data)), err
}
func pciDriver(pci string) (string, error) {
	if !pciAddress.MatchString(pci) {
		return "", fmt.Errorf("E_PCI_IDENTITY")
	}
	path, err := filepath.EvalSymlinks(filepath.Join("/sys/bus/pci/devices", pci, "driver"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return filepath.Base(path), nil
}
func driverOverride(pci string) (string, error) {
	value, err := readPCI(pci, "driver_override")
	if value == "(null)" {
		value = ""
	}
	return value, err
}
func writeSysfs(path, value string) error { return os.WriteFile(path, []byte(value+"\n"), 0200) }

func (n *Native) capturePCI(h Handle, target string) (*PCIState, error) {
	if h.Type != topology.PCIFunctionType || h.Scope != HostScope || h.Shared {
		return nil, fmt.Errorf("E_PCI_GRANT")
	}
	if target != "" && (!driverName.MatchString(target) || !slices.Contains(h.AllowedDrivers, target)) {
		return nil, fmt.Errorf("E_DRIVER_GRANT: target driver must be allowlisted")
	}
	rdma, err := DiscoverRDMA()
	if err != nil {
		return nil, err
	}
	if err := pciRDMAScope(h.PCIAddress, rdma); err != nil {
		return nil, err
	}
	s := &PCIState{Target: target}
	if err := noEnabledVFs("/sys", h.PCIAddress); err != nil {
		return nil, err
	}
	if s.Driver, err = pciDriver(h.PCIAddress); err != nil {
		return nil, err
	}
	if s.Driver == topology.VFIODriver || target == topology.VFIODriver {
		if s.Group, err = SingleFunctionIOMMU(h.PCIAddress); err != nil {
			return nil, err
		}
	}
	if !driverName.MatchString(s.Driver) {
		return nil, fmt.Errorf("E_DRIVER_IDENTITY: require an existing approved binding")
	}
	if !slices.Contains(h.AllowedDrivers, s.Driver) {
		return nil, fmt.Errorf("E_DRIVER_GRANT: original and target drivers must be allowlisted")
	}
	if s.Override, err = driverOverride(h.PCIAddress); err != nil {
		return nil, err
	}
	if s.Override != "" && !slices.Contains(h.AllowedDrivers, s.Override) {
		return nil, fmt.Errorf("E_OVERRIDE_GRANT: restoration requires an approved original override")
	}
	if s.Vendor, err = readPCI(h.PCIAddress, pciVendorAttribute); err != nil {
		return nil, err
	}
	if s.Device, err = readPCI(h.PCIAddress, pciDeviceAttribute); err != nil {
		return nil, err
	}
	if s.Driver != topology.VFIODriver {
		l, err := n.link(Handle{Type: topology.InterfaceType, Scope: HostScope, Name: h.Name, PCIAddress: h.PCIAddress})
		if err != nil {
			return nil, err
		}
		before := state(l)
		if before.MasterIndex != 0 || before.Alias != "" {
			return nil, fmt.Errorf("E_EXTERNAL_OWNER: kernel function has an owner")
		}
		s.Link = &before
	}
	if s.VF, err = n.snapshotPCIVF(h); err != nil {
		return nil, err
	}
	return s, nil
}
func verifyPCI(pci string, s PCIState) error {
	if err := noEnabledVFs("/sys", pci); err != nil {
		return err
	}
	rdma, err := DiscoverRDMA()
	if err != nil {
		return err
	}
	if err := pciRDMAScope(pci, rdma); err != nil {
		return err
	}
	for name, expected := range map[string]string{pciVendorAttribute: s.Vendor, pciDeviceAttribute: s.Device} {
		actual, err := readPCI(pci, name)
		if err != nil || actual != expected {
			return fmt.Errorf("E_PCI_INCARNATION: %s: %w", name, err)
		}
	}
	if s.Group != "" {
		group, err := SingleFunctionIOMMU(pci)
		if err != nil || group != s.Group {
			return fmt.Errorf("E_IOMMU_CHANGED: %w", err)
		}
	}
	return nil
}
func (n *Native) bindPCI(pci, target string, before PCIState, restore bool) error {
	if err := verifyPCI(pci, before); err != nil {
		return err
	}
	if err := n.verifyPCIVF(pci, before.VF); err != nil {
		return err
	}
	if !driverName.MatchString(target) {
		return fmt.Errorf("E_DRIVER_IDENTITY")
	}
	if _, err := os.Stat(filepath.Join("/sys/bus/pci/drivers", target)); err != nil {
		return fmt.Errorf("E_DRIVER_UNAVAILABLE: load the approved driver before allocation: %w", err)
	}
	current, err := pciDriver(pci)
	if err != nil {
		return err
	}
	applied := before.Target
	if applied == "" { // Journals written before kernel binding targeted VFIO.
		applied = topology.VFIODriver
	}
	if current != "" && current != before.Driver && current != applied {
		return fmt.Errorf("E_EXTERNAL_BINDING: %s", current)
	}
	override, err := driverOverride(pci)
	if err != nil {
		return err
	}
	if override != before.Override && override != applied && override != before.Driver {
		return fmt.Errorf("E_EXTERNAL_OVERRIDE")
	}
	if err := noDeviceUsers(before.Group); err != nil {
		return err
	}
	if err := writeSysfs(filepath.Join("/sys/bus/pci/devices", pci, "driver_override"), target); err != nil {
		return err
	}
	if current != target {
		if current != "" {
			if err := writeSysfs(filepath.Join("/sys/bus/pci/drivers", current, "unbind"), pci); err != nil {
				return err
			}
		}
		if err := writeSysfs(filepath.Join("/sys/bus/pci/drivers", target, "bind"), pci); err != nil {
			return err
		}
	}
	if restore {
		if err := writeSysfs(filepath.Join("/sys/bus/pci/devices", pci, "driver_override"), before.Override); err != nil {
			return err
		}
		return nil // Verify VF state after restoring the recorded kernel link.
	}
	return n.verifyPCIVF(pci, before.VF)
}

func vfioDevices(pci string) ([]DeviceNode, error) {
	group, err := SingleFunctionIOMMU(pci)
	if err != nil {
		return nil, err
	}
	driver, err := pciDriver(pci)
	if err != nil || driver != topology.VFIODriver {
		return nil, fmt.Errorf("E_VFIO_BINDING: %w", err)
	}
	var result []DeviceNode
	for _, path := range []string{"/dev/vfio/vfio", "/dev/vfio/" + group} {
		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil {
			return nil, err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
			return nil, fmt.Errorf("E_DEVICE_NODE: require character device")
		}
		major, minor := unix.Major(stat.Rdev), unix.Minor(stat.Rdev)
		dev := filepath.Join("/sys/dev/char", fmt.Sprintf("%d:%d", major, minor))
		resolved, err := filepath.EvalSymlinks(dev)
		if err != nil || filepath.Base(resolved) != filepath.Base(path) {
			return nil, fmt.Errorf("E_DEVICE_IDENTITY: %s", path)
		}
		result = append(result, DeviceNode{Path: path, Major: int64(major), Minor: int64(minor), Inode: stat.Ino, Filesystem: stat.Dev})
	}
	return result, nil
}
func noDeviceUsers(group string) error {
	if group == "" {
		return nil
	}
	path := "/dev/vfio/" + group
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	// VFIO groups allow only one open owner. A successful temporary open proves
	// no current runtime/external process owns it; close before the next action.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("E_DEVICE_ACTIVE: %w", err)
	}
	return f.Close()
}
func (n *Native) applyHardware(r *Record, save func() error) error {
	if r.Effect.Kind == topology.DeviceAccess && r.Effect.Parameters.Mode == topology.RDMAMode {
		h := r.Inputs[topology.DevicePort]
		if r.Effect.Parameters.AccessBackend != "nri" {
			return fmt.Errorf("E_ACCESS_GRANT")
		}
		if err := VerifyRDMADevice(h); err != nil {
			return err
		}
		r.Output = h
		return nil
	}
	if r.Effect.Kind == topology.PCIReset {
		return n.applyReset(r, save)
	}
	if r.Effect.Kind == topology.VFConfigure {
		return n.applyVF(r, false)
	}
	h := r.Inputs[topology.DevicePort]
	if r.Effect.Kind == topology.DriverBind {
		target := r.Effect.Parameters.Driver
		if r.PCI == nil || !driverName.MatchString(target) || !slices.Contains(h.AllowedDrivers, target) {
			return fmt.Errorf("E_DRIVER_CONTRACT")
		}
		if err := n.bindPCI(h.PCIAddress, target, *r.PCI, false); err != nil {
			return err
		}
		r.Output = h
		if target != topology.VFIODriver {
			links, err := os.ReadDir(filepath.Join("/sys/bus/pci/devices", h.PCIAddress, "net"))
			if err != nil || len(links) != 1 {
				return fmt.Errorf("E_KERNEL_REDISCOVERY: require exactly one kernel netdevice: %w", err)
			}
			r.Output.Name = links[0].Name()
			link, err := n.link(r.Output)
			if err != nil {
				return err
			}
			if link.Type() != kernelDeviceType || link.Attrs().MasterIndex != 0 || link.Attrs().Alias != "" {
				return fmt.Errorf("E_KERNEL_BINDING: require an unowned PCI netdevice")
			}
		}
		return nil
	}
	if r.Effect.Parameters.Mode != topology.VFIOMode || r.Effect.Parameters.AccessBackend != "cdi" || h.Type != topology.PCIFunctionType || h.Shared {
		return fmt.Errorf("E_ACCESS_GRANT")
	}
	var err error
	for deadline := time.Now().Add(5 * time.Second); ; {
		r.Output.Devices, err = vfioDevices(h.PCIAddress)
		if err == nil || time.Now().After(deadline) || !os.IsNotExist(err) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	r.Output.Type, r.Output.Scope, r.Output.PCIAddress = topology.VFIOType, HostScope, h.PCIAddress
	return nil
}
func verifyDevice(h Handle) error {
	if h.Type != topology.VFIOType || h.Scope != HostScope || h.Shared {
		return fmt.Errorf("E_ACCESS_GRANT")
	}
	actual, err := vfioDevices(h.PCIAddress)
	if err != nil {
		return err
	}
	if !slices.Equal(actual, h.Devices) {
		return fmt.Errorf("E_DEVICE_INCARNATION")
	}
	return nil
}
func (n *Native) observeHardware(r Record) error {
	if r.Effect.Kind == topology.PCIReset {
		if err := observeReset(r); err != nil {
			return err
		}
		return n.verifyPCIVF(r.Reset.Scope.PCI, r.PCI.VF)
	}
	if r.Effect.Kind == topology.VFConfigure {
		return n.observeVF(r)
	}
	if r.Effect.Kind == topology.DeviceAccess {
		if r.Effect.Parameters.Mode == topology.RDMAMode {
			if r.Effect.Parameters.AccessBackend != "nri" || topology.Digest(r.Output) != topology.Digest(r.Inputs[topology.DevicePort]) {
				return fmt.Errorf("E_ACCESS_GRANT")
			}
			return VerifyRDMADevice(r.Output)
		}
		return verifyDevice(r.Output)
	}
	if r.PCI == nil {
		return fmt.Errorf("E_PCI_BEFORE_IMAGE")
	}
	if err := verifyPCI(r.Output.PCIAddress, *r.PCI); err != nil {
		return err
	}
	if err := n.verifyPCIVF(r.Output.PCIAddress, r.PCI.VF); err != nil {
		return err
	}
	driver, err := pciDriver(r.Output.PCIAddress)
	if err != nil || driver != r.Effect.Parameters.Driver {
		return fmt.Errorf("E_DRIVER_POSTCONDITION: %w", err)
	}
	override, err := driverOverride(r.Output.PCIAddress)
	if err != nil || override != r.Effect.Parameters.Driver {
		return fmt.Errorf("E_OVERRIDE_POSTCONDITION: %v", err)
	}
	return nil
}
func (n *Native) restoreHardware(r *Record, save func() error) error {
	if r.Effect.Kind == topology.PCIReset {
		return n.restoreReset(r, save)
	}
	if r.Effect.Kind == topology.VFConfigure {
		return n.applyVF(r, true)
	}
	if r.Effect.Kind == topology.DeviceAccess {
		return nil
	}
	return n.restorePCI(r, save)
}
func (n *Native) restorePCI(r *Record, save func() error) error {
	if r.PCI == nil {
		return fmt.Errorf("E_PCI_BEFORE_IMAGE")
	}
	pci := r.Inputs[topology.DevicePort].PCIAddress
	if err := verifyPCI(pci, *r.PCI); err != nil {
		return err
	}
	if r.KernelRestore == nil {
		if err := n.verifyPCIVF(pci, r.PCI.VF); err != nil {
			return err
		}
		r.KernelRestore = &KernelRestoreState{}
		if err := save(); err != nil {
			return err
		}
	}
	if !r.KernelRestore.Bound {
		if err := n.bindPCI(pci, r.PCI.Driver, *r.PCI, true); err != nil {
			return err
		}
		probeMAC, err := n.kernelRestoreVF(r, true)
		if err != nil {
			return err
		}
		r.KernelRestore.ProbeMAC, r.KernelRestore.Bound = probeMAC, true
	}
	if _, err := n.kernelRestoreVF(r, false); err != nil {
		return err
	}
	// Commit the acknowledged probe on every attempt, including a retry after a
	// failed outcome commit in this process, before changing its link MAC.
	if err := save(); err != nil {
		return err
	}
	driver, err := pciDriver(pci)
	if err != nil || driver != r.PCI.Driver {
		return errors.Join(fmt.Errorf("E_DRIVER_RESTORE"), err)
	}
	override, err := driverOverride(pci)
	if err != nil || override != r.PCI.Override {
		return fmt.Errorf("E_OVERRIDE_RESTORE: %w", err)
	}
	if r.PCI.Link != nil {
		links, err := os.ReadDir(filepath.Join("/sys/bus/pci/devices", pci, "net"))
		if err != nil || len(links) != 1 {
			return fmt.Errorf("E_KERNEL_REDISCOVERY: %w", err)
		}
		l, err := n.host.LinkByName(links[0].Name())
		if err != nil {
			return err
		}
		if l.Attrs().MasterIndex != 0 || l.Attrs().Alias != "" {
			return fmt.Errorf("E_EXTERNAL_OWNER: restored function acquired an owner")
		}
		if err := n.Preflight(Execution{Roots: map[string]Handle{"restore": {Type: topology.InterfaceType, Scope: HostScope, Name: l.Attrs().Name, PCIAddress: pci}}}); err != nil {
			return err
		}
		if err := restoreState(n.host, l, *r.PCI.Link); err != nil {
			return err
		}
	}
	return n.verifyPCIVF(pci, r.PCI.VF)
}

func (n *Native) kernelRestoreVF(r *Record, capture bool) (string, error) {
	if r.PCI.VF == nil {
		return "", nil
	}
	pci, before := r.Inputs[topology.DevicePort].PCIAddress, r.PCI.VF
	h := Handle{Type: topology.PCIFunctionType, Scope: HostScope, PCIAddress: pci, ParentPCI: before.ParentPCI, VFID: before.VFID}
	if err := verifyVFIdentity(h, before.Before); err != nil {
		return "", err
	}
	_, current, err := n.readVF(h)
	if err != nil {
		return "", err
	}
	if r.PCI.Link == nil {
		return "", n.verifyPCIVF(pci, before)
	}
	parentDriver, err := pciDriver(before.ParentPCI)
	if err != nil {
		return "", err
	}
	links, err := os.ReadDir(filepath.Join("/sys/bus/pci/devices", pci, "net"))
	if err != nil || len(links) != 1 {
		return "", fmt.Errorf("E_KERNEL_REDISCOVERY: %w", err)
	}
	link, err := n.link(Handle{Type: topology.InterfaceType, Scope: HostScope, PCIAddress: pci, Name: links[0].Name()})
	if err != nil {
		return "", err
	}
	return r.KernelRestore.vfProbeMAC(*r.PCI, current, state(link), parentDriver, capture)
}

func (s KernelRestoreState) vfProbeMAC(before PCIState, current VFValues, link LinkState, parentDriver string, capture bool) (string, error) {
	expected := before.VF.Before.Values
	if link.Alias != "" || link.MasterIndex != 0 {
		return "", fmt.Errorf("E_EXTERNAL_OWNER: driver probe acquired an owner")
	}
	if current == expected {
		if !capture && link.MAC != before.Link.MAC && link.MAC != s.ProbeMAC {
			return "", fmt.Errorf("E_VF_PROBE_UNCERTAIN: kernel MAC differs from the recorded probe and original")
		}
		return "", nil
	}
	withoutMAC := current
	withoutMAC.MAC = expected.MAC
	if withoutMAC != expected || before.Link.MAC != expected.MAC {
		return "", fmt.Errorf("E_VF_ADMIN_CHANGED: settings outside recorded link restoration changed")
	}
	// igb clears an unpinned VF MAC on reset; igbvf chooses another during
	// probe. Restore through the VF's saved kernel link, never the PF setter.
	if parentDriver != "igb" || before.Driver != "igbvf" {
		return "", fmt.Errorf("E_VF_PROBE_PROFILE: changed MAC requires the certified igb/igbvf probe profile")
	}
	if link.MAC != current.MAC || !capture && current.MAC != s.ProbeMAC {
		return "", fmt.Errorf("E_VF_PROBE_UNCERTAIN: retain scope without overwriting a different MAC")
	}
	return current.MAC, nil
}

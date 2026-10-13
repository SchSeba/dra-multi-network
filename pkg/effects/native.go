// Copyright 2015 CNI authors; Copyright 2017 Intel Corporation;
// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
// Adapted from containernetworking/plugins host-device, vlan, macvlan and
// ipvlan at 257ef09a103e8b8fe91a0fefe8680c01f84b520b, sriov-cni
// pkg/sriov/sriov.go at b536073521baf5bfba748e9d3da27ef736f48679, and
// bond-cni/bond/bond.go at f593e15f3935c8e73a6f66f2a718827ccfce494f.
// DRA replaces allocation/CNI entrypoints; mutations use durable ownership,
// retained staging namespaces, scoped netlink handles and explicit read-back.
package effects

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/ipam"
	"github.com/SchSeba/dra-multi-network/pkg/journal"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type Handle struct {
	Address             string                `json:"address,omitempty"`
	Lease               *ipam.Lease           `json:"lease,omitempty"`
	Type                string                `json:"type"`
	Scope               string                `json:"scope"`
	Name                string                `json:"name"`
	Alias               string                `json:"alias"`
	PCIAddress          string                `json:"pciAddress,omitempty"`
	Shared              bool                  `json:"shared,omitempty"`
	ShareID             string                `json:"shareID,omitempty"`
	IfIndex             int                   `json:"ifIndex,omitempty"`
	AllowedDrivers      []string              `json:"allowedDrivers,omitempty"`
	Devices             []DeviceNode          `json:"devices,omitempty"`
	ParentPCI           string                `json:"parentPCI,omitempty"`
	VFID                int                   `json:"vfID,omitempty"`
	AllowedVFSettings   []string              `json:"allowedVFSettings,omitempty"`
	AllowedResetMethods []string              `json:"allowedResetMethods,omitempty"`
	AllowedRDMAProfiles []string              `json:"allowedRDMAProfiles,omitempty"`
	AllowedMADAccess    bool                  `json:"allowedMADAccess,omitempty"`
	MADAccess           bool                  `json:"madAccess,omitempty"`
	RDMA                *RDMADevice           `json:"rdma,omitempty"`
	NamespacePath       string                `json:"namespacePath,omitempty"`
	Representor         *RepresentorIdentity  `json:"representor,omitempty"`
	AllowedHostBridges  []api.HostBridgeGrant `json:"allowedHostBridges,omitempty"`
	AllowedOVSBridges   []api.OVSBridgeGrant  `json:"allowedOVSBridges,omitempty"`
}
type LinkState struct {
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	MTU         int    `json:"mtu"`
	MAC         string `json:"mac"`
	Up          bool   `json:"up"`
	MasterIndex int    `json:"masterIndex"`
}
type Record struct {
	IPv4ForwardBefore *bool               `json:"ipv4ForwardBefore,omitempty"`
	Effect            Effect              `json:"effect"`
	Inputs            map[string]Handle   `json:"inputs"`
	Before            *LinkState          `json:"before,omitempty"`
	Output            Handle              `json:"output"`
	Created           *Handle             `json:"created,omitempty"`
	PCI               *PCIState           `json:"pci,omitempty"`
	VF                *VFState            `json:"vf,omitempty"`
	Reset             *ResetState         `json:"reset,omitempty"`
	KernelRestore     *KernelRestoreState `json:"kernelRestore,omitempty"`
	RDMA              *RDMAState          `json:"rdma,omitempty"`
	Outputs           map[string]Handle   `json:"outputs,omitempty"`
	State             string              `json:"state"`
	Bridge            *BridgePortState    `json:"bridge,omitempty"`
	OVS               *OVSPortState       `json:"ovs,omitempty"`
	Uplinks           []UplinkLease       `json:"uplinks,omitempty"`
	UplinkOwner       string              `json:"uplinkOwner,omitempty"`
}
type Execution struct {
	ID      string            `json:"id"`
	Program Program           `json:"program"`
	Roots   map[string]Handle `json:"roots"`
	Records []Record          `json:"records"`
	Outputs map[string]Handle `json:"outputs"`
}
type Native struct {
	UplinkStore                *journal.Store
	host, sandbox, stage       *netlink.Handle
	hostNS, sandboxNS, stageNS netns.NsHandle
	sandboxPath                string
}

const Applying = "Applying"

const kernelDeviceType = "device"

func OpenHostNative() (*Native, error) {
	n := &Native{hostNS: netns.None(), sandboxNS: netns.None(), stageNS: netns.None()}
	var err error
	n.hostNS, err = netns.Get()
	if err != nil {
		return nil, err
	}
	n.host, err = netlink.NewHandle()
	if err != nil {
		n.Close()
		return nil, err
	}
	return n, nil
}

type NamespaceIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func IdentifyNamespace(path string) (NamespaceIdentity, error) {
	ns, err := netns.GetFromPath(path)
	if err != nil {
		return NamespaceIdentity{}, err
	}
	defer ns.Close()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(ns), &fs); err != nil {
		return NamespaceIdentity{}, err
	}
	if fs.Type != unix.NSFS_MAGIC {
		return NamespaceIdentity{}, fmt.Errorf("E_NAMESPACE_TYPE: not a namespace filesystem")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(ns), &stat); err != nil {
		return NamespaceIdentity{}, err
	}
	return NamespaceIdentity{Device: stat.Dev, Inode: stat.Ino}, nil
}

func PinNamespace(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	// A mount is the retention authority; the file alone never counts as a pin.
	var stat unix.Statfs_t
	if err := unix.Statfs(target, &stat); err == nil && stat.Type == unix.NSFS_MAGIC {
		sourceID, err := IdentifyNamespace(source)
		if err != nil {
			return err
		}
		targetID, err := IdentifyNamespace(target)
		if err != nil {
			return err
		}
		if sourceID != targetID {
			return fmt.Errorf("E_NAMESPACE_IDENTITY: retained mount differs from runtime namespace")
		}
		return nil
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_RDONLY, 0600)
	if err != nil && !os.IsExist(err) {
		return err
	}
	if f != nil {
		if err := f.Close(); err != nil {
			return err
		}
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("retain namespace: %w", err)
	}
	return nil
}
func CreateStagingNamespace(target string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(target, &stat); err == nil && stat.Type == unix.NSFS_MAGIC {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		original, err := netns.Get()
		if err != nil {
			done <- err
			return
		}
		defer original.Close()
		created, err := netns.New()
		if err != nil {
			runtime.UnlockOSThread()
			done <- err
			return
		}
		err = PinNamespace(fmt.Sprintf("/proc/self/task/%d/ns/net", unix.Gettid()), target)
		err = errors.Join(err, created.Close())
		if restoreErr := netns.Set(original); restoreErr != nil {
			done <- errors.Join(err, restoreErr)
			return
		} // Locked thread dies on goroutine exit.
		runtime.UnlockOSThread()
		done <- err
	}()
	return <-done
}
func Unpin(path string) error {
	if err := unix.Unmount(path, 0); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
func OpenNative(sandbox, stage string) (*Native, error) {
	n := &Native{hostNS: netns.None(), sandboxNS: netns.None(), stageNS: netns.None(), sandboxPath: sandbox}
	var err error
	n.hostNS, err = netns.Get()
	if err != nil {
		return nil, err
	}
	n.sandboxNS, err = netns.GetFromPath(sandbox)
	if err != nil {
		n.Close()
		return nil, err
	}
	if n.hostNS.Equal(n.sandboxNS) {
		n.Close()
		return nil, fmt.Errorf("E_HOST_NAMESPACE: sandbox equals host")
	}
	n.stageNS, err = netns.GetFromPath(stage)
	if err != nil {
		n.Close()
		return nil, err
	}
	n.host, err = netlink.NewHandleAt(n.hostNS)
	if err != nil {
		n.Close()
		return nil, err
	}
	n.sandbox, err = netlink.NewHandleAt(n.sandboxNS)
	if err != nil {
		n.Close()
		return nil, err
	}
	n.stage, err = netlink.NewHandleAt(n.stageNS)
	if err != nil {
		n.Close()
		return nil, err
	}
	return n, nil
}
func (n *Native) Close() {
	for _, h := range []*netlink.Handle{n.host, n.sandbox, n.stage} {
		if h != nil {
			if err := h.Close(); err != nil {
				klog.ErrorS(err, "close netlink handle")
			}
		}
	}
	for _, ns := range []netns.NsHandle{n.hostNS, n.sandboxNS, n.stageNS} {
		if ns.IsOpen() {
			if err := ns.Close(); err != nil {
				klog.ErrorS(err, "close namespace handle")
			}
		}
	}
}
func (n *Native) scope(scope string) *netlink.Handle {
	switch scope {
	case HostScope:
		return n.host
	case SandboxScope:
		return n.sandbox
	case StageScope:
		return n.stage
	default:
		return nil
	}
}
func state(link netlink.Link) LinkState {
	a := link.Attrs()
	return LinkState{Name: a.Name, Alias: a.Alias, MTU: a.MTU, MAC: a.HardwareAddr.String(), Up: a.Flags&net.FlagUp != 0, MasterIndex: a.MasterIndex}
}

// Handles retain the durable identity; the user label cannot replace it.
func ownershipAlias(alias string) string {
	if strings.HasPrefix(alias, "dra-mn:") {
		identity, _, _ := strings.Cut(alias, ";")
		return identity
	}
	return alias
}

func labeledAlias(identity, label string) string {
	if label == "" {
		return identity
	}
	return identity + ";" + label
}

func linkMACGrant(h Handle, c topology.Config) error {
	if c.MAC != "" && (h.Shared || h.ParentPCI != "" && h.ParentPCI != h.PCIAddress && !slices.Contains(h.AllowedVFSettings, "mac")) {
		return fmt.Errorf("E_LINK_MAC_GRANT: shared parents cannot change MAC; VF MAC requires its policy grant")
	}
	return nil
}

func (n *Native) link(handle Handle) (netlink.Link, error) {
	if handle.Type == topology.RepresentorType {
		l, err := n.representor(handle)
		if err == nil && handle.Alias != "" && l.Attrs().Alias != handle.Alias {
			return nil, fmt.Errorf("E_REPRESENTOR_OWNERSHIP")
		}
		return l, err
	}
	h := n.scope(handle.Scope)
	if h == nil {
		return nil, fmt.Errorf("E_NAMESPACE_SCOPE")
	}
	if handle.Alias != "" {
		links, err := h.LinkList()
		if err != nil {
			return nil, err
		}
		var found netlink.Link
		for _, l := range links {
			if ownershipAlias(l.Attrs().Alias) == handle.Alias {
				if found != nil {
					return nil, fmt.Errorf("E_OWNERSHIP_AMBIGUOUS: duplicate alias")
				}
				if handle.IfIndex != 0 && handle.IfIndex != l.Attrs().Index {
					return nil, fmt.Errorf("E_LINK_INCARNATION: index differs for owned alias")
				}
				found = l
			}
		}
		if found != nil {
			return found, nil
		}
		return nil, fmt.Errorf("E_OWNERSHIP: link %s absent", handle.Alias)
	}
	if handle.Scope != HostScope || handle.PCIAddress == "" {
		return nil, fmt.Errorf("E_OWNERSHIP: missing allocation identity")
	}
	l, err := h.LinkByName(handle.Name)
	if err != nil {
		return nil, err
	}
	device, err := filepath.EvalSymlinks(filepath.Join("/sys/class/net", l.Attrs().Name, "device"))
	if err != nil || filepath.Base(device) != handle.PCIAddress {
		return nil, fmt.Errorf("E_PCI_IDENTITY: %s", handle.PCIAddress)
	}
	return l, nil
}
func (n *Native) owned(alias string) (netlink.Link, string, error) {
	var found netlink.Link
	var foundScope string
	for _, scope := range []string{SandboxScope, StageScope, HostScope} {
		links, err := n.scope(scope).LinkList()
		if err != nil {
			return nil, "", err
		}
		for _, link := range links {
			if ownershipAlias(link.Attrs().Alias) == alias {
				if found != nil {
					return nil, "", fmt.Errorf("E_OWNERSHIP_AMBIGUOUS: duplicate alias")
				}
				found = link
				foundScope = scope
			}
		}
	}
	return found, foundScope, nil
}
func (n *Native) Preflight(ex Execution) error {
	grants := map[string]Handle{}
	for id, root := range ex.Roots {
		grants["call.allocation."+id] = root
	}
	for _, root := range ex.Roots {
		if root.Type == topology.AddressLeaseType {
			continue
		}
		if root.Type == topology.RDMAType {
			continue // The namespace effect supplies its locked profile below.
		}
		if root.Type == topology.VFIOType {
			if err := verifyDevice(root); err != nil {
				return err
			}
			continue
		}
		if root.Type == topology.PCIFunctionType {
			before, err := n.capturePCI(root, "")
			if err != nil {
				return err
			}
			if err := noDeviceUsers(before.Group); err != nil {
				return err
			}
			if before.Link == nil {
				continue
			}
		}
		if err := n.preflightHostLink(root); err != nil {
			return err
		}
	}
	for _, e := range ex.Program.Effects {
		if e.Kind == topology.RepresentorMap {
			mapped, err := n.mapRepresentor(grants[e.Inputs[topology.DevicePort].From])
			if err != nil {
				return err
			}
			grants["effects."+e.ID+"."+topology.RepresentorPort] = mapped
		}
		if e.Kind == topology.UplinkConfigure {
			input := grants[e.Inputs[topology.RepresentorPort].From]
			if _, err := n.captureUplink(input, e.Parameters); err != nil {
				return err
			}
			grants["effects."+e.ID+"."+topology.RepresentorPort] = input
		}
		if e.Kind == topology.BridgeConfigure {
			input := grants[e.Inputs[topology.RepresentorPort].From]
			if _, err := n.captureBridge(input, e.Parameters); err != nil {
				return err
			}
		}
		if e.Kind == topology.OVSConfigure {
			if _, err := n.captureOVS(grants[e.Inputs[topology.RepresentorPort].From], e.Parameters); err != nil {
				return err
			}
		}
		if e.Kind == topology.RDMANamespace {
			if _, err := n.captureRDMA(grants[e.Inputs[topology.DevicePort].From], e.Parameters); err != nil {
				return err
			}
		}
		if err := topology.ValidateLinkConfig(e.Parameters); err != nil {
			return err
		}
		if e.Kind == Move || e.Kind == Configure {
			input := grants[e.Inputs[InterfacePort].From]
			if err := linkMACGrant(input, e.Parameters); err != nil {
				return err
			}
			grants["effects."+e.ID+".interface"] = input
		}
		if e.Kind == topology.DriverBind || e.Kind == topology.PCIReset {
			root := grants[e.Inputs[topology.DevicePort].From]
			if e.Kind == topology.PCIReset {
				if _, _, err := n.captureReset(root, e.Parameters.ResetMethod); err != nil {
					return err
				}
			} else if _, err := n.capturePCI(root, e.Parameters.Driver); err != nil {
				return err
			}
			grants["effects."+e.ID+".device"] = root
		}
		if e.Kind == topology.VFConfigure {
			root := grants[e.Inputs[topology.DevicePort].From]
			if _, err := n.captureVF(root, e.Parameters.VF); err != nil {
				return err
			}
			grants["effects."+e.ID+".device"] = root
		}
		if e.Parameters.IfName == "" {
			continue
		}
		if n.sandbox == nil {
			return fmt.Errorf("E_PREPARATION_SCOPE")
		}
		if _, err := n.sandbox.LinkByName(e.Parameters.IfName); err == nil {
			return fmt.Errorf("E_NAME_COLLISION: %s", e.Parameters.IfName)
		} else {
			var missing netlink.LinkNotFoundError
			if !errors.As(err, &missing) {
				return err
			}
		}
	}
	return nil
}
func (n *Native) Execute(ex *Execution, save func() error) error {
	resolved := map[string]Handle{}
	for id, h := range ex.Roots {
		resolved["call.allocation."+id] = h
		if h.Type == topology.VFIOType {
			resolved["call.prepared."+id+"."+topology.VFIOPort] = h
		}
	}
	for i, e := range ex.Program.Effects {
		if i >= len(ex.Records) {
			rec := Record{Effect: e, Inputs: map[string]Handle{}, State: Applying, Output: Handle{Type: topology.InterfaceType, Scope: SandboxScope, Name: e.Parameters.IfName, Alias: "dra-mn:" + ex.ID + ":" + e.ID}}
			for port, ref := range e.Inputs {
				h, exists := resolved[ref.From]
				if !exists {
					return fmt.Errorf("E_EFFECT_REFERENCE: %s", ref.From)
				}
				rec.Inputs[port] = h
			}
			if e.Kind == Move || e.Kind == Configure || e.Kind == AttachMaster {
				input := rec.Inputs[InterfacePort]
				if input.Type != topology.InterfaceType || e.Kind == AttachMaster && rec.Inputs["master"].Type != topology.InterfaceType {
					return fmt.Errorf("E_KERNEL_INTERFACE_GRANT: representors cannot use sandbox link operations")
				}
				l, err := n.link(input)
				if err != nil {
					return err
				}
				before := state(l)
				rec.Before = &before
				if e.Kind != Move {
					rec.Output = input
				} else {
					rec.Output.PCIAddress = input.PCIAddress
					rec.Output.ShareID = input.ShareID
					rec.Output.ParentPCI = input.ParentPCI
					rec.Output.VFID = input.VFID
					rec.Output.AllowedVFSettings = input.AllowedVFSettings
				}
				if input.Shared {
					return fmt.Errorf("E_SHARED_PARENT_MUTATION")
				}
			}
			if e.Kind == topology.AddressConfigure {
				var err error
				rec.Output, err = n.captureAddress(rec.Inputs)
				if err != nil {
					return err
				}
			}
			if e.Kind == topology.IPv4Forwarding {
				input := rec.Inputs[InterfacePort]
				before, err := n.ipv4Forwarding(input, nil)
				if err != nil {
					return err
				}
				rec.IPv4ForwardBefore, rec.Output = &before, input
			}
			if e.Kind == topology.DriverBind || e.Kind == topology.PCIReset {
				var err error
				if e.Kind == topology.PCIReset {
					rec.PCI, rec.Reset, err = n.captureReset(rec.Inputs[topology.DevicePort], e.Parameters.ResetMethod)
					rec.Output = rec.Inputs[topology.DevicePort]
				} else {
					rec.PCI, err = n.capturePCI(rec.Inputs[topology.DevicePort], e.Parameters.Driver)
				}
				if err != nil {
					return err
				}
			}
			if e.Kind == topology.VFConfigure {
				input := rec.Inputs[topology.DevicePort]
				before, err := n.captureVF(input, e.Parameters.VF)
				if err != nil {
					return err
				}
				rec.VF, rec.Output = before, input
			}
			if e.Kind == topology.RDMANamespace {
				var err error
				rec.RDMA, err = n.captureRDMA(rec.Inputs[topology.DevicePort], e.Parameters)
				if err != nil {
					return err
				}
				kernel := &rec.RDMA.Kernel
				if !topology.IBProfile(e.Parameters.RDMAProfile) {
					kernel.Effect = Effect{ID: e.ID + "-kernel", Kind: Move, Parameters: topology.Config{IfName: e.Parameters.IfName}}
				}
				input := rec.Inputs[topology.DevicePort]
				input.Type = topology.InterfaceType
				kernel.Inputs = map[string]Handle{InterfacePort: input}
				kernel.Output = Handle{Type: topology.InterfaceType, Scope: SandboxScope, Name: e.Parameters.IfName, Alias: "dra-mn:" + ex.ID + ":" + e.ID + ":kernel", PCIAddress: input.PCIAddress}
				kernel.State = Applying
			}
			if e.Kind == topology.RepresentorMap {
				var err error
				rec.Output, err = n.mapRepresentor(rec.Inputs[topology.DevicePort])
				if err != nil {
					return err
				}
			}
			if e.Kind == topology.UplinkConfigure {
				input := rec.Inputs[topology.RepresentorPort]
				var err error
				rec.Uplinks, err = n.captureUplink(input, e.Parameters)
				if err != nil {
					return err
				}
				rec.UplinkOwner = "dra-mn:" + ex.ID + ":" + e.ID
				rec.Output = input
			}
			if e.Kind == topology.BridgeConfigure {
				input := rec.Inputs[topology.RepresentorPort]
				var err error
				rec.Bridge, err = n.captureBridge(input, e.Parameters)
				if err != nil {
					return err
				}
				rec.Output = input
				rec.Output.Alias = "dra-mn:" + ex.ID + ":" + e.ID
			}
			if e.Kind == topology.OVSConfigure {
				input := rec.Inputs[topology.RepresentorPort]
				var err error
				rec.OVS, err = n.captureOVS(input, e.Parameters)
				if err != nil {
					return err
				}
				rec.Output = input
				rec.Output.Alias = "dra-mn:" + ex.ID + ":" + e.ID
			}
			ex.Records = append(ex.Records, rec)
			if err := save(); err != nil {
				return err
			}
		}
		rec := &ex.Records[i]
		if rec.State == Restored {
			return fmt.Errorf("E_EXECUTION_REMOVED")
		}
		if err := n.apply(rec, save); err != nil {
			return fmt.Errorf("effect %s: %w", e.ID, err)
		}
		if err := n.observe(*rec); err != nil {
			return fmt.Errorf("postcondition %s: %w", e.ID, err)
		}
		port := topology.DevicePort
		if rec.Output.Type == topology.RepresentorType {
			port = topology.RepresentorPort
		}
		if rec.Output.Type == topology.InterfaceType {
			observed, err := n.link(rec.Output)
			if err != nil {
				return err
			}
			rec.Output.IfIndex = observed.Attrs().Index
			port = InterfacePort
		}
		rec.State = Applied
		if err := save(); err != nil {
			return err
		}
		resolved["effects."+e.ID+"."+port] = rec.Output
		for extra, output := range rec.Outputs {
			if extra == port {
				return fmt.Errorf("E_EFFECT_OUTPUT: duplicate port %s", extra)
			}
			resolved["effects."+e.ID+"."+extra] = output
		}
	}
	ex.Outputs = map[string]Handle{}
	for name, ref := range ex.Program.Exports {
		ex.Outputs[name] = resolved[ref.From]
	}
	return save()
}
func (n *Native) apply(r *Record, save func() error) error {
	if r.Effect.Kind == topology.IPv4Forwarding {
		if r.IPv4ForwardBefore == nil || r.Effect.Parameters.IPv4Forward == nil {
			return fmt.Errorf("E_TUNING_RECORD")
		}
		_, err := n.ipv4Forwarding(r.Output, r.Effect.Parameters.IPv4Forward)
		return err
	}
	if r.Effect.Kind == topology.AddressConfigure {
		return n.applyAddress(*r)
	}
	if r.Effect.Kind == topology.UplinkConfigure {
		return n.applyUplink(r)
	}
	if r.Effect.Kind == topology.OVSConfigure {
		return n.applyOVS(r, save)
	}
	if r.Effect.Kind == topology.RepresentorMap {
		_, err := n.representor(r.Output)
		return err
	}
	if r.Effect.Kind == topology.BridgeConfigure {
		return n.applyBridge(r, save)
	}
	if r.Effect.Kind == topology.RDMANamespace {
		return n.applyRDMA(r, save)
	}
	if r.Effect.Kind == topology.DriverBind || r.Effect.Kind == topology.DeviceAccess || r.Effect.Kind == topology.VFConfigure || r.Effect.Kind == topology.PCIReset {
		return n.applyHardware(r, save)
	}
	c := r.Effect.Parameters
	switch r.Effect.Kind {
	case Move:
		// Recheck on replay as well as preflight: a new HCA dependency is not
		// covered by an older Ethernet-only allocation or checkpoint.
		if err := ethernetScope(r.Inputs[InterfacePort]); err != nil {
			return err
		}
		link, scope, err := n.owned(r.Output.Alias)
		if err != nil {
			return err
		}
		if link == nil {
			link, err = n.link(r.Inputs[InterfacePort])
			if err != nil {
				return err
			}
			if state(link) != *r.Before {
				return fmt.Errorf("E_ORIGINAL_STATE_CHANGED")
			}
			if err := n.host.LinkSetAlias(link, r.Output.Alias); err != nil {
				return err
			}
			scope = HostScope
		}
		if scope == HostScope {
			if err := n.host.LinkSetDown(link); err != nil {
				return err
			}
			if err := n.host.LinkSetNsFd(link, int(n.stageNS)); err != nil {
				return err
			}
			scope = StageScope
		}
		if scope == StageScope {
			link, _, err = n.owned(r.Output.Alias)
			if err != nil {
				return err
			}
			for _, alt := range link.Attrs().AltNames {
				if alt == c.IfName {
					if err := n.stage.LinkDelAltName(link, alt); err != nil {
						return err
					}
				}
			}
			if err := n.stage.LinkSetName(link, c.IfName); err != nil {
				return err
			}
			if err := n.stage.LinkSetNsFd(link, int(n.sandboxNS)); err != nil {
				return err
			}
		}
		return nil
	case Configure:
		if err := topology.ValidateLinkConfig(c); err != nil {
			return err
		}
		input := r.Inputs[InterfacePort]
		if input.Shared || input.Scope != SandboxScope || input.Type != topology.InterfaceType || !strings.HasPrefix(input.Alias, "dra-mn:") {
			return fmt.Errorf("E_LINK_CONFIG_SCOPE: require an owned private sandbox interface")
		}
		if err := linkMACGrant(input, c); err != nil {
			return err
		}
		link, err := n.link(r.Inputs[InterfacePort])
		if err != nil {
			return err
		}
		if c.Alias != nil && len(labeledAlias(input.Alias, *c.Alias)) > 255 {
			return fmt.Errorf("E_LINK_ALIAS_SIZE")
		}
		if c.MAC != "" {
			// IPvlan inherits its parent MAC; active-backup bonds use the
			// active member MAC. Neither supports an independent MAC here.
			if link.Type() == "ipvlan" || link.Type() == "bond" || len(link.Attrs().HardwareAddr) != 6 {
				return fmt.Errorf("E_LINK_MAC_PROFILE: %s controls its own MAC", link.Type())
			}
			if link.Attrs().HardwareAddr.String() != c.MAC {
				if err := n.sandbox.LinkSetDown(link); err != nil {
					return err
				}
				mac, _ := net.ParseMAC(c.MAC) // Validated before mutation.
				if err := n.sandbox.LinkSetHardwareAddr(link, mac); err != nil {
					return err
				}
			}
		}
		if c.Alias != nil {
			if err := n.sandbox.LinkSetAlias(link, labeledAlias(input.Alias, *c.Alias)); err != nil {
				return err
			}
		}
		if err := n.sandbox.LinkSetMTU(link, c.MTU); err != nil {
			return err
		}
		if c.Up {
			return n.sandbox.LinkSetUp(link)
		}
		return n.sandbox.LinkSetDown(link)
	case AttachMaster:
		link, err := n.link(r.Inputs[InterfacePort])
		if err != nil {
			return err
		}
		master, err := n.link(r.Inputs["master"])
		if err != nil {
			return err
		}
		if link.Attrs().MasterIndex != 0 && link.Attrs().MasterIndex != master.Attrs().Index {
			return fmt.Errorf("E_EXTERNAL_MASTER")
		}
		if err := n.sandbox.LinkSetDown(link); err != nil {
			return err
		}
		if err := n.sandbox.LinkSetMaster(link, master); err != nil {
			return err
		}
		return n.sandbox.LinkSetUp(link)
	default:
		existing, scope, err := n.owned(r.Output.Alias)
		if err != nil {
			return err
		}
		if existing != nil {
			if scope != SandboxScope {
				return fmt.Errorf("E_NAMESPACE_SCOPE")
			}
			if existing.Attrs().Name != c.IfName {
				return n.sandbox.LinkSetName(existing, c.IfName)
			}
			return nil
		}
		if r.Created != nil {
			created, err := n.sandbox.LinkByName(r.Created.Name)
			if err != nil {
				return err
			}
			if created.Attrs().Index != r.Created.IfIndex || created.Type() != r.Created.Type || created.Attrs().Alias != "" {
				return fmt.Errorf("E_CREATE_AMBIGUOUS")
			}
			if err := n.sandbox.LinkSetAlias(created, r.Output.Alias); err != nil {
				return err
			}
			return n.sandbox.LinkSetName(created, c.IfName)
		}
		attrs := netlink.NewLinkAttrs()
		attrs.Name = "mn" + topology.Digest(r.Output.Alias)[:12]
		attrs.Alias = r.Output.Alias
		attrs.MTU = c.MTU
		var link netlink.Link
		source := n.sandbox
		switch r.Effect.Kind {
		case CreateBond:
			b := netlink.NewLinkBond(attrs)
			b.Mode = netlink.BOND_MODE_ACTIVE_BACKUP
			b.Miimon = c.MonitorIntervalMs
			b.FailOverMac = netlink.BOND_FAIL_OVER_MAC_ACTIVE
			link = b
		case CreateVLAN, CreateMacvlan, CreateIPvlan:
			parent := r.Inputs["parent"]
			l, err := n.link(parent)
			if err != nil {
				return err
			}
			attrs.ParentIndex = l.Attrs().Index
			if c.MTU > l.Attrs().MTU {
				return fmt.Errorf("E_MTU: child exceeds live parent")
			}
			switch r.Effect.Kind {
			case CreateVLAN:
				if parent.Scope != SandboxScope || parent.Shared {
					return fmt.Errorf("E_PORT_ACCESS")
				}
				link = &netlink.Vlan{LinkAttrs: attrs, VlanId: c.VlanID, VlanProtocol: netlink.VLAN_PROTOCOL_8021Q}
			case CreateMacvlan:
				if !parent.Shared || parent.Scope != HostScope {
					return fmt.Errorf("E_SHARE_ENTITLEMENT")
				}
				attrs.Namespace = netlink.NsFd(int(n.sandboxNS))
				link = &netlink.Macvlan{LinkAttrs: attrs, Mode: netlink.MACVLAN_MODE_BRIDGE}
				source = n.host
			case CreateIPvlan:
				if !parent.Shared || parent.Scope != HostScope {
					return fmt.Errorf("E_SHARE_ENTITLEMENT")
				}
				attrs.Namespace = netlink.NsFd(int(n.sandboxNS))
				link = &netlink.IPVlan{LinkAttrs: attrs, Mode: netlink.IPVLAN_MODE_L2}
				source = n.host
			}
		default:
			return fmt.Errorf("E_EFFECT_OPCODE")
		}
		if err := source.LinkAdd(link); err != nil {
			return err
		}
		// Some kernel link drivers discard IFLA_IFALIAS during creation. Capture
		// the freshly returned object before assigning its durable ownership alias.
		created, err := n.sandbox.LinkByName(attrs.Name)
		if err != nil {
			return err
		}
		r.Created = &Handle{Type: link.Type(), Scope: SandboxScope, Name: attrs.Name, IfIndex: created.Attrs().Index}
		if err := save(); err != nil {
			return err
		}
		if err := n.sandbox.LinkSetAlias(created, r.Output.Alias); err != nil {
			return err
		}
		created, _, err = n.owned(r.Output.Alias)
		if err != nil {
			return err
		}
		if created == nil {
			return fmt.Errorf("E_OWNERSHIP: created alias absent")
		}
		return n.sandbox.LinkSetName(created, c.IfName)
	}
}
func (n *Native) observe(r Record) error {
	if r.Effect.Kind == topology.IPv4Forwarding {
		if r.IPv4ForwardBefore == nil || r.Effect.Parameters.IPv4Forward == nil {
			return fmt.Errorf("E_TUNING_RECORD")
		}
		actual, err := n.ipv4Forwarding(r.Output, nil)
		if err != nil {
			return err
		}
		if actual != *r.Effect.Parameters.IPv4Forward {
			return fmt.Errorf("E_TUNING_STATE: IPv4 forwarding differs")
		}
		return nil
	}
	if r.Effect.Kind == topology.AddressConfigure {
		return n.observeAddress(r)
	}
	if r.Effect.Kind == topology.UplinkConfigure {
		return n.observeUplink(r)
	}
	if r.Effect.Kind == topology.OVSConfigure {
		return n.observeOVS(r)
	}
	if r.Effect.Kind == topology.RepresentorMap {
		_, err := n.representor(r.Output)
		return err
	}
	if r.Effect.Kind == topology.BridgeConfigure {
		return n.observeBridge(r)
	}
	if r.Effect.Kind == topology.RDMANamespace {
		return n.observeRDMA(r)
	}
	if r.Effect.Kind == topology.DriverBind || r.Effect.Kind == topology.DeviceAccess || r.Effect.Kind == topology.VFConfigure || r.Effect.Kind == topology.PCIReset {
		return n.observeHardware(r)
	}
	if r.Effect.Kind == Move {
		if err := ethernetScope(r.Inputs[InterfacePort]); err != nil {
			return err
		}
	}
	l, err := n.link(r.Output)
	if err != nil {
		return err
	}
	c := r.Effect.Parameters
	if c.IfName != "" && l.Attrs().Name != c.IfName {
		return fmt.Errorf("name differs")
	}
	switch r.Effect.Kind {
	case Configure:
		if l.Attrs().MTU != c.MTU || (l.Attrs().Flags&net.FlagUp != 0) != c.Up {
			return fmt.Errorf("MTU/state differs")
		}
		if c.MAC != "" && l.Attrs().HardwareAddr.String() != c.MAC {
			return fmt.Errorf("E_LINK_MAC_STATE: MAC differs")
		}
		if c.Alias != nil && l.Attrs().Alias != labeledAlias(r.Output.Alias, *c.Alias) {
			return fmt.Errorf("E_LINK_ALIAS_STATE: label differs")
		}
	case CreateBond:
		b, ok := l.(*netlink.Bond)
		if !ok || b.Mode != netlink.BOND_MODE_ACTIVE_BACKUP || b.Miimon != c.MonitorIntervalMs || b.FailOverMac != netlink.BOND_FAIL_OVER_MAC_ACTIVE {
			return fmt.Errorf("bond parameters differ")
		}
	case AttachMaster:
		master, err := n.link(r.Inputs["master"])
		if err != nil {
			return err
		}
		if l.Attrs().MasterIndex != master.Attrs().Index {
			return fmt.Errorf("master differs")
		}
	case CreateVLAN:
		v, ok := l.(*netlink.Vlan)
		parent, err := n.link(r.Inputs["parent"])
		if err != nil {
			return err
		}
		if !ok || v.VlanId != c.VlanID || v.ParentIndex != parent.Attrs().Index {
			return fmt.Errorf("VLAN parent/tag differs")
		}
	case CreateMacvlan:
		v, ok := l.(*netlink.Macvlan)
		if !ok || v.Mode != netlink.MACVLAN_MODE_BRIDGE {
			return fmt.Errorf("macvlan mode differs")
		}
	case CreateIPvlan:
		v, ok := l.(*netlink.IPVlan)
		if !ok || v.Mode != netlink.IPVLAN_MODE_L2 {
			return fmt.Errorf("ipvlan mode differs")
		}
	}
	return nil
}
func (n *Native) Verify(ex Execution) error {
	for _, r := range ex.Records {
		if r.State != Applied {
			return fmt.Errorf("E_EFFECT_NOT_APPLIED")
		}
		if err := n.observe(r); err != nil {
			return err
		}
	}
	for _, h := range ex.Outputs {
		if h.Type == topology.AddressLeaseType {
			continue
		}
		if h.Type == topology.RDMAType {
			if err := VerifyRDMADevice(h); err != nil {
				return err
			}
			continue
		}
		if h.Type == topology.VFIOType {
			if err := verifyDevice(h); err != nil {
				return err
			}
			continue
		}
		if _, err := n.link(h); err != nil {
			return err
		}
	}
	return nil
}
func (n *Native) Restore(ex *Execution, save func() error) error {
	for i := len(ex.Records) - 1; i >= 0; i-- {
		r := &ex.Records[i]
		if r.State == Restored {
			continue
		}
		if r.Effect.Kind == topology.IPv4Forwarding {
			if err := n.restoreIPv4Forwarding(*r); err != nil {
				return err
			}
			r.State = Restored
			if err := save(); err != nil {
				return err
			}
			continue
		}
		if r.Effect.Kind == topology.AddressConfigure {
			if err := n.restoreAddress(*r); err != nil {
				return err
			}
			r.State = Restored
			if err := save(); err != nil {
				return err
			}
			continue
		}
		if r.Effect.Kind == topology.OVSConfigure {
			if err := n.restoreOVS(r, save); err != nil {
				return err
			}
			r.State = Restored
			if err := save(); err != nil {
				return err
			}
			continue
		}
		if r.Effect.Kind == topology.UplinkConfigure || r.Effect.Kind == topology.RepresentorMap || r.Effect.Kind == topology.BridgeConfigure {
			if r.Effect.Kind == topology.UplinkConfigure {
				if err := n.restoreUplink(r); err != nil {
					return err
				}
			}
			if r.Effect.Kind == topology.BridgeConfigure {
				if err := n.restoreBridge(r, save); err != nil {
					return err
				}
			}
			r.State = Restored
			if err := save(); err != nil {
				return err
			}
			continue
		}
		if r.Effect.Kind == topology.RDMANamespace {
			if err := n.restoreRDMA(r, save); err != nil {
				return err
			}
			r.State = Restored
			if err := save(); err != nil {
				return err
			}
			continue
		}
		if r.Effect.Kind == topology.DriverBind || r.Effect.Kind == topology.DeviceAccess || r.Effect.Kind == topology.VFConfigure || r.Effect.Kind == topology.PCIReset {
			if err := n.restoreHardware(r, save); err != nil {
				return err
			}
			r.State = Restored
			if err := save(); err != nil {
				return err
			}
			continue
		}
		l, scope, err := n.owned(r.Output.Alias)
		if err != nil {
			return err
		}
		switch r.Effect.Kind {
		case Move:
			if err := ethernetScope(r.Inputs[InterfacePort]); err != nil {
				return err
			}
			if l == nil {
				l, err = n.link(r.Inputs[InterfacePort])
				if err != nil {
					return fmt.Errorf("E_RESTORE_AMBIGUOUS: %w", err)
				}
				if state(l) != *r.Before {
					return fmt.Errorf("E_RESTORE_AMBIGUOUS: original state differs")
				}
			} else {
				if scope != StageScope {
					if err := n.scope(scope).LinkSetDown(l); err != nil {
						return err
					}
					if err := n.scope(scope).LinkSetNoMaster(l); err != nil {
						return err
					}
					if err := n.scope(scope).LinkSetNsFd(l, int(n.stageNS)); err != nil {
						return err
					}
				}
				l, _, err = n.owned(r.Output.Alias)
				if err != nil {
					return err
				}
				for _, alt := range l.Attrs().AltNames {
					if alt == r.Before.Name {
						if err := n.stage.LinkDelAltName(l, alt); err != nil {
							return err
						}
					}
				}
				if err := n.stage.LinkSetName(l, r.Before.Name); err != nil {
					return err
				}
				if err := n.stage.LinkSetNsFd(l, int(n.hostNS)); err != nil {
					return err
				}
				l, err = n.host.LinkByName(r.Before.Name)
				if err != nil {
					return err
				}
				if err := restoreState(n.host, l, *r.Before); err != nil {
					return err
				}
			}
		case Configure, AttachMaster:
			if l != nil {
				if scope != SandboxScope {
					return fmt.Errorf("E_RESTORE_SCOPE")
				}
				if r.Effect.Kind == AttachMaster {
					if err := n.sandbox.LinkSetDown(l); err != nil {
						return err
					}
					if err := n.sandbox.LinkSetNoMaster(l); err != nil {
						return err
					}
				}
				if err := restoreState(n.sandbox, l, *r.Before); err != nil {
					return err
				}
			}
		default:
			if l == nil && r.Created != nil {
				candidate, lookupErr := n.sandbox.LinkByName(r.Created.Name)
				var missing netlink.LinkNotFoundError
				if lookupErr != nil && !errors.As(lookupErr, &missing) {
					return lookupErr
				}
				if lookupErr == nil {
					if candidate.Attrs().Index != r.Created.IfIndex || candidate.Type() != r.Created.Type || candidate.Attrs().Alias != "" {
						return fmt.Errorf("E_CREATE_AMBIGUOUS")
					}
					l, scope = candidate, SandboxScope
				}
			}
			if l == nil && r.Created == nil {
				_, lookupErr := n.sandbox.LinkByName("mn" + topology.Digest(r.Output.Alias)[:12])
				var missing netlink.LinkNotFoundError
				if lookupErr == nil {
					return fmt.Errorf("E_CREATE_AMBIGUOUS: creation lacks committed locator")
				}
				if !errors.As(lookupErr, &missing) {
					return lookupErr
				}
			}
			if l != nil {
				if scope != SandboxScope {
					return fmt.Errorf("E_RESTORE_SCOPE")
				}
				if err := n.sandbox.LinkDel(l); err != nil {
					return err
				}
			}
		}
		r.State = Restored
		if err := save(); err != nil {
			return err
		}
	}
	return nil
}
func restoreState(h *netlink.Handle, l netlink.Link, b LinkState) error {
	if err := h.LinkSetDown(l); err != nil {
		return err
	}
	if l.Attrs().Name != b.Name {
		if err := h.LinkSetName(l, b.Name); err != nil {
			return err
		}
	}
	if err := h.LinkSetMTU(l, b.MTU); err != nil {
		return err
	}
	if b.MAC != "" {
		mac, err := net.ParseMAC(b.MAC)
		if err != nil {
			return err
		}
		if l.Attrs().HardwareAddr.String() != b.MAC {
			if err := h.LinkSetHardwareAddr(l, mac); err != nil {
				return err
			}
		}
	}
	if err := h.LinkSetAlias(l, b.Alias); err != nil {
		return err
	}
	if b.Up {
		return h.LinkSetUp(l)
	}
	return nil
}

func BootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(data)), err
}

func (n *Native) preflightHostLink(root Handle) error {
	if !root.Shared {
		if err := ethernetScope(root); err != nil {
			return err
		}
	}
	l, err := n.link(root)
	if err != nil {
		return err
	}
	return n.preflightLinkUsers(l, root.Shared, 0)
}

func (n *Native) preflightLinkUsers(l netlink.Link, shared bool, allowedMaster int) error {
	a := l.Attrs()
	if a.MasterIndex != 0 && a.MasterIndex != allowedMaster {
		return fmt.Errorf("E_EXTERNAL_MASTER: %s", a.Name)
	}
	addresses, err := n.host.AddrList(l, netlink.FAMILY_ALL)
	if err != nil {
		return err
	}
	for _, addr := range addresses {
		if !shared && addr.IP.IsGlobalUnicast() {
			return fmt.Errorf("E_HOST_ADDRESS: %s", a.Name)
		}
	}
	routes, err := n.host.RouteList(l, netlink.FAMILY_ALL)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if route.Dst == nil || route.Dst.IP.IsUnspecified() {
			return fmt.Errorf("E_PROTECTED_ROUTE: %s", a.Name)
		}
	}
	return nil
}

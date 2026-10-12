// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

// QPN/ifindex are recreated by the kernel's IPoIB client at HCA handoff.
// The GID, physical port and P_Key remain the interface's stable identity.
type IPoIBState struct {
	Link      LinkState         `json:"link"`
	GID       string            `json:"gid"`
	Port      uint32            `json:"port"`
	PKey      uint16            `json:"pkey"`
	Mode      netlink.IPoIBMode `json:"mode"`
	UMcast    uint16            `json:"umcast"`
	ActiveMTU int               `json:"activeMTU"`
}

func validateRDMAConfigGrant(h Handle, c topology.Config) error {
	if !topology.IBProfile(c.RDMAProfile) {
		if c.MADAccess {
			return fmt.Errorf("E_RDMA_MAD_GRANT: MAD requires an IB profile")
		}
		return validateRoCEGrant(h, c.RDMAProfile)
	}
	if h.Type != topology.RDMAType || h.Shared || h.RDMA == nil || h.PCIAddress != h.RDMA.PCI || !pciAddress.MatchString(h.PCIAddress) || !slices.Contains(h.AllowedRDMAProfiles, c.RDMAProfile) || c.MADAccess && !h.AllowedMADAccess {
		return fmt.Errorf("E_IB_GRANT: exclusive certified IB profile and explicit MAD grant required")
	}
	d := h.RDMA
	if d.Namespace == nil || d.NodeGUID == "" || d.SystemImageGUID == "" || len(d.RelatedPCI) != 1 || d.RelatedPCI[0] != d.PCI || len(d.Ports) != 1 || d.Ports[0].Number != 1 || d.Ports[0].LinkLayer != topology.InfiniBand {
		return fmt.Errorf("E_IB_SCOPE: complete single-function/single-port HCA required")
	}
	if c.RDMAProfile == topology.IBVerbsSinglePort {
		if len(d.NetDevices) != 0 || h.Name != "" || c.IfName != "" || c.MTU != 0 || c.Up {
			return fmt.Errorf("E_IB_VERBS_SCOPE: verbs-only profile requires no kernel netdevice")
		}
	} else if len(d.NetDevices) != 1 || d.NetDevices[0] != h.Name {
		return fmt.Errorf("E_IPOIB_SCOPE: exactly one base IPoIB interface required")
	}
	return nil
}

func (s IPoIBState) validateMTU(mtu int) error {
	maximum := s.ActiveMTU - 4
	if s.Mode == netlink.IPOIB_MODE_CONNECTED {
		maximum = 65520
	}
	if mtu < 256 || mtu > maximum {
		return fmt.Errorf("E_IPOIB_MTU: %d exceeds the recorded port/mode profile", mtu)
	}
	return nil
}

func readIPoIB(sysRoot string, link netlink.Link, d RDMADevice) (*IPoIBState, error) {
	ip, ok := link.(*netlink.IPoIB)
	if !ok || len(d.Ports) != 1 || d.Ports[0].LinkLayer != topology.InfiniBand || d.Ports[0].Number != 1 || len(d.NetDevices) != 1 || d.NetDevices[0] != ip.Name || ip.ParentIndex != 0 || ip.MasterIndex != 0 || len(ip.HardwareAddr) != 20 || ip.Mode > netlink.IPOIB_MODE_CONNECTED || ip.Umcast > 1 {
		return nil, fmt.Errorf("E_IPOIB_SCOPE: require one unenslaved base IB interface")
	}
	base := filepath.Join(sysRoot, "class/net", ip.Name)
	owner, err := filepath.EvalSymlinks(filepath.Join(base, "device"))
	hcaOwner, hcaErr := filepath.EvalSymlinks(filepath.Join(sysRoot, "class/infiniband", d.Name, "device"))
	if err != nil || hcaErr != nil || owner != hcaOwner {
		return nil, errors.Join(fmt.Errorf("E_IPOIB_HCA_IDENTITY"), err, hcaErr)
	}
	for key, expected := range map[string]string{"type": "32", "dev_port": "0"} {
		value, err := sysText(filepath.Join(base, key))
		if err != nil || value != expected {
			return nil, errors.Join(fmt.Errorf("E_IPOIB_IDENTITY: %s differs", key), err)
		}
	}
	mode, err := sysText(filepath.Join(base, "mode"))
	if err != nil && !(os.IsNotExist(err) && ip.Mode == netlink.IPOIB_MODE_DATAGRAM) || err == nil && mode != ip.Mode.String() {
		return nil, errors.Join(fmt.Errorf("E_IPOIB_MODE"), err)
	}
	value, err := sysText(filepath.Join(base, "pkey"))
	if err != nil {
		return nil, err
	}
	pkey, err := strconv.ParseUint(value, 0, 16)
	portKey, known := d.Ports[0].PKeys[0]
	if err != nil || !known || pkey == 0 || pkey&0x7fff == 0 || uint16(pkey) != ip.Pkey || portKey != ip.Pkey {
		return nil, fmt.Errorf("E_IPOIB_PKEY: base interface must match port P_Key index zero")
	}
	portPath := filepath.Join(sysRoot, "class/infiniband", d.Name, "ports/1")
	value, err = sysText(filepath.Join(portPath, "gids/0"))
	if err != nil {
		return nil, err
	}
	gid := strings.ToLower(strings.ReplaceAll(value, ":", ""))
	if gid == strings.Repeat("0", 32) || gid != hex.EncodeToString(ip.HardwareAddr[4:]) {
		return nil, fmt.Errorf("E_IPOIB_GID: hardware address differs from physical port GID")
	}
	value, err = sysText(filepath.Join(portPath, "active_mtu"))
	if err != nil {
		return nil, err
	}
	_, mtuText, found := strings.Cut(value, ":")
	mtu, err := strconv.Atoi(strings.TrimSpace(mtuText))
	if !found || err != nil || !slices.Contains([]int{256, 512, 1024, 2048, 4096}, mtu) {
		return nil, fmt.Errorf("E_IPOIB_PORT_MTU: unavailable active IB MTU")
	}
	result := &IPoIBState{Link: state(link), GID: gid, Port: 1, PKey: ip.Pkey, Mode: ip.Mode, UMcast: ip.Umcast, ActiveMTU: mtu}
	if err := result.validateMTU(result.Link.MTU); err != nil {
		return nil, err
	}
	return result, nil
}

func captureIPoIB(ns netns.NsHandle, h *netlink.Handle, d RDMADevice) (*IPoIBState, error) {
	if len(d.NetDevices) != 1 {
		return nil, fmt.Errorf("E_IPOIB_SCOPE: exactly one base interface required")
	}
	link, err := h.LinkByName(d.NetDevices[0])
	if err != nil {
		return nil, err
	}
	var result *IPoIBState
	err = withRDMASysfs(ns, func(root string) error { var err error; result, err = readIPoIB(root, link, d); return err })
	return result, err
}

func sameIPoIBIdentity(a, b IPoIBState) bool {
	return a.GID == b.GID && a.Port == b.Port && a.PKey == b.PKey && a.ActiveMTU == b.ActiveMTU
}

func sameIPoIBState(a, b IPoIBState) bool {
	// The first four address bytes contain kernel-managed flags/QPN.
	a.Link.MAC, b.Link.MAC = "", ""
	return sameIPoIBIdentity(a, b) && a.Link == b.Link && a.Mode == b.Mode && a.UMcast == b.UMcast
}

func verifyIPoIBOwner(alias string, index int, output Handle, committed bool) error {
	if output.Alias == "" || output.IfIndex != 0 && output.IfIndex != index || alias != "" && ownershipAlias(alias) != output.Alias || committed && ownershipAlias(alias) != output.Alias {
		return fmt.Errorf("E_IPOIB_OWNERSHIP")
	}
	return nil
}

// Recreated netdevices are discovered through the HCA and stable IB identity,
// never by a remembered ifindex or an administrator-supplied interface name.
func (n *Native) configureIPoIB(ns netns.NsHandle, h *netlink.Handle, d RDMADevice, before IPoIBState, output *Handle, desired LinkState, save func() error) error {
	current, err := captureIPoIB(ns, h, d)
	if err != nil {
		return err
	}
	if !sameIPoIBIdentity(*current, before) {
		return fmt.Errorf("E_IPOIB_INCARNATION")
	}
	link, err := h.LinkByName(current.Link.Name)
	if err != nil {
		return err
	}
	if err := verifyIPoIBOwner(current.Link.Alias, link.Attrs().Index, *output, false); err != nil {
		return err
	}
	if other, err := h.LinkByName(desired.Name); err == nil {
		if other.Attrs().Index != link.Attrs().Index {
			return fmt.Errorf("E_IPOIB_NAME_COLLISION")
		}
	} else {
		var missing netlink.LinkNotFoundError
		if !errors.As(err, &missing) {
			return err
		}
	}
	output.IfIndex = link.Attrs().Index
	if err := save(); err != nil {
		return err
	}
	if err := h.LinkSetAlias(link, output.Alias); err != nil {
		return err
	}
	if err := h.LinkSetDown(link); err != nil {
		return err
	}
	ip := link.(*netlink.IPoIB)
	if ip.Mode != before.Mode || ip.Umcast != before.UMcast {
		ip.Mode, ip.Umcast = before.Mode, before.UMcast
		if err := h.LinkModify(ip); err != nil {
			return err
		}
	}
	if current.Link.Name != desired.Name {
		if err := h.LinkSetName(link, desired.Name); err != nil {
			return err
		}
	}
	if err := h.LinkSetMTU(link, desired.MTU); err != nil {
		return err
	}
	if desired.Up {
		err = h.LinkSetUp(link)
	} else {
		err = h.LinkSetDown(link)
	}
	return err
}

func rdmaProfileAccessNodes(d RDMADevice, mad bool) ([]DeviceNode, error) {
	nodes, err := rdmaAccessNodes(d)
	if err != nil || !mad {
		return nodes, err
	}
	char, err := rdmaMADChar(d)
	if err != nil {
		return nil, err
	}
	node, err := verifiedRDMAChar(char)
	if err != nil {
		return nil, err
	}
	return append(nodes, node), nil
}

func rdmaMADChar(d RDMADevice) (RDMAChar, error) {
	if len(d.Ports) != 1 || d.Ports[0].Number != 1 || d.Ports[0].LinkLayer != topology.InfiniBand {
		return RDMAChar{}, fmt.Errorf("E_RDMA_MAD_SCOPE")
	}
	var selected []RDMAChar
	for _, char := range d.CharDevices {
		if char.Class == rdmaMADClass && strings.HasPrefix(char.Name, "umad") {
			number, err := strconv.Atoi(strings.TrimPrefix(char.Name, "umad"))
			if char.Port != 1 || err != nil || number < 0 {
				return RDMAChar{}, fmt.Errorf("E_RDMA_MAD_SCOPE: foreign port or invalid node")
			}
			selected = append(selected, char)
		}
	}
	if len(selected) != 1 {
		return RDMAChar{}, fmt.Errorf("E_RDMA_MAD_SCOPE: require one private UMAD node")
	}
	return selected[0], nil
}

func (n *Native) applyIB(r *Record, save func() error) error {
	s := r.RDMA
	if s == nil || s.Profile != r.Effect.Parameters.RDMAProfile || s.MADAccess != r.Effect.Parameters.MADAccess || s.RestoreAttempted || (s.Profile == topology.IPoIBSinglePort) != (s.IPoIB != nil) {
		return fmt.Errorf("E_IB_BEFORE_IMAGE")
	}
	c := r.Effect.Parameters
	if s.IPoIB != nil {
		if !topology.ValidIfName(c.IfName) {
			return fmt.Errorf("E_IPOIB_CONFIG")
		}
		if err := s.IPoIB.validateMTU(c.MTU); err != nil {
			return err
		}
	}
	if err := s.verifyPhysical(); err != nil {
		return err
	}
	if s.Connected {
		return n.observeIB(*r)
	}
	host, err := n.rdmaLocation(n.hostNS, *s)
	if err != nil {
		return err
	}
	sandbox, err := n.rdmaLocation(n.sandboxNS, *s)
	if err != nil || (host == nil) == (sandbox == nil) {
		return errors.Join(fmt.Errorf("E_IB_LOCATION"), err)
	}
	if host != nil {
		if s.IPoIB == nil && len(host.NetDevices) != 0 {
			return fmt.Errorf("E_IB_VERBS_SCOPE: unexpected kernel netdevice before handoff")
		}
		if err := emptyRDMANamespace(n.sandboxNS); err != nil {
			return err
		}
		if s.IPoIB != nil {
			current, err := captureIPoIB(n.hostNS, n.host, *host)
			if err != nil || current == nil || !sameIPoIBState(*current, *s.IPoIB) {
				return errors.Join(fmt.Errorf("E_IPOIB_BEFORE_IMAGE: host profile changed"), err)
			}
		}
		if err := moveRDMA(n.hostNS, n.sandboxNS, *host); err != nil {
			return err
		}
		sandbox, err = rdmaIdentityIn(n.sandboxNS, s.Before)
		if err != nil || sandbox == nil {
			return errors.Join(fmt.Errorf("E_IB_HANDOFF_IDENTITY"), err)
		}
	}
	if s.IPoIB != nil {
		desired := s.IPoIB.Link
		desired.Name, desired.MTU, desired.Up = c.IfName, c.MTU, c.Up
		if err := n.configureIPoIB(n.sandboxNS, n.sandbox, *sandbox, *s.IPoIB, &s.Kernel.Output, desired, save); err != nil {
			return err
		}
	} else if len(sandbox.NetDevices) != 0 {
		return fmt.Errorf("E_IB_VERBS_SCOPE: unexpected IPoIB client")
	}
	current, nodes, err := waitRDMANodes(n.sandboxNS, s.Before, s.MADAccess)
	if err != nil {
		return err
	}
	r.Output = Handle{Type: topology.RDMAType, Scope: SandboxScope, Name: current.Name, PCIAddress: current.PCI, RDMA: current, NamespacePath: n.sandboxPath, Devices: nodes, MADAccess: s.MADAccess}
	if s.IPoIB != nil {
		r.Outputs = map[string]Handle{InterfacePort: s.Kernel.Output}
	}
	s.Connected = true
	return save()
}

func (n *Native) observeIB(r Record) error {
	s := r.RDMA
	if s == nil || !s.Connected {
		return fmt.Errorf("E_IB_NOT_CONNECTED")
	}
	if err := s.verifyPhysical(); err != nil {
		return err
	}
	if err := VerifyRDMADevice(r.Output); err != nil {
		return err
	}
	if r.Output.MADAccess != s.MADAccess {
		return fmt.Errorf("E_RDMA_MAD_GRANT")
	}
	if s.IPoIB == nil {
		if len(r.Output.RDMA.NetDevices) != 0 || len(r.Outputs) != 0 {
			return fmt.Errorf("E_IB_VERBS_SCOPE")
		}
		return nil
	}
	current, err := captureIPoIB(n.sandboxNS, n.sandbox, *r.Output.RDMA)
	if err != nil {
		return err
	}
	desired := *s.IPoIB
	desired.Link.Name, desired.Link.MTU, desired.Link.Up, desired.Link.Alias = r.Effect.Parameters.IfName, r.Effect.Parameters.MTU, r.Effect.Parameters.Up, s.Kernel.Output.Alias
	link, err := n.link(s.Kernel.Output)
	if err != nil || link == nil || !sameIPoIBState(*current, desired) || topology.Digest(r.Outputs[InterfacePort]) != topology.Digest(s.Kernel.Output) {
		return errors.Join(fmt.Errorf("E_IPOIB_POSTCONDITION"), err)
	}
	return nil
}

func (n *Native) restoreIB(r *Record, save func() error) error {
	s := r.RDMA
	if s == nil || !topology.IBProfile(s.Profile) || (s.Profile == topology.IPoIBSinglePort) != (s.IPoIB != nil) {
		return fmt.Errorf("E_IB_BEFORE_IMAGE")
	}
	if err := s.verifyPhysical(); err != nil {
		return err
	}
	host, err := n.rdmaLocation(n.hostNS, *s)
	if err != nil {
		return err
	}
	sandbox, err := n.rdmaLocation(n.sandboxNS, *s)
	if err != nil || (host == nil) == (sandbox == nil) {
		return errors.Join(fmt.Errorf("E_IB_RESTORE_LOCATION"), err)
	}
	if sandbox != nil {
		if s.IPoIB != nil {
			current, err := captureIPoIB(n.sandboxNS, n.sandbox, *sandbox)
			if err != nil || current == nil || !sameIPoIBIdentity(*current, *s.IPoIB) {
				return errors.Join(fmt.Errorf("E_IPOIB_RESTORE_IDENTITY"), err)
			}
			link, err := n.sandbox.LinkByName(current.Link.Name)
			if err != nil {
				return err
			}
			if err := verifyIPoIBOwner(current.Link.Alias, link.Attrs().Index, s.Kernel.Output, s.Connected); err != nil {
				return err
			}
		}
		s.RestoreAttempted = true
		if err := save(); err != nil {
			return err
		}
		if err := moveRDMA(n.sandboxNS, n.hostNS, *sandbox); err != nil {
			return err
		}
		host, err = rdmaIdentityIn(n.hostNS, s.Before)
		if err != nil || host == nil {
			return errors.Join(fmt.Errorf("E_IB_RESTORE_IDENTITY"), err)
		}
	} else if s.Connected && !s.RestoreAttempted {
		return fmt.Errorf("E_IB_RESTORE_UNCERTAIN")
	}
	if err := noRDMAUsers(n.hostNS, *host); err != nil {
		return err
	}
	if s.IPoIB != nil {
		if s.IPoIBRestore == nil {
			s.IPoIBRestore = &Handle{Type: topology.InterfaceType, Scope: HostScope, PCIAddress: host.PCI, Alias: s.Kernel.Output.Alias + ":restore", Name: s.IPoIB.Link.Name}
			if err := save(); err != nil {
				return err
			}
		}
		if s.Kernel.State != Restored {
			if err := n.configureIPoIB(n.hostNS, n.host, *host, *s.IPoIB, s.IPoIBRestore, s.IPoIB.Link, save); err != nil {
				return err
			}
			// Checkpoint restored properties before clearing the ownership marker.
			// A lost acknowledgement must observe them, not overwrite host changes.
			s.Kernel.State = Restored
			if err := save(); err != nil {
				return err
			}
		}
		host, err = rdmaIdentityIn(n.hostNS, s.Before)
		if err != nil || host == nil {
			return errors.Join(fmt.Errorf("E_IB_RESTORE_IDENTITY"), err)
		}
		current, err := captureIPoIB(n.hostNS, n.host, *host)
		if err != nil {
			return err
		}
		expected := *s.IPoIB
		if current.Link.Alias == s.IPoIBRestore.Alias {
			expected.Link.Alias = s.IPoIBRestore.Alias
		}
		if !sameIPoIBState(*current, expected) {
			return fmt.Errorf("E_IPOIB_RESTORE_PROFILE: unrecorded host changes")
		}
		link, err := n.host.LinkByIndex(s.IPoIBRestore.IfIndex)
		if err != nil {
			return err
		}
		if link.Attrs().Name != current.Link.Name || link.Attrs().Alias != current.Link.Alias {
			return fmt.Errorf("E_IPOIB_RESTORE_INCARNATION")
		}
		if err := n.host.LinkSetAlias(link, s.IPoIB.Link.Alias); err != nil {
			return err
		}
		host, err = rdmaIdentityIn(n.hostNS, s.Before)
		if err != nil || host == nil {
			return errors.Join(fmt.Errorf("E_IB_RESTORE_IDENTITY"), err)
		}
		current, err = captureIPoIB(n.hostNS, n.host, *host)
		if err != nil || current == nil || !sameIPoIBState(*current, *s.IPoIB) {
			return errors.Join(fmt.Errorf("E_IPOIB_RESTORE_PROFILE"), err)
		}
	} else if len(host.NetDevices) != 0 {
		return fmt.Errorf("E_IB_VERBS_SCOPE")
	}
	s.RestoredDevice = host
	return save()
}

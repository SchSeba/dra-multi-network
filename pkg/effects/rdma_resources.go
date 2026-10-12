// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

// Stable include/uapi/rdma/rdma_netlink.h values missing from our netlink
// library. These are device-scoped resource dumps, never an unscoped socket.
const (
	rdmaResourcePID        = 28
	rdmaResourceKernelName = 29
	resourceQP             = "qp"
	// ponytail: at most 4096 resources per certified single-port baseline;
	// certify larger complete inventories before raising the bound.
	maxIBResources = 4096
	nlaTypeMask    = ^uint16(unix.NLA_F_NESTED | unix.NLA_F_NET_BYTEORDER)
)

type rdmaResourceKind struct {
	Name                  string
	Command, Table, Entry int
	ID                    int
}

var ibResourceKinds = []rdmaResourceKind{
	{Name: resourceQP, Command: 10, Table: 19, Entry: 20, ID: 21},
	{Name: "cq", Command: 12, Table: 35, Entry: 36, ID: 61},
	{Name: "mr", Command: 13, Table: 40, Entry: 41, ID: 62},
	{Name: "pd", Command: 14, Table: 46, Entry: 47, ID: 60},
}

var requiredRDMAResources = []string{resourceQP, "cq", "mr", "pd", "cm_id", "ctx"}

type rdmaResourceOwner struct {
	ID         uint32
	Port       uint32
	KernelName string
	User       bool
}

func resourceAttributes(data []byte, wanted ...int) (map[int][]byte, error) {
	attrs, err := nl.ParseRouteAttr(data)
	if err != nil {
		return nil, err
	}
	values := map[int][]byte{}
	for _, attr := range attrs {
		kind := int(attr.Attr.Type & nlaTypeMask)
		if !slices.Contains(wanted, kind) {
			continue
		}
		if _, duplicate := values[kind]; duplicate || attr.Attr.Type&unix.NLA_F_NET_BYTEORDER != 0 {
			return nil, fmt.Errorf("E_RDMA_RESOURCE_ENCODING: duplicate or non-native attribute %d", kind)
		}
		values[kind] = attr.Value
	}
	return values, nil
}

func resourceUint32(data []byte) (uint32, error) {
	if len(data) != 4 {
		return 0, fmt.Errorf("E_RDMA_RESOURCE_ENCODING: expected u32")
	}
	return nl.NativeEndian().Uint32(data), nil
}

func resourceString(data []byte) (string, error) {
	if len(data) < 2 || len(data) > 128 || data[len(data)-1] != 0 || bytes.IndexByte(data[:len(data)-1], 0) >= 0 {
		return "", fmt.Errorf("E_RDMA_RESOURCE_ENCODING: expected bounded NUL-terminated name")
	}
	return string(data[:len(data)-1]), nil
}

func parseRDMAResourceOwners(data []byte, kind rdmaResourceKind, expected RDMADevice) ([]rdmaResourceOwner, error) {
	attrs, err := resourceAttributes(data, nl.RDMA_NLDEV_ATTR_DEV_INDEX, nl.RDMA_NLDEV_ATTR_DEV_NAME, kind.Table)
	if err != nil {
		return nil, err
	}
	index, err := resourceUint32(attrs[nl.RDMA_NLDEV_ATTR_DEV_INDEX])
	if err != nil || index != expected.Index {
		return nil, errors.Join(fmt.Errorf("E_RDMA_RESOURCE_IDENTITY: device index differs"), err)
	}
	name, err := resourceString(attrs[nl.RDMA_NLDEV_ATTR_DEV_NAME])
	if err != nil || name != expected.Name {
		return nil, errors.Join(fmt.Errorf("E_RDMA_RESOURCE_IDENTITY: device name differs"), err)
	}
	table, exists := attrs[kind.Table]
	if !exists {
		return nil, fmt.Errorf("E_RDMA_RESOURCE_ENCODING: missing resource table")
	}
	entries, err := nl.ParseRouteAttr(table)
	if err != nil {
		return nil, err
	}
	var owners []rdmaResourceOwner
	for _, entry := range entries {
		if int(entry.Attr.Type&nlaTypeMask) != kind.Entry {
			return nil, fmt.Errorf("E_RDMA_RESOURCE_ENCODING: unexpected resource entry")
		}
		values, err := resourceAttributes(entry.Value, kind.ID, rdmaResourcePID, rdmaResourceKernelName, nl.RDMA_NLDEV_ATTR_PORT_INDEX)
		if err != nil {
			return nil, err
		}
		id, err := resourceUint32(values[kind.ID])
		if err != nil {
			return nil, err
		}
		owner := rdmaResourceOwner{ID: id}
		if pid, user := values[rdmaResourcePID]; user {
			if _, err := resourceUint32(pid); err != nil {
				return nil, err
			}
			owner.User = true // PID zero does not confer kernel authority.
		}
		if name, kernel := values[rdmaResourceKernelName]; kernel {
			owner.KernelName, err = resourceString(name)
			if err != nil {
				return nil, err
			}
		}
		if port, known := values[nl.RDMA_NLDEV_ATTR_PORT_INDEX]; known {
			owner.Port, err = resourceUint32(port)
			if err != nil {
				return nil, err
			}
		}
		owners = append(owners, owner)
	}
	return owners, nil
}

func rdmaResourceSocket(ns netns.NsHandle) (*nl.NetlinkSocket, error) {
	socket, err := nl.GetNetlinkSocketAt(ns, netns.None(), unix.NETLINK_RDMA)
	if err != nil {
		return nil, err
	}
	timeout := unix.Timeval{Sec: 1}
	if err := errors.Join(socket.SetReceiveTimeout(&timeout), socket.SetSendTimeout(&timeout)); err != nil {
		return nil, errors.Join(err, socket.Close())
	}
	return socket, nil
}

func rdmaResourceOwners(ns netns.NsHandle, kind rdmaResourceKind, expected RDMADevice) ([]rdmaResourceOwner, error) {
	socket, err := rdmaResourceSocket(ns)
	if err != nil {
		return nil, err
	}
	defer socket.Close()
	proto := nl.RDMA_NL_NLDEV<<nl.RDMA_NL_GET_CLIENT_SHIFT | kind.Command
	request := nl.NewNetlinkRequest(proto, unix.NLM_F_ACK|unix.NLM_F_DUMP)
	request.Sockets = map[int]*nl.SocketHandle{unix.NETLINK_RDMA: {Socket: socket}}
	request.AddData(nl.NewRtAttr(nl.RDMA_NLDEV_ATTR_DEV_INDEX, nl.Uint32Attr(expected.Index)))
	var owners []rdmaResourceOwner
	var parseErr error
	err = request.ExecuteIter(unix.NETLINK_RDMA, 0, func(message []byte) bool {
		if len(message) > 1024*1024 {
			parseErr = fmt.Errorf("E_RDMA_RESOURCE_SIZE")
			return false
		}
		part, err := parseRDMAResourceOwners(message, kind, expected)
		if err != nil {
			parseErr = err
			return false
		}
		owners = append(owners, part...)
		if len(owners) > maxIBResources {
			parseErr = fmt.Errorf("E_RDMA_RESOURCE_SIZE")
			return false
		}
		return true
	})
	if err == nil && parseErr == nil && len(owners) == 0 {
		return nil, fmt.Errorf("E_RDMA_RESOURCE_IDENTITY: no owner records for a nonempty summary")
	}
	return owners, errors.Join(err, parseErr)
}

func ibResourceCounts(resources []*netlink.RdmaResource, expected RDMADevice) (map[string]uint64, error) {
	if !pciAddress.MatchString(expected.PCI) || len(expected.Ports) != 1 || expected.Ports[0].Number != 1 || expected.Ports[0].LinkLayer != "infiniband" || len(expected.NetDevices) > 1 || len(expected.RelatedPCI) != 1 || expected.RelatedPCI[0] != expected.PCI {
		return nil, fmt.Errorf("E_RDMA_RESOURCE_SCOPE: complete single-port IB function required")
	}
	var counts map[string]uint64
	found := false
	for _, resource := range resources {
		if resource == nil {
			return nil, fmt.Errorf("E_RDMA_QUIESCENCE: missing resource observation")
		}
		if resource.Index == expected.Index || resource.Name == expected.Name {
			if found || resource.Index != expected.Index || resource.Name != expected.Name {
				return nil, fmt.Errorf("E_RDMA_RESOURCE_IDENTITY: ambiguous resource summary")
			}
			counts = resource.RdmaResourceSummaryEntries
			found = true
		}
	}
	for _, required := range requiredRDMAResources {
		if _, known := counts[required]; !known {
			return nil, fmt.Errorf("E_RDMA_QUIESCENCE: missing %s tracking", required)
		}
	}
	for name, count := range counts {
		if count > maxIBResources || count != 0 && !slices.ContainsFunc(ibResourceKinds, func(kind rdmaResourceKind) bool { return kind.Name == name }) {
			return nil, fmt.Errorf("E_RDMA_ACTIVE: unverified %s=%d", name, count)
		}
	}
	return counts, nil
}

func verifyIBResourceOwners(kind rdmaResourceKind, owners []rdmaResourceOwner, count uint64, expected RDMADevice) error {
	if uint64(len(owners)) != count || count > maxIBResources {
		return fmt.Errorf("E_RDMA_QUIESCENCE: summary and %s dump differ", kind.Name)
	}
	seen := map[uint32]bool{}
	for _, owner := range owners {
		if seen[owner.ID] || owner.User || owner.Port > 1 || owner.KernelName != "ib_mad" && (owner.KernelName != "ib_ipoib" || len(expected.NetDevices) != 1) {
			return fmt.Errorf("E_RDMA_ACTIVE: unverified %s owner", kind.Name)
		}
		seen[owner.ID] = true
	}
	return nil
}

func ibQuiescent(ns netns.NsHandle, h *netlink.Handle, resources []*netlink.RdmaResource, expected RDMADevice) error {
	before, err := ibResourceCounts(resources, expected)
	if err != nil {
		return err
	}
	for _, kind := range ibResourceKinds {
		if before[kind.Name] == 0 {
			continue
		}
		owners, err := rdmaResourceOwners(ns, kind, expected)
		if err != nil {
			return err
		}
		if err := verifyIBResourceOwners(kind, owners, before[kind.Name], expected); err != nil {
			return err
		}
	}
	after, err := h.RdmaResourceList()
	if err != nil {
		return err
	}
	counts, err := ibResourceCounts(after, expected)
	if err != nil || !maps.Equal(before, counts) {
		return errors.Join(fmt.Errorf("E_RDMA_QUIESCENCE: resource summary changed during ownership checks"), err)
	}
	return nil
}

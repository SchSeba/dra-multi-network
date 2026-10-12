// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"slices"

	"github.com/vishvananda/netlink"
)

// ProtectedLinks is shared by discovery and live host-bridge target checks.
// A default route protects every observed lower device and bridge member.
func ProtectedLinks(links []netlink.Link, routes []netlink.Route) map[int]bool {
	protected := map[int]bool{}
	for _, route := range routes {
		if route.LinkIndex > 0 && (route.Dst == nil || route.Dst.IP.IsUnspecified()) {
			protected[route.LinkIndex] = true
		}
	}
	for _, link := range links {
		if slices.Contains([]string{"cni0", "flannel.1", "ovn-k8s-mp0", "br-ex"}, link.Attrs().Name) {
			protected[link.Attrs().Index] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, link := range links {
			a := link.Attrs()
			if link.Type() != "veth" && protected[a.Index] && a.ParentIndex > 0 && !protected[a.ParentIndex] {
				protected[a.ParentIndex], changed = true, true
			}
			if a.MasterIndex > 0 && protected[a.MasterIndex] && !protected[a.Index] {
				protected[a.Index], changed = true, true
			}
		}
	}
	return protected
}

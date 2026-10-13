// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/vishvananda/netns"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func forwardingGrant(h Handle) error {
	if h.Type != topology.InterfaceType || h.Shared || h.Scope != SandboxScope || !strings.HasPrefix(h.Alias, "dra-mn:") {
		return fmt.Errorf("E_TUNING_SCOPE: require an owned private sandbox interface")
	}
	return nil
}

// Only the framework resolves the granted interface and enters its namespace.
func (n *Native) ipv4Forwarding(h Handle, value *bool) (bool, error) {
	if err := forwardingGrant(h); err != nil {
		return false, err
	}
	if !n.sandboxNS.IsOpen() || n.sandboxNS.Equal(n.hostNS) {
		return false, fmt.Errorf("E_TUNING_NAMESPACE")
	}
	link, err := n.link(h)
	if err != nil {
		return false, err
	}
	name := link.Attrs().Name
	if !topology.ValidIfName(name) || name == "all" || name == "default" {
		return false, fmt.Errorf("E_TUNING_INTERFACE: %s", name)
	}
	var actual bool
	done := make(chan error, 1)
	go func() {
		// The locked thread exits here, so its namespace cannot leak to Go workers.
		runtime.LockOSThread()
		done <- func() error {
			if err := netns.Set(n.sandboxNS); err != nil {
				return err
			}
			path := filepath.Join("/proc/sys/net/ipv4/conf", name, "forwarding")
			if value != nil {
				setting := "0"
				if *value {
					setting = "1"
				}
				if err := os.WriteFile(path, []byte(setting), 0600); err != nil {
					return err
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			switch strings.TrimSpace(string(data)) {
			case "0":
				actual = false
			case "1":
				actual = true
			default:
				return fmt.Errorf("E_TUNING_STATE: invalid IPv4 forwarding value")
			}
			if value != nil && actual != *value {
				return fmt.Errorf("E_TUNING_STATE: IPv4 forwarding differs")
			}
			return nil
		}()
	}()
	err = <-done
	return actual, err
}

func (n *Native) restoreIPv4Forwarding(r Record) error {
	if err := forwardingGrant(r.Output); err != nil {
		return err
	}
	if r.IPv4ForwardBefore == nil {
		return fmt.Errorf("E_TUNING_RECORD")
	}
	link, scope, err := n.owned(r.Output.Alias)
	if err != nil || link == nil {
		return err
	}
	if scope != SandboxScope || r.Output.IfIndex != 0 && r.Output.IfIndex != link.Attrs().Index {
		return fmt.Errorf("E_TUNING_RESTORE_IDENTITY")
	}
	_, err = n.ipv4Forwarding(r.Output, r.IPv4ForwardBefore)
	return err
}

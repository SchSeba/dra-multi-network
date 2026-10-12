// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package effects

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

// FLR is a per-function operation. Bus/slot and vendor-specific resets need a
// separately certified complete-domain contract; IOMMU membership is not one.
type FunctionResetScope struct {
	PCI             string `json:"pci"`
	Group           string `json:"group"`
	Method          string `json:"method"`
	OriginalMethods string `json:"originalMethods"`
}
type ResetState struct {
	Scope     FunctionResetScope `json:"scope"`
	Unbinding bool               `json:"unbinding"`
	Attempted bool               `json:"attempted"`
	Completed bool               `json:"completed"`
}

func ResetScope(pci, method string) (FunctionResetScope, error) {
	return resetScope("/sys", pci, method)
}
func resetScope(sysRoot, pci, method string) (FunctionResetScope, error) {
	s := FunctionResetScope{PCI: pci, Method: method}
	if method != topology.FLR {
		return s, fmt.Errorf("E_RESET_METHOD: require isolated function-level reset")
	}
	group, err := singleFunctionIOMMU(sysRoot, pci)
	if err != nil {
		return s, err
	}
	s.Group = group
	path := filepath.Join(sysRoot, "bus/pci/devices", pci)
	if _, err := os.Lstat(filepath.Join(path, "dep_link")); err == nil {
		return s, fmt.Errorf("E_RESET_DEPENDENCY")
	} else if !os.IsNotExist(err) {
		return s, err
	}
	methods, err := os.ReadFile(filepath.Join(path, "reset_method"))
	if err != nil {
		return s, err
	}
	s.OriginalMethods = strings.TrimSpace(string(methods))
	if !slices.Contains(strings.Fields(s.OriginalMethods), method) {
		return s, fmt.Errorf("E_RESET_METHOD: selected method not enabled")
	}
	if _, err := os.Stat(filepath.Join(path, "reset")); err != nil {
		return s, err
	}
	// An HCA can own ports outside this function's netdevice contract. Never
	// infer that it is safe to reset solely from the PCI/IOMMU function identity.
	devices, err := os.ReadDir(filepath.Join(sysRoot, "class/infiniband"))
	if err != nil && !os.IsNotExist(err) {
		return s, err
	}
	function, err := filepath.EvalSymlinks(path)
	if err != nil {
		return s, err
	}
	for _, device := range devices {
		owner, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "class/infiniband", device.Name(), "device"))
		if err != nil {
			return s, err
		}
		if owner == function || strings.HasPrefix(owner, function+string(filepath.Separator)) {
			return s, fmt.Errorf("E_RESET_RDMA_SCOPE")
		}
	}
	return s, nil
}

func (n *Native) captureReset(h Handle, method string) (*PCIState, *ResetState, error) {
	if h.Shared || h.Scope != HostScope || h.Type != topology.PCIFunctionType || !slices.Contains(h.AllowedResetMethods, method) {
		return nil, nil, fmt.Errorf("E_RESET_GRANT")
	}
	scope, err := ResetScope(h.PCIAddress, method)
	if err != nil {
		return nil, nil, err
	}
	before, err := n.capturePCI(h, "")
	if err != nil {
		return nil, nil, err
	}
	if before.Link == nil {
		return nil, nil, fmt.Errorf("E_RESET_QUIESCENCE: require an idle kernel interface")
	}
	if err := n.Preflight(Execution{Roots: map[string]Handle{"function": h}}); err != nil {
		return nil, nil, err
	}
	before.Group, before.Target = scope.Group, before.Driver
	if err := noDeviceUsers(scope.Group); err != nil {
		return nil, nil, err
	}
	return before, &ResetState{Scope: scope}, nil
}

// Intent is committed before the syscall. Only its acknowledged outcome can
// authorize forward recovery; neither retry nor cleanup repeats an uncertain reset.
func issueReset(s *ResetState, save func() error, reset func() error) error {
	if s.Attempted {
		if !s.Completed {
			return fmt.Errorf("E_RESET_UNCERTAIN: retain scope for inspection")
		}
		return nil
	}
	s.Attempted = true
	if err := save(); err != nil {
		return err
	}
	if err := reset(); err != nil {
		return err
	}
	s.Completed = true
	return save()
}

func (n *Native) applyReset(r *Record, save func() error) error {
	if r.Reset == nil || r.PCI == nil {
		return fmt.Errorf("E_RESET_BEFORE_IMAGE")
	}
	if r.Reset.Attempted && !r.Reset.Completed {
		return fmt.Errorf("E_RESET_UNCERTAIN: retain scope for inspection")
	}
	if r.State == Applied {
		return observeReset(*r)
	}
	h := r.Inputs[topology.DevicePort]
	scope := r.Reset.Scope
	if h.PCIAddress != scope.PCI || r.Effect.Parameters.ResetMethod != scope.Method || !slices.Contains(h.AllowedResetMethods, scope.Method) {
		return fmt.Errorf("E_RESET_GRANT")
	}
	if err := verifyPCI(h.PCIAddress, *r.PCI); err != nil {
		return err
	}
	if err := n.verifyPCIVF(h.PCIAddress, r.PCI.VF); err != nil {
		return err
	}
	current, err := pciDriver(h.PCIAddress)
	if err != nil {
		return err
	}
	if !r.Reset.Completed {
		if current == r.PCI.Driver {
			before, snapshot, err := n.captureReset(h, scope.Method)
			if err != nil {
				return err
			}
			expected := *r.PCI.Link
			if r.Reset.Unbinding && !before.Link.Up {
				expected.Up = false // recorded quiescence may have completed before a crash
			}
			if before.Driver != r.PCI.Driver || before.Override != r.PCI.Override || *before.Link != expected || snapshot.Scope != scope {
				return fmt.Errorf("E_RESET_INCARNATION")
			}
		} else if current != "" || !r.Reset.Unbinding {
			return fmt.Errorf("E_RESET_EXTERNAL_BINDING")
		}
		override, err := driverOverride(h.PCIAddress)
		if err != nil || override != r.PCI.Override {
			return fmt.Errorf("E_RESET_EXTERNAL_OVERRIDE: %w", err)
		}
		methods, err := readPCI(h.PCIAddress, "reset_method")
		if err != nil || methods != scope.OriginalMethods && methods != scope.Method {
			return fmt.Errorf("E_RESET_EXTERNAL_METHODS: %w", err)
		}
		if err := noDeviceUsers(scope.Group); err != nil {
			return err
		}
		r.Reset.Unbinding = true
		if err := save(); err != nil {
			return err
		}
		if current != "" {
			link, err := n.link(h)
			if err != nil {
				return err
			}
			if err := n.host.LinkSetDown(link); err != nil {
				return err
			}
			if err := writeSysfs(filepath.Join("/sys/bus/pci/drivers", current, "unbind"), h.PCIAddress); err != nil {
				return err
			}
		}
		if err := writeSysfs(filepath.Join("/sys/bus/pci/devices", h.PCIAddress, "reset_method"), scope.Method); err != nil {
			return err
		}
		if method, err := readPCI(h.PCIAddress, "reset_method"); err != nil || method != scope.Method {
			return fmt.Errorf("E_RESET_METHOD_POSTCONDITION: %w", err)
		}
		if err := issueReset(r.Reset, save, func() error { return writeSysfs(filepath.Join("/sys/bus/pci/devices", h.PCIAddress, "reset"), "1") }); err != nil {
			return err
		}
	}
	if err := n.restoreReset(r, save); err != nil {
		return err
	}
	r.Output = h
	return observeReset(*r)
}
func observeReset(r Record) error {
	if r.Reset == nil || r.PCI == nil || !r.Reset.Attempted || !r.Reset.Completed {
		return fmt.Errorf("E_RESET_UNCERTAIN: no acknowledged reset")
	}
	if err := verifyPCI(r.Reset.Scope.PCI, *r.PCI); err != nil {
		return err
	}
	methods, err := readPCI(r.Reset.Scope.PCI, "reset_method")
	if err != nil || methods != r.Reset.Scope.OriginalMethods {
		return fmt.Errorf("E_RESET_METHOD_POSTCONDITION: %w", err)
	}
	return nil
}
func (n *Native) restoreReset(r *Record, save func() error) error {
	if r.Reset == nil || r.PCI == nil {
		return fmt.Errorf("E_RESET_BEFORE_IMAGE")
	}
	if r.Reset.Attempted && !r.Reset.Completed {
		return fmt.Errorf("E_RESET_UNCERTAIN: cleanup cannot repeat or undo an uncertain reset")
	}
	if err := verifyPCI(r.Reset.Scope.PCI, *r.PCI); err != nil {
		return err
	}
	methods, err := readPCI(r.Reset.Scope.PCI, "reset_method")
	if err != nil {
		return err
	}
	if methods != r.Reset.Scope.OriginalMethods && methods != r.Reset.Scope.Method {
		return fmt.Errorf("E_RESET_EXTERNAL_METHODS")
	}
	if err := writeSysfs(filepath.Join("/sys/bus/pci/devices", r.Reset.Scope.PCI, "reset_method"), r.Reset.Scope.OriginalMethods); err != nil {
		return err
	}
	return n.restorePCI(r, save)
}

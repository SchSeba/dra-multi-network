// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func roceRootHandle(root Root) effects.Handle {
	d, e := root.Persona.Inventory, root.Persona.Policy.Spec.Exposure
	h := effects.Handle{Type: topology.RDMAType, Scope: effects.HostScope, Name: d.IfName, PCIAddress: d.PCI, AllowedDrivers: e.AllowedDrivers, AllowedMADAccess: e.AllowedMADAccess}
	for _, profile := range e.AllowedRDMAProfiles {
		h.AllowedRDMAProfiles = append(h.AllowedRDMAProfiles, string(profile))
	}
	if len(d.RDMA) == 1 {
		h.RDMA = &d.RDMA[0]
	}
	return h
}
func hasRoCE(p Preparation) bool {
	for _, root := range p.Roots {
		if root.Persona.Policy.Spec.Exposure != nil && root.Persona.Policy.Spec.Exposure.Mode == topology.RDMAMode {
			return true
		}
	}
	return false
}
func hasDeviceAccess(p Preparation) bool { return hasRoCE(p) || len(preparationEdits(p)) > 0 }

// The attachment's acknowledged, post-move character nodes are the runtime
// grant. Preparation's original host nodes are never injected into a container.
func runtimeDevicePreparation(p Preparation, at Attachment) (Preparation, error) {
	if !hasRoCE(p) {
		return p, nil
	}
	if at.Phase != Ready || at.Spec.ClaimUID != string(p.Claim.UID) || at.Spec.Fingerprint != p.Fingerprint || at.Spec.NodeUID != p.NodeUID || at.Spec.BootID != p.BootID || at.Spec.RevisionRef.UID != string(p.Revision.UID) || at.Spec.RevisionRef.Digest != p.Revision.Spec.Digest {
		return p, fmt.Errorf("E_RDMA_ATTACHMENT_GRANT")
	}
	p.Hardware.Outputs = maps.Clone(p.Hardware.Outputs)
	if p.Hardware.Outputs == nil {
		p.Hardware.Outputs = map[string]effects.Handle{}
	}
	for _, root := range p.Roots {
		if root.Persona.Policy.Spec.Exposure.Mode != topology.RDMAMode {
			continue
		}
		name := root.Step + "." + topology.RDMAPort
		config, err := rootConfiguration(p, root)
		if err != nil {
			return p, err
		}
		h, ok := at.Execution.Outputs[name]
		deviceCount := 2
		if h.MADAccess {
			deviceCount++
		}
		if !ok || h.Type != topology.RDMAType || h.Shared || h.Scope != effects.SandboxScope || h.PCIAddress != root.Persona.Inventory.PCI || h.RDMA == nil || h.RDMA.Namespace == nil || *h.RDMA.Namespace != at.SandboxNS || h.NamespacePath != at.Pin || len(h.Devices) != deviceCount || h.MADAccess != config.MADAccess || h.MADAccess && (!root.Persona.Policy.Spec.Exposure.AllowedMADAccess || !topology.IBProfile(config.RDMAProfile)) {
			return p, fmt.Errorf("E_RDMA_ATTACHMENT_GRANT: missing scoped outcome for %s", root.Step)
		}

		original := root.Persona.Inventory.RDMA
		if len(original) != 1 || h.RDMA.NodeGUID != original[0].NodeGUID || h.RDMA.SystemImageGUID != original[0].SystemImageGUID || h.RDMA.Name != original[0].Name || h.RDMA.Index != original[0].Index {
			return p, fmt.Errorf("E_RDMA_ATTACHMENT_GRANT: HCA incarnation differs")
		}
		p.Hardware.Outputs[name] = h
	}
	return p, nil
}

func (a *Agent) verifyRoCEPreparation(p Preparation) error {
	if !hasRoCE(p) {
		return nil
	}
	records, err := a.store.List("attach/")
	if err != nil {
		return err
	}
	for _, raw := range records {
		var at Attachment
		if err := json.Unmarshal(raw, &at); err != nil {
			return err
		}
		if at.Spec.ClaimUID != string(p.Claim.UID) || at.Phase == Removed {
			continue
		}
		if at.Spec.Fingerprint != p.Fingerprint {
			return fmt.Errorf("E_RDMA_ATTACHMENT_GRANT")
		}
		if at.Phase == Ready {
			if _, err := runtimeDevicePreparation(p, at); err != nil {
				return err
			}
			return a.verify(at)
		}
		// The native runner reconciles incomplete connection intent under the same
		// mutation lock. No container can pass its Ready gate in this state.
		return nil
	}
	for _, root := range p.Roots {
		if root.Persona.Policy.Spec.Exposure.Mode == topology.RDMAMode {
			config, err := rootConfiguration(p, root)
			if err != nil {
				return err
			}
			if err := effects.CheckRDMAAllocation(roceRootHandle(root), config); err != nil {
				return err
			}
		}
	}
	return nil
}

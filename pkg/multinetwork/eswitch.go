// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	sdk "github.com/SchSeba/dra-multi-network/pkg/pluginapi"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

const PFKind = "PF"
const pendingEswitchDrain = "PendingDrain"
const eswitchBlockedPhase = "Blocked"
const pendingEswitchFence = "PendingFence"

const ManageEswitch = "ManageEswitch"
const eswitchReleased = "Released"

var managedPCI = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

type eswitchManagementRecord struct {
	PCI        string                `json:"pci"`
	NodeUID    string                `json:"nodeUID"`
	BootID     string                `json:"bootID"`
	PolicyUID  string                `json:"policyUID"`
	PolicyName string                `json:"policyName"`
	Digest     string                `json:"digest"`
	Phase      string                `json:"phase"`
	Native     *effects.EswitchState `json:"native,omitempty"`
}

func validateEswitchPolicy(p api.DeviceExposurePolicy) error {
	m := p.Spec.Management
	if m == nil || p.Spec.Exposure != nil || m.Profile != effects.EswitchProfile || !slices.Contains([]string{"legacy", "switchdev"}, m.Mode) || len(m.AllowedModes) != 2 || !slices.Contains(m.AllowedModes, "legacy") || !slices.Contains(m.AllowedModes, "switchdev") || len(m.EffectPermissions) != 1 || m.EffectPermissions[0] != sdk.EswitchConfigure {
		return fmt.Errorf("E_ESWITCH_POLICY_GRANT")
	}
	s := p.Spec.Selector
	if s.Kind != PFKind || len(s.PCIAddresses) != 1 || !managedPCI.MatchString(s.PCIAddresses[0]) || len(s.ParentPCIAddresses) != 0 || s.Vendor != "" || s.Driver != "" || s.LinkType != "" {
		return fmt.Errorf("E_ESWITCH_POLICY_SCOPE: one exact PF PCI address is required")
	}
	return nil
}

// Hardware management belongs to the framework's discovery/publication
// authority. CNI planners still receive no node, PCI, policy or journal handles.
// ponytail: destructive mode changes require a whole-node DRA drain and cordon;
// per-controller scheduling fences can replace this when drain cost matters.
func (a *Agent) reconcileEswitch(ctx context.Context, policies []api.DeviceExposurePolicy, active map[string]bool) (bool, error) {
	a.eswitchBlocked = map[string]bool{}
	desired := map[string]api.DeviceExposurePolicy{}
	for _, p := range policies {
		if p.Spec.Action != ManageEswitch {
			continue
		}
		if err := validateEswitchPolicy(p); err != nil {
			return false, err
		}
		pci := p.Spec.Selector.PCIAddresses[0]
		match, err := Matches(p, Inventory{PCI: pci, Kind: PFKind}, a.node)
		if err != nil {
			return false, err
		}
		if !match {
			continue
		}
		if _, exists := desired[pci]; exists {
			return false, fmt.Errorf("E_ESWITCH_MULTIPLE_POLICIES: %s", pci)
		}
		desired[pci] = p
	}
	stored, err := a.store.List("eswitch/")
	if err != nil {
		return false, err
	}
	records := map[string]eswitchManagementRecord{}
	for _, raw := range stored {
		var r eswitchManagementRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return false, err
		}
		records[r.PCI] = r
	}
	for pci, p := range desired {
		if r, exists := records[pci]; !exists || r.Phase == eswitchReleased {
			records[pci] = eswitchManagementRecord{PCI: pci, NodeUID: string(a.node.UID), BootID: a.boot, PolicyUID: string(p.UID), PolicyName: p.Name, Digest: topology.Digest(p.Spec), Phase: pendingEswitchDrain}
		}
	}
	changed := false
	for pci, r := range records {
		p, wanted := desired[pci]
		if r.Phase == eswitchReleased {
			continue
		}
		message := ""
		restore := !wanted || r.PolicyUID != string(p.UID) || r.Digest != topology.Digest(p.Spec)
		var node corev1.Node
		if err := a.client.Get(ctx, client.ObjectKey{Name: a.node.Name}, &node); err != nil {
			return false, err
		}
		if r.NodeUID != string(node.UID) || r.BootID != a.boot {
			r.Phase, message = eswitchBlockedPhase, "E_ESWITCH_INCARNATION: saved authority requires reconciliation"
		} else if restore && r.Native == nil {
			r.Phase = eswitchReleased
		} else if !restore && r.Phase == Ready && r.Native != nil {
			mode, err := effects.HostEswitchMode("pci", pci)
			if err != nil || mode != r.Native.Target {
				r.Phase, message = eswitchBlockedPhase, fmt.Sprintf("E_ESWITCH_OBSERVATION: mode=%q: %v", mode, err)
			}
		} else if len(active) > 0 {
			r.Phase, message = pendingEswitchDrain, "allocated claims and retained preparations must be released before a mode change"
		} else if !node.Spec.Unschedulable {
			r.Phase, message = "PendingCordon", "cordon the node before destructive mode management"
		} else if d, exists := a.inventory[pci]; !exists || d.Protected || d.Kind != PFKind {
			r.Phase, message = eswitchBlockedPhase, "E_ESWITCH_PROTECTED_OR_UNKNOWN_PF"
		} else if r.Phase != pendingEswitchFence && (r.Native == nil || restore && r.Phase == Ready) {
			// Publish allocationReady=false before entering a destructive transition.
			// The next reconciliation renews API allocation and native drain checks.
			r.Phase, message = pendingEswitchFence, "withholding the affected PF/VF family before native management"
		} else {
			// API allocations are checked again immediately before entering the
			// native fence; callbacks share the same mutation lock as publication.
			fresh, _, err := a.activeClaims(ctx)
			if err != nil {
				return false, err
			}
			if len(fresh) > 0 {
				r.Phase, message = pendingEswitchDrain, "a new allocation arrived during drain verification"
			} else {
				n, err := effects.OpenHostNative()
				if err != nil {
					return false, err
				}
				save := func() error { return a.store.Put("eswitch/"+pci, &r) }
				if r.Native == nil {
					m := p.Spec.Management
					r.Native, err = n.CaptureEswitch(effects.EswitchGrant{PCI: pci, PolicyUID: r.PolicyUID, Profile: m.Profile, AllowedModes: m.AllowedModes}, m.Mode)
					if err == nil {
						err = save()
					}
				}
				if err == nil {
					err = n.ReconcileEswitch(r.Native, restore, save)
				}
				n.Close()
				if err != nil {
					r.Phase, message = eswitchBlockedPhase, err.Error()
				} else {
					changed = true
					if restore {
						r.Phase = eswitchReleased
					} else {
						r.Phase = Ready
					}
				}
			}
		}
		if r.Phase != Ready && r.Phase != eswitchReleased {
			a.eswitchBlocked[pci] = true
		}
		if err := a.store.Put("eswitch/"+pci, &r); err != nil {
			return false, err
		}
		if wanted {
			status := api.EswitchNodeStatus{Node: a.node.Name, ObservedGeneration: p.Generation, PCI: pci, Phase: r.Phase, Message: message}
			if r.Native != nil && r.Native.Completed {
				status.Mode = r.Native.Target
			}
			if err := a.reportEswitchPolicy(ctx, p, status); err != nil {
				return false, err
			}
		}
	}
	return changed, nil
}

func (a *Agent) reportEswitchPolicy(ctx context.Context, p api.DeviceExposurePolicy, status api.EswitchNodeStatus) error {
	before := p.DeepCopy()
	index := slices.IndexFunc(p.Status.EswitchNodes, func(s api.EswitchNodeStatus) bool { return s.Node == a.node.Name })
	if index < 0 {
		p.Status.EswitchNodes = append(p.Status.EswitchNodes, status)
	} else {
		p.Status.EswitchNodes[index] = status
	}
	if topology.Digest(p.Status) == topology.Digest(before.Status) {
		return nil
	}
	return a.client.Status().Patch(ctx, &p, client.MergeFrom(before))
}

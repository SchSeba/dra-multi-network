// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	nri "github.com/containerd/nri/pkg/api"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"sigs.k8s.io/controller-runtime/pkg/client"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func preparationEdits(p Preparation) map[string]cdispec.ContainerEdits {
	edits := map[string]cdispec.ContainerEdits{}
	for name, h := range p.Hardware.Outputs {
		if len(h.Devices) == 0 {
			continue
		}
		root, _, _ := strings.Cut(name, ".")
		var edit cdispec.ContainerEdits
		for _, d := range h.Devices {
			edit.DeviceNodes = append(edit.DeviceNodes, &cdispec.DeviceNode{Path: d.Path, HostPath: d.Path, Type: "c", Major: d.Major, Minor: d.Minor, Permissions: "rw"})
		}
		edits[root] = edit
	}
	return edits
}
func rootConfiguration(p Preparation, root Root) (topology.Config, error) {
	for _, step := range p.Revision.Spec.Topology.Steps {
		if step.ID != root.Step {
			continue
		}
		for _, plugin := range p.Revision.Spec.Plugins {
			if plugin.Name == step.PluginRef.Name {
				return topology.ConfigForPlugin(step, plugin.Spec)
			}
		}
	}
	return topology.Config{}, fmt.Errorf("E_ROOT_CONTRACT: missing locked configuration for %s", root.Step)
}

func (a *Agent) prepareHardware(p *Preparation) error {
	if err := a.verifyRoCEPreparation(*p); err != nil {
		return err
	}
	program, err := effects.PrepareDevices(p.Revision.Spec)
	if err != nil || len(program.Effects) == 0 {
		return err
	}
	save := func() error { return a.store.Put("prepare/"+string(p.Claim.UID), p) }
	native, err := effects.OpenHostNative()
	if err != nil {
		return err
	}
	defer native.Close()
	if p.State == CleanupPending {
		if err := restoreHardware(native, p, save); err != nil {
			return err
		}
		p.Hardware = effects.Execution{}
		p.State = Preparing
		if err := save(); err != nil {
			return err
		}
	}
	if p.Hardware.Program.APIVersion == "" {
		p.Hardware = effects.Execution{ID: "prepare-" + string(p.Claim.UID), Program: program, Roots: map[string]effects.Handle{}}
		for _, root := range p.Roots {
			c, err := rootConfiguration(*p, root)
			if err != nil {
				return err
			}
			if c.Driver == "" && c.VF == nil {
				continue
			}
			h := effects.Handle{Type: topology.InterfaceType, Scope: effects.HostScope, Name: root.Persona.Inventory.IfName, PCIAddress: root.Persona.Inventory.PCI, ParentPCI: root.Persona.Inventory.Parent, VFID: root.Persona.Inventory.VFID, AllowedDrivers: root.Persona.Policy.Spec.Exposure.AllowedDrivers}
			if c.Driver != "" {
				h.Type = topology.PCIFunctionType
			}
			for _, method := range root.Persona.Policy.Spec.Exposure.AllowedResetMethods {
				h.AllowedResetMethods = append(h.AllowedResetMethods, string(method))
			}
			for _, field := range root.Persona.Policy.Spec.Exposure.AllowedVFSettings {
				h.AllowedVFSettings = append(h.AllowedVFSettings, string(field))
			}
			p.Hardware.Roots[root.Step] = h
		}
		if err := save(); err != nil {
			return err
		}
	}
	if topology.Digest(p.Hardware.Program) != topology.Digest(program) {
		return fmt.Errorf("E_PREPARATION_CONTRACT")
	}
	if len(p.Hardware.Records) == 0 {
		if err := native.Preflight(p.Hardware); err != nil {
			return err
		}
	}
	if err := native.Execute(&p.Hardware, save); err != nil {
		p.State = CleanupPending
		if saveErr := save(); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		if restoreErr := restoreHardware(native, p, save); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		p.Hardware = effects.Execution{}
		p.State = Preparing
		return errors.Join(err, save())
	}
	if err := native.Verify(p.Hardware); err != nil {
		return err
	}
	for i := range p.Roots {
		for _, record := range p.Hardware.Records {
			if record.Effect.Kind == topology.DriverBind && record.Output.PCIAddress == p.Roots[i].Persona.Inventory.PCI && record.Effect.Parameters.Driver != topology.VFIODriver {
				p.Roots[i].Persona.Inventory.IfName = record.Output.Name
			}
		}
	}
	for _, root := range p.Roots {
		if root.Persona.Policy.Spec.Exposure.Mode == topology.VFIOMode {
			h := p.Hardware.Outputs[root.Step+"."+topology.VFIOPort]
			if h.Type != topology.VFIOType || h.PCIAddress != root.Persona.Inventory.PCI || len(h.Devices) != 2 {
				return fmt.Errorf("E_PREPARATION_OUTPUT")
			}
		}
	}
	if edits := preparationEdits(*p); len(edits) > 0 {
		if err := a.cdi.CreateScopedSpecFile(string(p.Claim.UID), edits); err != nil {
			return err
		}
	}
	for i := range p.Roots {
		if p.Roots[i].Persona.Policy.Spec.Exposure.Mode == topology.VFIOMode {
			p.Roots[i].CDIDeviceIDs = []string{a.cdi.GetClaimDevices(string(p.Claim.UID), p.Roots[i].Step)}
		}
	}
	return save()
}
func (a *Agent) verifyPreparation(p Preparation) error {
	if err := a.verifyRoCEPreparation(p); err != nil {
		return err
	}
	if len(p.Hardware.Program.Effects) == 0 {
		return nil
	}
	native, err := effects.OpenHostNative()
	if err != nil {
		return err
	}
	defer native.Close()
	if err := native.Verify(p.Hardware); err != nil {
		return err
	}
	if edits := preparationEdits(p); len(edits) > 0 {
		return a.cdi.CheckScopedSpecFile(string(p.Claim.UID), edits)
	}
	return nil
}

func (a *Agent) recoverPreparedAccess(p Preparation) error {
	if err := a.verifyRoCEPreparation(p); err != nil {
		return err
	}
	if len(p.Hardware.Program.Effects) == 0 {
		return nil
	}
	native, err := effects.OpenHostNative()
	if err != nil {
		return err
	}
	defer native.Close()
	if err := native.Verify(p.Hardware); err != nil {
		return err
	}
	if edits := preparationEdits(p); len(edits) > 0 {
		if err := a.cdi.CheckScopedSpecFile(string(p.Claim.UID), edits); err != nil {
			// The verified journal owns this claim's descriptor. Recreate metadata;
			// recovery does not rebind or reset a device with an active consumer.
			return a.cdi.CreateScopedSpecFile(string(p.Claim.UID), edits)
		}
	}
	return nil
}
func (a *Agent) releaseHardware(p *Preparation) error {
	if len(p.Hardware.Program.Effects) == 0 {
		return nil
	}
	p.State = Unpreparing
	save := func() error { return a.store.Put("prepare/"+string(p.Claim.UID), p) }
	if err := save(); err != nil {
		return err
	}
	if len(preparationEdits(*p)) > 0 {
		if err := a.cdi.DeleteSpecFile(string(p.Claim.UID)); err != nil {
			return err
		}
	}
	native, err := effects.OpenHostNative()
	if err != nil {
		return err
	}
	defer native.Close()
	return restoreHardware(native, p, save)
}

func restoreHardware(native *effects.Native, p *Preparation, save func() error) error {
	if err := topology.PlanCleanup(p.Revision.Spec, topology.UnprepareHook, p.Hardware.ID); err != nil {
		return err
	}
	return native.Restore(&p.Hardware, save)
}

// Recover preparation intent only after attachment recovery has established
// which sandboxes still own resources. API deletion alone cannot release them.
func (a *Agent) recoverPreparations(ctx context.Context, pods []*nri.PodSandbox, containers []*nri.Container) error {
	prepared, err := a.store.List("prepare/")
	if err != nil {
		return err
	}
	attachments, err := a.store.List("attach/")
	if err != nil {
		return err
	}
	inUse := map[string]bool{}
	attachmentClaims := map[string]string{}
	attachmentStates := map[string]Attachment{}
	for _, raw := range attachments {
		var at Attachment
		if err := json.Unmarshal(raw, &at); err != nil {
			return err
		}
		if at.Phase != Removed {
			inUse[at.Spec.ClaimUID] = true
			attachmentClaims[at.Spec.SandboxID] = at.Spec.ClaimUID
			attachmentStates[at.Spec.SandboxID] = at
		}
	}
	liveIDs, liveUIDs := map[string]bool{}, map[string]bool{}
	liveClaims := map[client.ObjectKey]bool{}
	actualPods := map[string]corev1.Pod{}
	unknownNamespaces := map[string]bool{}
	for _, sandbox := range pods {
		liveIDs[sandbox.Id], liveUIDs[sandbox.Uid] = true, true
		var pod corev1.Pod
		if err := a.client.Get(ctx, client.ObjectKey{Namespace: sandbox.Namespace, Name: sandbox.Name}, &pod); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			unknownNamespaces[sandbox.Namespace] = true
			continue
		}
		if string(pod.UID) != sandbox.Uid {
			unknownNamespaces[sandbox.Namespace] = true
			continue
		}
		actualPods[sandbox.Id] = pod
		for _, ref := range pod.Spec.ResourceClaims {
			name, _, err := resourceclaim.Name(&pod, &ref)
			if err != nil || name == nil {
				unknownNamespaces[pod.Namespace] = true
				continue
			}
			liveClaims[client.ObjectKey{Namespace: pod.Namespace, Name: *name}] = true
		}
	}
	unknownContainer := false
	for _, container := range containers {
		if !liveIDs[container.PodSandboxId] && container.State != nri.ContainerState_CONTAINER_STOPPED {
			unknownContainer = true
		}
	}
	for key, raw := range prepared {
		var p Preparation
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if p.State == Released {
			continue
		}
		if p.NodeUID != string(a.node.UID) || p.BootID != a.boot {
			return fmt.Errorf("E_PREPARATION_IDENTITY")
		}
		claimKey := client.ObjectKeyFromObject(&p.Claim)
		used := inUse[string(p.Claim.UID)] || liveClaims[claimKey] || unknownNamespaces[p.Claim.Namespace] || unknownContainer
		for _, consumer := range p.Claim.Status.ReservedFor {
			used = used || liveUIDs[string(consumer.UID)]
		}
		var claim resourceapi.ResourceClaim
		if err := a.client.Get(ctx, claimKey, &claim); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		allocated := claim.Status.Allocation != nil && allocationFingerprint(&claim) == p.Fingerprint
		if hasDeviceAccess(p) {
			for _, container := range containers {
				if container.State == nri.ContainerState_CONTAINER_STOPPED {
					continue
				}
				pod, found := actualPods[container.PodSandboxId]
				consumes := attachmentClaims[container.PodSandboxId] == string(p.Claim.UID)
				checkOwner := false
				if found && pod.Namespace == p.Claim.Namespace {
					for _, ref := range pod.Spec.ResourceClaims {
						name, owner, err := resourceclaim.Name(&pod, &ref)
						if err == nil && name != nil && *name == p.Claim.Name {
							consumes, checkOwner = true, owner
						}
					}
				}
				if !consumes {
					continue
				}
				if !found || pod.Spec.NodeName != a.node.Name {
					return fmt.Errorf("E_POD_IDENTITY: cannot verify recovered device consumer")
				}
				if !allocated {
					return fmt.Errorf("E_PREPARATION_IDENTITY: recovered device consumer has no matching allocation")
				}
				if err := verifyClaimConsumer(&pod, &claim, checkOwner); err != nil {
					return err
				}
				access, err := runtimeDevicePreparation(p, attachmentStates[container.PodSandboxId])
				if err != nil {
					return err
				}
				if err := validateContainerAccess(access, pod, container); err != nil {
					return err
				}
			}
		}
		switch p.State {
		case Prepared:
			if allocated || used {
				if err := a.recoverPreparedAccess(p); err != nil {
					return err
				}
				continue
			}
		case Unpreparing:
			if used {
				continue
			}
		case Preparing, CleanupPending:
			if used {
				return fmt.Errorf("E_RECOVERY_LIVE_CONSUMER: incomplete preparation")
			}
			if allocated {
				// Restore attempted effects; kubelet can retry the original plan
				// after synchronization. Never replay binding against a live user.
				if err := a.releaseHardware(&p); err != nil {
					return err
				}
				p.Hardware = effects.Execution{}
				for i := range p.Roots {
					p.Roots[i].CDIDeviceIDs = nil
				}
				p.State = Preparing
				if err := a.store.Put(key, p); err != nil {
					return err
				}
				continue
			}
		default:
			return fmt.Errorf("E_PREPARATION_STATE: %s", p.State)
		}
		if err := a.unprepare(p.Claim.UID); err != nil {
			return err
		}
	}
	return nil
}

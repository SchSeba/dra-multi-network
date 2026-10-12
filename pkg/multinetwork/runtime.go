// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	nri "github.com/containerd/nri/pkg/api"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func (a *Agent) resolve(ctx context.Context, sandbox *nri.PodSandbox) (*Preparation, error) {
	var pod corev1.Pod
	if err := a.client.Get(ctx, client.ObjectKey{Namespace: sandbox.Namespace, Name: sandbox.Name}, &pod); err != nil {
		return nil, err
	}
	if string(pod.UID) != sandbox.Uid || pod.Spec.NodeName != a.node.Name {
		return nil, fmt.Errorf("E_POD_IDENTITY")
	}
	var selected *Preparation
	for _, ref := range pod.Spec.ResourceClaims {
		name, checkOwner, err := resourceclaim.Name(&pod, &ref)
		if err != nil {
			return nil, err
		}
		if name == nil {
			return nil, fmt.Errorf("E_CLAIM_RESOLUTION")
		}
		var claim resourceapi.ResourceClaim
		if err := a.client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: *name}, &claim); err != nil {
			return nil, err
		}
		required := false
		if claim.Status.Allocation != nil {
			for _, r := range claim.Status.Allocation.Devices.Results {
				if r.Driver == api.Driver {
					required = true
				}
			}
		} else {
			for _, request := range claim.Spec.Devices.Requests {
				if request.Exactly == nil {
					continue
				}
				var class resourceapi.DeviceClass
				if err := a.client.Get(ctx, client.ObjectKey{Name: request.Exactly.DeviceClassName}, &class); err != nil {
					return nil, err
				}
				for _, config := range class.Spec.Config {
					if config.Opaque != nil && config.Opaque.Driver == api.Driver {
						required = true
					}
				}
			}
		}
		if !required {
			continue
		}
		if pod.Spec.HostNetwork {
			return nil, fmt.Errorf("E_HOST_NETWORK: secondary topology needs a separate sandbox")
		}
		if err := verifyClaimConsumer(&pod, &claim, checkOwner); err != nil {
			return nil, err
		}
		var p Preparation
		found, err := a.store.Get("prepare/"+string(claim.UID), &p)
		if err != nil {
			return nil, err
		}
		if !found || p.State != Prepared {
			return nil, fmt.Errorf("E_NOT_PREPARED: %s", claim.Name)
		}
		fingerprint := allocationFingerprint(&claim)
		if fingerprint != p.Fingerprint || p.BootID != a.boot || p.NodeUID != string(a.node.UID) {
			return nil, fmt.Errorf("E_PREPARATION_IDENTITY")
		}
		if selected != nil {
			return nil, fmt.Errorf("E_WORKLOAD_PROFILE: one project topology claim per Pod")
		}
		selected = &p
	}
	return selected, nil
}

func verifyClaimConsumer(pod *corev1.Pod, claim *resourceapi.ResourceClaim, checkOwner bool) error {
	if checkOwner {
		if err := resourceclaim.IsForPod(pod, claim, false); err != nil {
			return err
		}
	}
	if len(claim.Status.ReservedFor) != 1 || !resourceclaim.IsReservedForPod(pod, claim, false) {
		return fmt.Errorf("E_CONSUMER_BINDING")
	}
	return nil
}
func sandboxNamespace(sandbox *nri.PodSandbox) string {
	if sandbox.Linux == nil {
		return ""
	}
	for _, ns := range sandbox.Linux.Namespaces {
		if ns.Type == "network" {
			return ns.Path
		}
	}
	return ""
}
func attachmentID(sandbox *nri.PodSandbox, p Preparation) string {
	return topology.Digest([]string{sandbox.Uid, sandbox.Id, p.Fingerprint})[:24]
}
func (a *Agent) RunPodSandbox(ctx context.Context, sandbox *nri.PodSandbox) error {
	if err := a.lock(ctx); err != nil {
		return err
	}
	defer a.unlock()
	p, err := a.resolve(ctx, sandbox)
	if err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	if err := a.verifyPreparation(*p); err != nil {
		return err
	}
	id := attachmentID(sandbox, *p)
	var at Attachment
	found, err := a.store.Get("attach/"+id, &at)
	if err != nil {
		return err
	}
	if found {
		if at.Phase == Ready {
			return a.verify(at)
		}
		if at.Phase == Removing || at.Phase == Removed {
			return fmt.Errorf("E_SANDBOX_REMOVED")
		}
	} else {
		records, err := a.store.List("attach/")
		if err != nil {
			return err
		}
		for _, raw := range records {
			var other Attachment
			if err := json.Unmarshal(raw, &other); err != nil {
				return err
			}
			if other.Spec.ClaimUID == string(p.Claim.UID) && other.Phase != Removed {
				return fmt.Errorf("E_SANDBOX_INCARNATION: old attachment must finish cleanup")
			}
		}
		path := sandboxNamespace(sandbox)
		if path == "" {
			return fmt.Errorf("E_SANDBOX_NAMESPACE")
		}
		program, err := effects.ConnectNetwork(p.Revision.Spec)
		if err != nil {
			return err
		}
		at = Attachment{Spec: api.AttachmentSpec{PodUID: sandbox.Uid, SandboxID: sandbox.Id, NodeName: a.node.Name, NodeUID: string(a.node.UID), BootID: a.boot, ClaimUID: string(p.Claim.UID), Fingerprint: p.Fingerprint, RevisionRef: api.RevisionRef{Name: p.Revision.Name, UID: string(p.Revision.UID), Digest: p.Revision.Spec.Digest}}, Namespace: p.Claim.Namespace, Name: "network-" + id, Phase: "Connecting", Pin: filepath.Join(a.dir, "netns", id), Stage: filepath.Join(a.dir, "netns", id+"-stage"), Execution: effects.Execution{ID: id, Program: program, Roots: rootsFor(*p)}}
		if err := a.store.Put("attach/"+id, at); err != nil {
			return err
		}
		if err := effects.PinNamespace(path, at.Pin); err != nil {
			return err
		}
		if err := effects.CreateStagingNamespace(at.Stage); err != nil {
			return err
		}
		at.SandboxNS, err = effects.IdentifyNamespace(at.Pin)
		if err != nil {
			return err
		}
		at.StageNS, err = effects.IdentifyNamespace(at.Stage)
		if err != nil {
			return err
		}
		if err := a.store.Put("attach/"+id, at); err != nil {
			return err
		}
	}
	if err := verifyPins(at); err != nil {
		return err
	}
	native, err := effects.OpenNative(at.Pin, at.Stage)
	if err != nil {
		return err
	}
	defer native.Close()
	native.UplinkStore = a.store
	save := func() error { return a.store.Put("attach/"+id, at) }
	if len(at.Execution.Records) == 0 {
		if err := native.Preflight(at.Execution); err != nil {
			at.LastError = err.Error()
			if saveErr := save(); saveErr != nil {
				return saveErr
			}
			return err
		}
	}
	if err := native.Execute(&at.Execution, save); err != nil {
		at.LastError = err.Error()
		at.Phase = Removing
		if saveErr := save(); saveErr != nil {
			return saveErr
		}
		if cleanupErr := a.restoreAttachment(native, &at, save); cleanupErr != nil {
			return fmt.Errorf("%w; cleanup pending: %v", err, cleanupErr)
		}
		at.Phase = Removed
		if saveErr := save(); saveErr != nil {
			return saveErr
		}
		return err
	}
	if err := native.Verify(at.Execution); err != nil {
		return err
	}
	at.Phase = Ready
	at.LastError = ""
	return save()
}
func verifyPins(at Attachment) error {
	for _, pin := range []struct {
		path     string
		identity effects.NamespaceIdentity
	}{{at.Pin, at.SandboxNS}, {at.Stage, at.StageNS}} {
		actual, err := effects.IdentifyNamespace(pin.path)
		if err != nil {
			return err
		}
		if pin.identity != (effects.NamespaceIdentity{}) && actual != pin.identity {
			return fmt.Errorf("E_NAMESPACE_IDENTITY: retained namespace changed")
		}
	}
	return nil
}
func (a *Agent) verify(at Attachment) error {
	if err := verifyPins(at); err != nil {
		return err
	}
	if at.Phase != Ready || at.Spec.BootID != a.boot || at.Spec.NodeUID != string(a.node.UID) {
		return fmt.Errorf("E_ATTACHMENT_NOT_READY")
	}
	native, err := effects.OpenNative(at.Pin, at.Stage)
	if err != nil {
		return err
	}
	defer native.Close()
	native.UplinkStore = a.store
	return native.Verify(at.Execution)
}

// OCI must join the namespace retained for this attachment. A matching Pod ID
// alone does not prove that the container can see the connected topology.
func verifyContainerNamespace(at Attachment, container *nri.Container) error {
	if container == nil || container.PodSandboxId != at.Spec.SandboxID {
		return fmt.Errorf("E_CONTAINER_IDENTITY")
	}
	var path string
	for _, ns := range container.GetLinux().GetNamespaces() {
		if ns == nil {
			return fmt.Errorf("E_CONTAINER_NAMESPACE: missing namespace")
		}
		if ns.Type == "network" {
			if path != "" || ns.Path == "" {
				return fmt.Errorf("E_CONTAINER_NAMESPACE: expected one joined network namespace")
			}
			path = ns.Path
		}
	}
	if path == "" || at.SandboxNS == (effects.NamespaceIdentity{}) {
		return fmt.Errorf("E_CONTAINER_NAMESPACE: missing retained network namespace identity")
	}
	actual, err := effects.IdentifyNamespace(path)
	if err != nil {
		return fmt.Errorf("E_CONTAINER_NAMESPACE: %w", err)
	}
	if actual != at.SandboxNS {
		return fmt.Errorf("E_CONTAINER_NAMESPACE: container does not join the retained sandbox")
	}
	return nil
}

func (a *Agent) gate(ctx context.Context, pod *nri.PodSandbox, container *nri.Container, create bool) (*nri.ContainerAdjustment, error) {
	if err := a.lock(ctx); err != nil {
		return nil, err
	}
	defer a.unlock()
	p, err := a.resolve(ctx, pod)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	if err := a.verifyPreparation(*p); err != nil {
		return nil, err
	}
	var at Attachment
	found, err := a.store.Get("attach/"+attachmentID(pod, *p), &at)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("E_ATTACHMENT_NOT_READY")
	}
	if err := a.verify(at); err != nil {
		return nil, err
	}
	if err := verifyContainerNamespace(at, container); err != nil {
		return nil, err
	}
	if !hasDeviceAccess(*p) {
		return nil, nil
	}
	var actual corev1.Pod
	if err := a.client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, &actual); err != nil {
		return nil, err
	}
	if string(actual.UID) != pod.Uid {
		return nil, fmt.Errorf("E_POD_IDENTITY")
	}
	access, err := runtimeDevicePreparation(*p, at)
	if err != nil {
		return nil, err
	}
	if create && hasRoCE(*p) {
		return adjustRDMAAccess(access, actual, container)
	}
	return nil, validateContainerAccess(access, actual, container)
}
func (a *Agent) CreateContainer(ctx context.Context, pod *nri.PodSandbox, container *nri.Container) (*nri.ContainerAdjustment, []*nri.ContainerUpdate, error) {
	adjustment, err := a.gate(ctx, pod, container, true)
	return adjustment, nil, err
}
func (a *Agent) StartContainer(ctx context.Context, pod *nri.PodSandbox, container *nri.Container) error {
	_, err := a.gate(ctx, pod, container, false)
	return err
}

// CRI stops containers before StopPodSandbox. Release sandbox networking here:
// kubelet may wait for NodeUnprepare before asking CRI to remove the sandbox.
func (a *Agent) StopPodSandbox(ctx context.Context, pod *nri.PodSandbox) error {
	return a.RemovePodSandbox(ctx, pod)
}
func (a *Agent) RemovePodSandbox(ctx context.Context, pod *nri.PodSandbox) error {
	if err := a.lock(ctx); err != nil {
		return err
	}
	defer a.unlock()
	records, err := a.store.List("attach/")
	if err != nil {
		return err
	}
	for key, raw := range records {
		var at Attachment
		if err := json.Unmarshal(raw, &at); err != nil {
			return err
		}
		if at.Spec.PodUID != pod.Uid || at.Spec.SandboxID != pod.Id || at.Phase == Removed {
			continue
		}
		if err := a.remove(key, &at); err != nil {
			return err
		}
	}
	return nil
}
func (a *Agent) remove(key string, at *Attachment) error {
	at.Phase = Removing
	save := func() error { return a.store.Put(key, at) }
	if err := save(); err != nil {
		return err
	}
	if err := verifyPins(*at); err != nil {
		return err
	}
	native, err := effects.OpenNative(at.Pin, at.Stage)
	if err != nil {
		return err
	}
	err = a.restoreAttachment(native, at, save)
	native.Close()
	if err != nil {
		at.LastError = err.Error()
		if saveErr := save(); saveErr != nil {
			return saveErr
		}
		return err
	}
	at.Phase = Removed
	at.LastError = ""
	if err := save(); err != nil {
		return err
	}
	return releasePins(*at)
}

func (a *Agent) restoreAttachment(native *effects.Native, at *Attachment, save func() error) error {
	var p Preparation
	found, err := a.store.Get("prepare/"+at.Spec.ClaimUID, &p)
	if err != nil {
		return err
	}
	ref := at.Spec.RevisionRef
	if !found || string(p.Claim.UID) != at.Spec.ClaimUID || p.Fingerprint != at.Spec.Fingerprint || p.Revision.Name != ref.Name || string(p.Revision.UID) != ref.UID || p.Revision.Spec.Digest != ref.Digest {
		return fmt.Errorf("E_ATTACHMENT_CLEANUP_CONTRACT: saved preparation differs")
	}
	if err := topology.PlanCleanup(p.Revision.Spec, topology.RemoveHook, at.Execution.ID); err != nil {
		return err
	}
	native.UplinkStore = a.store
	return native.Restore(&at.Execution, save)
}
func releasePins(at Attachment) error {
	if err := effects.Unpin(at.Pin); err != nil {
		return err
	}
	return effects.Unpin(at.Stage)
}
func (a *Agent) Synchronize(ctx context.Context, pods []*nri.PodSandbox, containers []*nri.Container) (updates []*nri.ContainerUpdate, syncErr error) {
	defer func() {
		if syncErr != nil {
			klog.ErrorS(syncErr, "NRI recovery failed")
		}
	}()
	if err := a.lock(ctx); err != nil {
		return nil, err
	}
	defer a.unlock()
	live := map[string]*nri.PodSandbox{}
	for _, pod := range pods {
		live[pod.Id] = pod
	}
	records, err := a.store.List("attach/")
	if err != nil {
		return nil, err
	}
	for key, raw := range records {
		var at Attachment
		if err := json.Unmarshal(raw, &at); err != nil {
			return nil, err
		}
		if at.Phase == Removed {
			if err := releasePins(at); err != nil {
				return nil, err
			}
			continue
		}
		if at.Spec.BootID != a.boot || at.Spec.NodeUID != string(a.node.UID) {
			return nil, fmt.Errorf("E_RECOVERY_IDENTITY")
		}
		pod, exists := live[at.Spec.SandboxID]
		// A crash before the first effect intent cannot have mutated a link.
		// Empty/missing pins in that window need no native effect restoration.
		if at.Phase != Ready && len(at.Execution.Records) == 0 {
			for _, container := range containers {
				if container.PodSandboxId == at.Spec.SandboxID && container.State != nri.ContainerState_CONTAINER_STOPPED {
					return nil, fmt.Errorf("E_RECOVERY_LIVE_CONTAINER: incomplete attachment")
				}
			}
			at.Phase = Removed
			if err := a.store.Put(key, at); err != nil {
				return nil, err
			}
			if err := releasePins(at); err != nil {
				return nil, err
			}
			continue
		}
		if at.SandboxNS == (effects.NamespaceIdentity{}) {
			at.SandboxNS, err = effects.IdentifyNamespace(at.Pin)
			if err != nil {
				return nil, err
			}
			at.StageNS, err = effects.IdentifyNamespace(at.Stage)
			if err != nil {
				return nil, err
			}
			if err := a.store.Put(key, at); err != nil {
				return nil, err
			}
		}
		if !exists {
			for _, container := range containers {
				if container.PodSandboxId == at.Spec.SandboxID && container.State != nri.ContainerState_CONTAINER_STOPPED {
					return nil, fmt.Errorf("E_RECOVERY_LIVE_CONTAINER")
				}
			}
			if err := a.remove(key, &at); err != nil {
				return nil, err
			}
		} else {
			if pod.Uid != at.Spec.PodUID {
				return nil, fmt.Errorf("E_RECOVERY_POD_IDENTITY")
			}
			if at.Phase != Ready {
				for _, container := range containers {
					if container.PodSandboxId == at.Spec.SandboxID && container.State != nri.ContainerState_CONTAINER_STOPPED {
						return nil, fmt.Errorf("E_RECOVERY_LIVE_CONTAINER: incomplete attachment")
					}
				}
				if err := a.remove(key, &at); err != nil {
					return nil, err
				}
				continue
			}
			if err := a.verify(at); err != nil {
				return nil, err
			}
			for _, container := range containers {
				if container.PodSandboxId == at.Spec.SandboxID && container.State != nri.ContainerState_CONTAINER_STOPPED {
					if err := verifyContainerNamespace(at, container); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if err := a.recoverPreparations(ctx, pods, containers); err != nil {
		return nil, err
	}
	if !a.synced {
		a.synced = true
		close(a.syncDone)
	}
	return nil, nil
}
func (a *Agent) reportStatuses(ctx context.Context) error {
	records, err := a.store.List("attach/")
	if err != nil {
		return err
	}
	var result error
	for key, raw := range records {
		var at Attachment
		err := json.Unmarshal(raw, &at)
		if err == nil {
			err = a.reportStatus(ctx, key, at)
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("%s: %w", key, err))
		}
	}
	return result
}

func (a *Agent) reportStatus(ctx context.Context, key string, at Attachment) error {
	if at.Phase == Removed {
		if err := releasePins(at); err != nil {
			return err
		}
		if at.Reported {
			return nil
		}
		var namespace corev1.Namespace
		err := a.client.Get(ctx, client.ObjectKey{Name: at.Namespace}, &namespace)
		if apierrors.IsNotFound(err) || err == nil && namespace.DeletionTimestamp != nil {
			at.Reported = true
			return a.store.Put(key, at)
		}
		if err != nil {
			return err
		}
	}
	object := api.NetworkAttachmentStatus{ObjectMeta: metav1.ObjectMeta{Name: at.Name, Namespace: at.Namespace}, Spec: at.Spec}
	if err := a.client.Get(ctx, client.ObjectKeyFromObject(&object), &object); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		if err := a.client.Create(ctx, &object); err != nil {
			return err
		}
	} else if topology.Digest(object.Spec) != topology.Digest(at.Spec) {
		return fmt.Errorf("E_STATUS_IDENTITY")
	}

	status := api.AttachmentStatus{Phase: at.Phase, LastError: at.LastError, Conditions: object.Status.Conditions}
	for name, h := range at.Execution.Outputs {
		step, port, _ := strings.Cut(name, ".")
		output := api.Output{Step: step, Port: port, Type: h.Type, IfName: h.Name, PCIAddress: h.PCIAddress, ShareID: h.ShareID}
		if h.Type == topology.RDMAType {
			output.IfName = ""
			output.RDMADevice = h.Name
		}
		status.Outputs = append(status.Outputs, output)
	}
	slices.SortFunc(status.Outputs, func(a, b api.Output) int { return strings.Compare(a.Step+"."+a.Port, b.Step+"."+b.Port) })
	ready := metav1.ConditionFalse
	if at.Phase == Ready {
		ready = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: Ready, Status: ready, Reason: at.Phase, Message: "Committed node journal state", ObservedGeneration: object.Generation})
	if topology.Digest(object.Status) != topology.Digest(status) {
		object.Status = status
		if err := a.client.Status().Update(ctx, &object); err != nil {
			return err
		}
	}
	if at.Phase == Removed {
		at.Reported = true
		if err := a.store.Put(key, at); err != nil {
			return err
		}
	}
	return nil
}

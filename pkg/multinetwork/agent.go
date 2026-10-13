// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/containerd/nri/pkg/stub"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/cdi"
	nodeconfig "github.com/SchSeba/dra-multi-network/pkg/config"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/ipam"
	"github.com/SchSeba/dra-multi-network/pkg/journal"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

const (
	Preparing      = "Preparing"
	Unpreparing    = "Unpreparing"
	CleanupPending = "CleanupPending"
	Removed        = "Removed"
	Removing       = "Removing"
	Ready          = "Ready"
	Released       = "Released"
	Prepared       = "Prepared"
	VFKind         = "VF"
)

const StateDir = "/var/lib/dra-multi-network"

type Root struct {
	Step         string                                    `json:"step"`
	Result       resourceapi.DeviceRequestAllocationResult `json:"result"`
	Persona      Persona                                   `json:"persona"`
	CDIDeviceIDs []string                                  `json:"cdiDeviceIDs,omitempty"`
}
type IPAMRoot struct {
	Step   string                                    `json:"step"`
	Result resourceapi.DeviceRequestAllocationResult `json:"result"`
	Lease  ipam.Lease                                `json:"lease"`
}
type Preparation struct {
	IPAMRoots   []IPAMRoot                  `json:"ipamRoots,omitempty"`
	Claim       resourceapi.ResourceClaim   `json:"claim"`
	Fingerprint string                      `json:"fingerprint"`
	Revision    api.NetworkTopologyRevision `json:"revision"`
	Roots       []Root                      `json:"roots"`
	State       string                      `json:"state"`
	NodeUID     string                      `json:"nodeUID"`
	BootID      string                      `json:"bootID"`
	Hardware    effects.Execution           `json:"hardware,omitempty"`
}
type Attachment struct {
	Spec      api.AttachmentSpec        `json:"spec"`
	Namespace string                    `json:"namespace"`
	Name      string                    `json:"name"`
	Phase     string                    `json:"phase"`
	LastError string                    `json:"lastError,omitempty"`
	Reported  bool                      `json:"reported,omitempty"`
	Pin       string                    `json:"pin"`
	SandboxNS effects.NamespaceIdentity `json:"sandboxNS,omitempty"`
	StageNS   effects.NamespaceIdentity `json:"stageNS,omitempty"`
	Stage     string                    `json:"stage"`
	Execution effects.Execution         `json:"execution"`
}
type Agent struct {
	client         client.Client
	config         *nodeconfig.Config
	node           corev1.Node
	boot           string
	podName        string
	podUID         string
	dir            string
	store          *journal.Store
	helper         *kubeletplugin.Helper
	ipamHelper     *kubeletplugin.Helper
	ipamProvider   ipam.Provider
	stub           stub.Stub
	mutation       chan struct{}
	inventory      map[string]Inventory
	personas       map[string]Persona
	eswitchBlocked map[string]bool
	synced         bool
	syncDone       chan struct{}
	cdi            *cdi.Handler
}

func Run(ctx context.Context, config *nodeconfig.Config) error {
	dir := filepath.Join(StateDir, api.Driver)
	store, err := journal.Open(dir)
	if err != nil {
		return err
	}
	defer store.Close()
	a := &Agent{client: config.K8sClient.Client, config: config, store: store, dir: dir, mutation: make(chan struct{}, 1), personas: map[string]Persona{}, syncDone: make(chan struct{}), podName: os.Getenv("POD_NAME"), podUID: os.Getenv("POD_UID")}
	if a.podName == "" || a.podUID == "" {
		return fmt.Errorf("E_AGENT_POD_IDENTITY: POD_NAME and POD_UID are required")
	}
	a.cdi, err = cdi.NewHandler(config.Flags.CdiRoot)
	if err != nil {
		return err
	}
	a.boot, err = effects.BootID()
	if err != nil {
		return err
	}
	if err := a.client.Get(ctx, client.ObjectKey{Name: config.Flags.NodeName}, &a.node); err != nil {
		return err
	}
	_, err = store.Get("inventory", &a.inventory)
	if err != nil {
		return err
	}
	var savedBoot string
	found, err := store.Get("inventory-boot", &savedBoot)
	if err != nil {
		return err
	}
	if !found || savedBoot != a.boot {
		active, err := store.List("attach/")
		if err != nil {
			return err
		}
		for _, raw := range active {
			var at Attachment
			if err := json.Unmarshal(raw, &at); err != nil {
				return err
			}
			if at.Phase != Removed {
				return fmt.Errorf("E_REBOOT_RECOVERY: retained attachment requires verified hardware reconciliation")
			}
		}
		a.inventory, err = Discover()
		if err != nil {
			return err
		}
		if err := store.Put("inventory", a.inventory); err != nil {
			return err
		}
		if err := store.Put("inventory-boot", a.boot); err != nil {
			return err
		}
	}
	if _, err := store.Get("publication", &a.personas); err != nil {
		return err
	}
	protected, err := protectedPCI()
	if err != nil {
		return err
	}
	var rdma []effects.RDMADevice
	for pci, device := range a.inventory {
		device.Protected = protected[pci] || protected[device.Parent]
		a.inventory[pci] = device
		rdma = append(rdma, device.RDMA...)
	}
	protectRDMAInventory(a.inventory, rdma)
	if err := store.Put("inventory", a.inventory); err != nil {
		return err
	}
	a.helper, err = kubeletplugin.Start(ctx, a, kubeletplugin.KubeClient(config.K8sClient.Interface), kubeletplugin.NodeName(a.node.Name), kubeletplugin.DriverName(api.Driver), kubeletplugin.RegistrarDirectoryPath(config.Flags.KubeletRegistrarDirectoryPath), kubeletplugin.PluginDataDirectoryPath(config.DriverPluginPath()))
	if err != nil {
		return err
	}
	defer a.helper.Stop()
	a.ipamProvider = ipam.Store{Client: a.client, Namespace: config.Flags.Namespace}
	ipamPath := filepath.Join(config.Flags.KubeletPluginsDirectoryPath, topology.IPAMDriver)
	if err := os.MkdirAll(ipamPath, 0750); err != nil {
		return err
	}
	a.ipamHelper, err = kubeletplugin.Start(ctx, &ipam.Driver{Provider: a.ipamProvider, Client: a.client, NodeName: a.node.Name, Cancel: config.CancelMainCtx}, kubeletplugin.KubeClient(config.K8sClient.Interface), kubeletplugin.NodeName(a.node.Name), kubeletplugin.DriverName(topology.IPAMDriver), kubeletplugin.RegistrarDirectoryPath(config.Flags.KubeletRegistrarDirectoryPath), kubeletplugin.PluginDataDirectoryPath(ipamPath))
	if err != nil {
		return err
	}
	defer a.ipamHelper.Stop()
	nriOptions := []stub.Option{stub.WithOnClose(func() { config.CancelMainCtx(fmt.Errorf("NRI disconnected")) })}
	if os.Getenv("NRI_PLUGIN_NAME") == "" {
		nriOptions = append(nriOptions, stub.WithPluginName("dra-multi-network"))
	}
	if os.Getenv("NRI_PLUGIN_IDX") == "" {
		nriOptions = append(nriOptions, stub.WithPluginIdx("42"))
	}
	a.stub, err = stub.New(a, nriOptions...)
	if err != nil {
		return err
	}
	if err := a.stub.Start(ctx); err != nil {
		return err
	}
	defer a.stub.Stop()
	select {
	case <-a.syncDone:
	case <-time.After(30 * time.Second):
		return fmt.Errorf("E_RUNTIME_SYNC: timed out waiting for NRI synchronization")
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	registrationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	registrationTicker := time.NewTicker(100 * time.Millisecond)
	defer registrationTicker.Stop()
	for a.helper.RegistrationStatus() == nil || !a.helper.RegistrationStatus().PluginRegistered || a.ipamHelper.RegistrationStatus() == nil || !a.ipamHelper.RegistrationStatus().PluginRegistered {
		select {
		case <-registrationCtx.Done():
			return fmt.Errorf("E_KUBELET_REGISTRATION: %w", registrationCtx.Err())
		case <-registrationTicker.C:
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.setReady(cleanup, false); err != nil {
			klog.ErrorS(err, "remove node readiness")
		}
	}()
	// A controller restart briefly removes its admission endpoint. Publish and
	// mark ready under the same mutation fence as callbacks, retrying only
	// transient API failures while that endpoint comes back.
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := a.lock(ctx); err != nil {
			return false, err
		}
		err := a.publish(ctx)
		if err == nil {
			err = a.setReady(ctx, true)
		}
		a.unlock()
		if apierrors.IsInternalError(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || apierrors.IsTooManyRequests(err) {
			klog.ErrorS(err, "waiting for publication/readiness admission")
			return false, nil
		}
		return err == nil, err
	}); err != nil {
		return err
	}
	// #nosec G303 -- Root-only container filesystem; no shared /tmp host mount.
	if err := os.WriteFile("/tmp/dra-multi-network-ready", []byte("ready"), 0600); err != nil {
		return err
	}
	defer func() {
		if err := os.Remove("/tmp/dra-multi-network-ready"); err != nil && !os.IsNotExist(err) {
			klog.ErrorS(err, "remove readiness file")
		}
	}()
	klog.InfoS("INTERNAL networking agent ready", "node", a.node.Name, "inventory", len(a.inventory), "personas", len(a.personas))
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
			if err := a.lock(ctx); err != nil {
				return err
			}
			err := a.publish(ctx)
			if err == nil {
				err = a.setReady(ctx, true)
			}
			a.unlock()
			if err != nil {
				klog.ErrorS(err, "publish exposure policies")
			}
			if err := a.reportStatuses(ctx); err != nil {
				klog.ErrorS(err, "report attachment status")
			}
		}
	}
}
func (a *Agent) lock(ctx context.Context) error {
	select {
	case a.mutation <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *Agent) unlock() { <-a.mutation }
func (a *Agent) setReady(ctx context.Context, ready bool) error {
	var node corev1.Node
	if err := a.client.Get(ctx, client.ObjectKey{Name: a.node.Name}, &node); err != nil {
		return err
	}
	before := node.DeepCopy()
	if node.UID != a.node.UID {
		return fmt.Errorf("E_NODE_IDENTITY")
	}
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	if ready {
		report, err := a.capabilityReport(ctx)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(report)
		if err != nil {
			return err
		}
		if _, err := topology.ParseCapabilities(string(raw), time.Now()); err != nil {
			return err
		}
		if node.Annotations == nil {
			node.Annotations = map[string]string{}
		}
		node.Annotations[topology.CapabilityAnnotation] = string(raw)
		node.Labels[api.Driver+"/ready"] = "true"
	} else {
		delete(node.Labels, api.Driver+"/ready")
		delete(node.Annotations, topology.CapabilityAnnotation)
	}
	return a.client.Patch(ctx, &node, client.MergeFrom(before))
}
func (a *Agent) WatchHealthStatus(context.Context, chan<- kubeletplugin.DeviceHealthReport) error {
	return kubeletplugin.ErrHealthNotSupported
}
func (a *Agent) HandleError(ctx context.Context, err error, msg string) {
	klog.FromContext(ctx).Error(err, msg)
	if !errors.Is(err, kubeletplugin.ErrRecoverable) {
		a.config.CancelMainCtx(err)
	}
}

func (a *Agent) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[k8stypes.UID]kubeletplugin.PrepareResult, error) {
	results := map[k8stypes.UID]kubeletplugin.PrepareResult{}
	for _, claim := range claims {
		result := kubeletplugin.PrepareResult{}
		if err := a.lock(ctx); err != nil {
			return nil, err
		}
		p, err := a.prepare(ctx, claim)
		a.unlock()
		if err != nil {
			result.Err = err
		} else {
			for _, r := range p.Roots {
				result.Devices = append(result.Devices, kubeletplugin.Device{Requests: []string{r.Result.Request}, PoolName: r.Result.Pool, DeviceName: r.Result.Device, ShareID: r.Result.ShareID, CDIDeviceIDs: r.CDIDeviceIDs})
			}
		}
		results[claim.UID] = result
	}
	return results, nil
}
func allocationFingerprint(claim *resourceapi.ResourceClaim) string {
	return topology.Digest(struct {
		UID        k8stypes.UID
		Allocation *resourceapi.AllocationResult
	}{claim.UID, claim.Status.Allocation})
}
func (a *Agent) prepare(ctx context.Context, claim *resourceapi.ResourceClaim) (Preparation, error) {
	p := Preparation{}
	if claim == nil || claim.Status.Allocation == nil {
		return p, fmt.Errorf("E_ALLOCATION: missing allocation")
	}
	fingerprint := allocationFingerprint(claim)
	found, err := a.store.Get("prepare/"+string(claim.UID), &p)
	if err != nil {
		return p, err
	}
	if found && p.State == Prepared {
		if p.Fingerprint != fingerprint || p.NodeUID != string(a.node.UID) || p.BootID != a.boot {
			return p, fmt.Errorf("E_ALLOCATION_CHANGED")
		}
		if err := a.verifyPreparation(p); err != nil {
			return p, err
		}
		return p, nil
	}
	previous := p
	if found && p.State != Released && (p.Fingerprint != fingerprint || p.NodeUID != string(a.node.UID) || p.BootID != a.boot) {
		return p, fmt.Errorf("E_ALLOCATION_CHANGED")
	}
	if found && p.State == Unpreparing {
		return p, fmt.Errorf("E_CLAIM_RELEASING")
	}
	if len(claim.Status.ReservedFor) > 1 {
		return p, fmt.Errorf("E_CONSUMER: one Pod per claim")
	}
	for _, c := range claim.Status.Allocation.Devices.Config {
		if c.Opaque != nil && c.Opaque.Driver == api.Driver && c.Source != resourceapi.AllocationConfigSourceClass {
			return p, fmt.Errorf("E_BINDING_ORIGIN: project configuration must come from DeviceClass")
		}
	}
	bindings := map[string]bool{}
	var revisionRef api.RevisionRef
	p = Preparation{Claim: *claim.DeepCopy(), Fingerprint: fingerprint, State: Preparing, NodeUID: string(a.node.UID), BootID: a.boot}
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != api.Driver && result.Driver != topology.IPAMDriver {
			continue
		}
		if result.Driver == topology.IPAMDriver {
			bound := false
			for _, config := range resourceclaim.ConfigForResult(claim.Status.Allocation.Devices.Config, result) {
				bound = bound || config.Opaque != nil && config.Opaque.Driver == api.Driver
			}
			if !bound {
				continue
			}
		}
		if result.Driver == api.Driver && result.Pool != a.node.Name || result.AdminAccess != nil && *result.AdminAccess {
			return p, fmt.Errorf("E_ALLOCATION_SCOPE")
		}
		requestOK := false
		for _, request := range claim.Spec.Devices.Requests {
			if request.Name == result.Request && request.Exactly != nil && len(request.FirstAvailable) == 0 && request.Exactly.Count == 1 && (request.Exactly.AllocationMode == "" || request.Exactly.AllocationMode == resourceapi.DeviceAllocationModeExactCount) && (request.Exactly.AdminAccess == nil || !*request.Exactly.AdminAccess) {
				requestOK = true
			}
		}
		if !requestOK {
			return p, fmt.Errorf("E_REQUEST_PROFILE: root requires Exactly/ExactCount 1")
		}
		var binding api.TopologyRootConfiguration
		count := 0
		for _, config := range resourceclaim.ConfigForResult(claim.Status.Allocation.Devices.Config, result) {
			if config.Opaque == nil || config.Opaque.Driver != api.Driver {
				continue
			}
			count++
			if err := topology.Decode(config.Opaque.Parameters.Raw, &binding); err != nil {
				return p, err
			}
			if config.Source != resourceapi.AllocationConfigSourceClass || binding.APIVersion != api.GroupVersion.String() || binding.Kind != "TopologyRootConfiguration" {
				return p, fmt.Errorf("E_BINDING_ORIGIN")
			}
		}
		if count != 1 || bindings[binding.RootStep] {
			return p, fmt.Errorf("E_ROOT_COVERAGE: duplicate/missing binding")
		}
		bindings[binding.RootStep] = true
		if revisionRef.Name == "" {
			revisionRef = binding.RevisionRef
			if err := a.client.Get(ctx, client.ObjectKey{Name: revisionRef.Name}, &p.Revision); err != nil {
				return p, err
			}
			if string(p.Revision.UID) != revisionRef.UID || p.Revision.Spec.Digest != revisionRef.Digest || !meta.IsStatusConditionTrue(p.Revision.Status.Conditions, Ready) {
				return p, fmt.Errorf("E_REVISION_BINDING")
			}
			if err := topology.VerifyRevision(p.Revision.Spec); err != nil {
				return p, err
			}
		} else if revisionRef != binding.RevisionRef {
			return p, fmt.Errorf("E_REVISION_MIXED")
		}

		var rootStep api.Step
		var rootPlugin api.PluginSpec
		for _, step := range p.Revision.Spec.Topology.Steps {
			if step.ID == binding.RootStep {
				rootStep = step
			}
		}
		for _, plugin := range p.Revision.Spec.Plugins {
			if plugin.Name == rootStep.PluginRef.Name && plugin.Digest == binding.PluginContractDigest {
				rootPlugin = plugin.Spec
			}
		}
		if rootStep.Kind != topology.AllocationKind || topology.AllocationDriver(rootPlugin) != result.Driver {
			return p, fmt.Errorf("E_ROOT_DRIVER_CONTRACT")
		}
		if result.Driver == topology.IPAMDriver {
			expected, err := topology.ConfigForPlugin(rootStep, rootPlugin)
			if err != nil {
				return p, err
			}
			actual, err := ipam.Entitlement(claim, result)
			if err != nil {
				return p, err
			}
			if topology.Digest(actual) != topology.Digest(expected) {
				return p, fmt.Errorf("E_IPAM_ROOT_CONFIG")
			}
			pool, err := a.ipamProvider.GetPool(ctx, result.Pool)
			if err != nil {
				return p, err
			}
			if pool.Deleting || actual.Network != pool.Config.Network || actual.Subnet != pool.Config.Subnet || result.Pool != pool.Name || result.Device != "subnet" {
				return p, fmt.Errorf("E_IPAM_ALLOCATION_SCOPE")
			}
			p.IPAMRoots = append(p.IPAMRoots, IPAMRoot{Step: binding.RootStep, Result: result, Lease: ipam.Reservation(claim, result, pool)})
			continue
		}
		persona, exists := a.personas[result.Device]
		if !exists {
			return p, fmt.Errorf("E_INVENTORY: unknown persona %s", result.Device)
		}
		if a.eswitchBlocked[persona.Inventory.PCI] || a.eswitchBlocked[persona.Inventory.Parent] {
			return p, fmt.Errorf("E_ESWITCH_DRAIN: new preparation is fenced by pending hardware management")
		}
		// allocationReady is a scheduling hint. Already allocated claims keep
		// their retained policy grants while publication drains.
		stepOK := false
		for _, step := range p.Revision.Spec.Topology.Steps {
			if step.ID != binding.RootStep {
				continue
			}
			var spec api.PluginSpec
			for _, plugin := range p.Revision.Spec.Plugins {
				if plugin.Name == step.PluginRef.Name {
					spec = plugin.Spec
				}
			}
			c, err := topology.ConfigForPlugin(step, spec)
			if err != nil {
				return p, err
			}
			e := persona.Policy.Spec.Exposure
			if step.Kind != "Allocation" || step.PluginRef.Name != e.Plugin || c.Mode != e.Mode {
				return p, fmt.Errorf("E_PERSONA_CONTRACT")
			}
			if spec.Implementation.Type == topology.EffectProgramImplementation && (persona.Plugin == nil || topology.Digest(*persona.Plugin) != binding.PluginContractDigest) {
				return p, fmt.Errorf("E_PERSONA_CONTRACT: published programmable allocation contract differs")
			}
			for _, plugin := range p.Revision.Spec.Plugins {
				if plugin.Name == step.PluginRef.Name && plugin.Digest == binding.PluginContractDigest {
					stepOK = true
					for _, effect := range plugin.Spec.AllowedEffects {
						if !slices.Contains(e.EffectPermissions, effect) {
							return p, fmt.Errorf("E_EFFECT_DENIED: policy %s", effect)
						}
					}
				}
			}
		}
		if !stepOK {
			return p, fmt.Errorf("E_ROOT_CONTRACT")
		}
		shared := persona.Policy.Spec.Exposure.AllowMultipleAllocations
		if shared {
			q := result.ConsumedCapacity[resourceapi.QualifiedName(api.Driver+"/children")]
			if result.ShareID == nil || *result.ShareID == "" || q.Cmp(resource.MustParse("1")) != 0 {
				return p, fmt.Errorf("E_SHARE_ENTITLEMENT")
			}
		} else if result.ShareID != nil || len(result.ConsumedCapacity) != 0 {
			return p, fmt.Errorf("E_EXCLUSIVE_ENTITLEMENT")
		}
		p.Roots = append(p.Roots, Root{Step: binding.RootStep, Result: result, Persona: persona})
	}
	for _, step := range p.Revision.Spec.Topology.Steps {
		if step.Kind == "Allocation" && !bindings[step.ID] {
			return p, fmt.Errorf("E_ROOT_COVERAGE: missing %s", step.ID)
		}
	}
	if len(p.Roots) == 0 {
		return p, fmt.Errorf("E_ROOT_COVERAGE: empty")
	}
	if err := a.validateIPAMFabric(ctx, p); err != nil {
		return p, err
	}
	if err := a.checkRetirement(revisionRef); err != nil {
		return p, err
	}
	program, err := effects.ConnectNetwork(p.Revision.Spec)
	if err != nil {
		return p, err
	}
	_ = program
	if err := a.checkReservations(ctx, p); err != nil {
		return p, err
	}
	if found && previous.State != Released {
		p.Hardware = previous.Hardware
		p.State = previous.State
	}
	if err := a.store.Put("prepare/"+string(claim.UID), p); err != nil {
		return p, err
	}
	if err := a.prepareHardware(&p); err != nil {
		return p, err
	}
	p.State = Prepared
	if err := a.store.Put("prepare/"+string(claim.UID), p); err != nil {
		return p, err
	}
	return p, nil
}
func conflicts(a, b Persona) bool {
	da, db := a.Inventory, b.Inventory
	ea, eb := a.Policy.Spec.Exposure, b.Policy.Spec.Exposure
	if da.PCI == db.PCI {
		return !(ea.AllowMultipleAllocations && eb.AllowMultipleAllocations && ea.Plugin == eb.Plugin)
	}
	return da.Parent == db.Parent && (da.Kind != VFKind && !ea.AllowMultipleAllocations || db.Kind != VFKind && !eb.AllowMultipleAllocations)
}
func (a *Agent) checkReservations(ctx context.Context, p Preparation) error {
	roots := slices.Clone(p.Roots)
	knownClaims := map[k8stypes.UID]bool{p.Claim.UID: true}
	records, err := a.store.List("prepare/")
	if err != nil {
		return err
	}
	for _, raw := range records {
		var other Preparation
		if err := json.Unmarshal(raw, &other); err != nil {
			return err
		}
		if other.State != Released && other.Claim.UID != p.Claim.UID {
			roots = append(roots, other.Roots...)
			knownClaims[other.Claim.UID] = true
		}
	}
	// Scheduler allocations are authoritative even before node preparation.
	var claims resourceapi.ResourceClaimList
	if err := a.client.List(ctx, &claims); err != nil {
		return err
	}
	for _, claim := range claims.Items {
		if knownClaims[claim.UID] || claim.Status.Allocation == nil {
			continue
		}
		for _, r := range claim.Status.Allocation.Devices.Results {
			if r.Driver != api.Driver || r.Pool != a.node.Name {
				continue
			}
			persona, exists := a.personas[r.Device]
			if !exists {
				return fmt.Errorf("E_UNKNOWN_ACTIVE_PERSONA")
			}
			roots = append(roots, Root{Result: r, Persona: persona})
		}
	}
	protected, err := protectedPCI()
	if err != nil {
		return err
	}
	shares := map[string]bool{}
	usage := map[string]int{}
	for _, root := range roots {
		d := root.Persona.Inventory
		if protected[d.PCI] || protected[d.Parent] {
			return fmt.Errorf("E_PRIMARY_DEPENDENCY: %s", d.PCI)
		}
		if !root.Persona.Policy.Spec.Exposure.AllowMultipleAllocations {
			continue
		}
		q := root.Result.ConsumedCapacity[resourceapi.QualifiedName(api.Driver+"/children")]
		if root.Result.ShareID == nil || *root.Result.ShareID == "" || q.Cmp(resource.MustParse("1")) != 0 {
			return fmt.Errorf("E_SHARE_ENTITLEMENT")
		}
		key := root.Result.Device + "/" + string(*root.Result.ShareID)
		if shares[key] {
			return fmt.Errorf("E_DUPLICATE_SHARE")
		}
		shares[key] = true
		usage[root.Result.Device]++
		limit, err := strconv.Atoi(root.Persona.Policy.Spec.Exposure.Capacity["children"].Value)
		if err != nil || usage[root.Result.Device] > limit {
			return fmt.Errorf("E_SHARE_CAPACITY")
		}
	}
	for i, root := range roots {
		for j := i + 1; j < len(roots); j++ {
			if conflicts(root.Persona, roots[j].Persona) {
				return fmt.Errorf("E_FAMILY_CONFLICT: %s and %s", root.Persona.Inventory.PCI, roots[j].Persona.Inventory.PCI)
			}
		}
	}
	return nil
}
func (a *Agent) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[k8stypes.UID]error, error) {
	results := map[k8stypes.UID]error{}
	for _, claim := range claims {
		if err := a.lock(ctx); err != nil {
			return nil, err
		}
		results[claim.UID] = a.unprepare(claim.UID)
		a.unlock()
	}
	return results, nil
}
func (a *Agent) unprepare(uid k8stypes.UID) error {
	var p Preparation
	found, err := a.store.Get("prepare/"+string(uid), &p)
	if err != nil || !found {
		return err
	}
	if p.State == Released {
		return nil
	}
	// Persist the release request even when attachment cleanup must run first.
	// A restart must not accept another sandbox for this preparation.
	p.State = Unpreparing
	if err := a.store.Put("prepare/"+string(uid), p); err != nil {
		return err
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
		if at.Spec.ClaimUID == string(uid) && at.Phase != Removed {
			return fmt.Errorf("E_SANDBOX_ACTIVE: attachment cleanup pending")
		}
	}
	if err := a.releaseHardware(&p); err != nil {
		return err
	}
	p.State = Released
	return a.store.Put("prepare/"+string(uid), p)
}

func rootsFor(p Preparation) map[string]effects.Handle {
	roots := map[string]effects.Handle{}
	for _, r := range p.Roots {
		if r.Persona.Policy.Spec.Exposure.Mode == topology.RDMAMode {
			h := roceRootHandle(r)
			h.ParentPCI, h.VFID = r.Persona.Inventory.Parent, r.Persona.Inventory.VFID
			roots[r.Step] = representorRootGrant(p, r, h)
			continue
		}
		if r.Persona.Policy.Spec.Exposure.Mode == topology.VFIOMode {
			h := p.Hardware.Outputs[r.Step+"."+topology.VFIOPort]
			h.ParentPCI, h.VFID = r.Persona.Inventory.Parent, r.Persona.Inventory.VFID
			h = representorRootGrant(p, r, h)
			roots[r.Step] = h
			continue
		}
		h := effects.Handle{Type: topology.InterfaceType, Scope: "host", Name: r.Persona.Inventory.IfName, PCIAddress: r.Persona.Inventory.PCI, ParentPCI: r.Persona.Inventory.Parent, VFID: r.Persona.Inventory.VFID, Shared: r.Persona.Policy.Spec.Exposure.AllowMultipleAllocations}
		h = representorRootGrant(p, r, h)
		for _, field := range r.Persona.Policy.Spec.Exposure.AllowedVFSettings {
			h.AllowedVFSettings = append(h.AllowedVFSettings, string(field))
		}
		if r.Result.ShareID != nil {
			h.ShareID = string(*r.Result.ShareID)
		}
		roots[r.Step] = h
	}
	return roots
}

// Discovery observations become authority only under a locked switchdev root.
func representorRootGrant(p Preparation, r Root, h effects.Handle) effects.Handle {
	c, err := rootConfiguration(p, r)
	if err == nil && c.Representor {
		h.Representor = r.Persona.Inventory.Representor
		h.AllowedHostBridges = r.Persona.Policy.Spec.Exposure.AllowedHostBridges
		h.AllowedOVSBridges = r.Persona.Policy.Spec.Exposure.AllowedOVSBridges
	}
	return h
}

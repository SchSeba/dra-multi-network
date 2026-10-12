// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

const ReadyCondition = "Ready"

const ManagedLabel = api.Driver + "/managed"
const ManagedLabelValue = "true"
const RevisionFinalizer = api.Group + "/retain"

type TopologyReconciler struct {
	client.Client
	Namespace string
}

func (r *TopologyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).For(&api.NetworkTopology{}).
		Watches(&api.NetworkPlugin{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []ctrl.Request {
			var list api.NetworkTopologyList
			if err := r.List(ctx, &list); err != nil {
				return nil
			}
			result := make([]ctrl.Request, 0, len(list.Items))
			for _, t := range list.Items {
				result = append(result, ctrl.Request{NamespacedName: k8stypes.NamespacedName{Name: t.Name}})
			}
			return result
		})).Complete(r); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).For(&api.NetworkPlugin{}).Complete(reconcile.Func(r.reconcilePlugin))
}
func (r *TopologyReconciler) reconcilePlugin(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var p api.NetworkPlugin
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !p.DeletionTimestamp.IsZero() {
		retention := RetentionReconciler{Client: r.Client, Namespace: r.Namespace}
		used, err := retention.descriptorInUse(ctx, p.Name)
		if used || err != nil {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, err
		}
		before := p.DeepCopy()
		p.Finalizers = slices.DeleteFunc(p.Finalizers, func(f string) bool { return f == topology.PluginRetentionFinalizer })
		return ctrl.Result{}, r.Patch(ctx, &p, client.MergeFrom(before))
	}
	if !slices.Contains(p.Finalizers, topology.PluginRetentionFinalizer) {
		before := p.DeepCopy()
		p.Finalizers = append(p.Finalizers, topology.PluginRetentionFinalizer)
		if err := r.Patch(ctx, &p, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
	}
	status := api.PluginStatus{Digest: topology.Digest(p.Spec), ObservedGeneration: p.Generation, Conditions: p.Status.Conditions}
	condition := metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Validated", Message: "Plugin contract supported by this build", ObservedGeneration: p.Generation}
	if err := topology.ValidatePlugin(p); err != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "Invalid"
		condition.Message = err.Error()
	}
	meta.SetStatusCondition(&status.Conditions, condition)
	if topology.Digest(p.Status) == topology.Digest(status) {
		return ctrl.Result{}, nil
	}
	p.Status = status
	return ctrl.Result{}, r.Status().Update(ctx, &p)
}
func (r *TopologyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var t api.NetworkTopology
	if err := r.Get(ctx, req.NamespacedName, &t); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !t.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	var list api.NetworkPluginList
	if err := r.List(ctx, &list); err != nil {
		return ctrl.Result{}, err
	}
	plugins := map[string]api.NetworkPlugin{}
	for _, p := range list.Items {
		if p.DeletionTimestamp.IsZero() {
			plugins[p.Name] = p
		}
	}
	spec, err := topology.Compile(t, plugins)
	if err != nil {
		t.Status.ObservedGeneration = t.Generation
		meta.SetStatusCondition(&t.Status.Conditions, metav1.Condition{Type: ReadyCondition, Status: metav1.ConditionFalse, Reason: "Invalid", Message: err.Error(), ObservedGeneration: t.Generation})
		return ctrl.Result{}, r.Status().Update(ctx, &t)
	}
	name := t.Name
	if len(name) > 40 {
		name = name[:40]
	}
	name += "-" + spec.Digest[:16]
	revision := api.NetworkTopologyRevision{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{ManagedLabel: ManagedLabelValue}, Finalizers: []string{RevisionFinalizer}}, Spec: spec}
	if err := r.Create(ctx, &revision); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, client.ObjectKey{Name: name}, &revision); err != nil {
			return ctrl.Result{}, err
		}
		if topology.Digest(revision.Spec) != topology.Digest(spec) {
			return ctrl.Result{}, fmt.Errorf("E_REVISION_COLLISION: %s", name)
		}
	}
	if !revision.DeletionTimestamp.IsZero() || revision.Annotations[topology.RetirementAnnotation] != "" {
		return ctrl.Result{}, fmt.Errorf("E_REVISION_RETIRED: %s", revision.Name)
	}
	ref := api.RevisionRef{Name: revision.Name, UID: string(revision.UID), Digest: spec.Digest}
	var classes []api.RootClass
	for _, step := range spec.Topology.Steps {
		if step.Kind != "Allocation" {
			continue
		}
		var pluginDigest string
		var pluginSpec api.PluginSpec
		for _, p := range spec.Plugins {
			if p.Name == step.PluginRef.Name {
				pluginDigest = p.Digest
				pluginSpec = p.Spec
			}
		}
		c, err := topology.ConfigForPlugin(step, pluginSpec)
		if err != nil {
			return ctrl.Result{}, err
		}
		binding := api.TopologyRootConfiguration{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "TopologyRootConfiguration"}, RevisionRef: ref, RootStep: step.ID, PluginContractDigest: pluginDigest}
		raw, err := json.Marshal(binding)
		if err != nil {
			return ctrl.Result{}, err
		}
		className := name + "-" + step.ID
		linkType := c.LinkType
		if linkType == "" {
			linkType = topology.Ethernet
		}
		mandatory := fmt.Sprintf(`device.driver == %q && device.attributes[%q].plugin == %q && device.attributes[%q].mode == %q && device.attributes[%q].linkType == %q && device.attributes[%q].allocationReady == true`, api.Driver, api.Driver, step.PluginRef.Name, api.Driver, c.Mode, api.Driver, linkType, api.Driver)
		if pluginSpec.Implementation.Type == topology.EffectProgramImplementation {
			mandatory += fmt.Sprintf(` && device.attributes[%q].pluginContractDigest == %q`, api.Driver, pluginDigest)
		}
		if pluginSpec.AllocationDriver == topology.IPAMDriver {
			mandatory = fmt.Sprintf(`device.driver == %q && device.allowMultipleAllocations == true && device.attributes[%q].network == %q && device.attributes[%q].subnet == %q && device.attributes[%q].allocationReady == true`, topology.IPAMDriver, topology.IPAMDriver, c.Network, topology.IPAMDriver, c.Subnet, topology.IPAMDriver)
		}
		selectors := []resourceapi.DeviceSelector{{CEL: &resourceapi.CELDeviceSelector{Expression: mandatory}}}
		for _, expression := range step.Selectors {
			selectors = append(selectors, resourceapi.DeviceSelector{CEL: &resourceapi.CELDeviceSelector{Expression: expression}})
		}
		class := resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: className, Labels: map[string]string{ManagedLabel: ManagedLabelValue}, OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "NetworkTopologyRevision", Name: revision.Name, UID: revision.UID}}}, Spec: resourceapi.DeviceClassSpec{
			Selectors: selectors, Config: []resourceapi.DeviceClassConfiguration{{DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: api.Driver, Parameters: runtime.RawExtension{Raw: raw}}}}},
		}}
		if pluginSpec.AllocationDriver == topology.IPAMDriver {
			parameters, _ := json.Marshal(map[string]any{"apiVersion": topology.IPAMDriver + "/v1alpha1", "kind": "IPAMAllocationConfiguration", "network": c.Network, "subnet": c.Subnet, "addresses": 1})
			class.Spec.Config = append(class.Spec.Config, resourceapi.DeviceClassConfiguration{DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: topology.IPAMDriver, Parameters: runtime.RawExtension{Raw: parameters}}}})
		}
		if err := r.Create(ctx, &class); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, err
			}
			var existing resourceapi.DeviceClass
			if err := r.Get(ctx, client.ObjectKey{Name: className}, &existing); err != nil {
				return ctrl.Result{}, err
			}
			if topology.Digest(existing.Spec) != topology.Digest(class.Spec) {
				return ctrl.Result{}, fmt.Errorf("E_CLASS_CONFLICT: %s", className)
			}
		}
		classes = append(classes, api.RootClass{RootStep: step.ID, ClassName: className})
	}
	status := api.TopologyStatus{ObservedGeneration: t.Generation, RevisionRef: &ref, RootClasses: classes}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: ReadyCondition, Status: metav1.ConditionTrue, Reason: "Compiled", Message: "Immutable revision and all root classes verified", ObservedGeneration: t.Generation})
	if topology.Digest(revision.Status) != topology.Digest(status) {
		revision.Status = status
		if err := r.Status().Update(ctx, &revision); err != nil {
			return ctrl.Result{}, err
		}
	}
	// Preserve transition time on a no-op reconcile, avoiding an update loop.
	status.Conditions = t.Status.Conditions
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: ReadyCondition, Status: metav1.ConditionTrue, Reason: "Compiled", Message: "Immutable revision and all root classes verified", ObservedGeneration: t.Generation})
	if topology.Digest(t.Status) != topology.Digest(status) {
		t.Status = status
		if err := r.Status().Update(ctx, &t); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

// Reads use the uncached API client. Missing/stale node reports never prove
// release; the persisted node retirement fence prevents delayed preparation.
type RetentionReconciler struct {
	client.Client
	Namespace string
}

func (r *RetentionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&api.NetworkTopologyRevision{}).Complete(r)
}

func (r *RetentionReconciler) healthyReports(ctx context.Context) ([]topology.NodeCapabilities, error) {
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return nil, err
	}
	if len(nodes.Items) == 0 {
		return nil, fmt.Errorf("E_GC_NODE_UNKNOWN: no reconciled nodes")
	}
	var reports []topology.NodeCapabilities
	for _, node := range nodes.Items {
		report, err := topology.ParseCapabilities(node.Annotations[topology.CapabilityAnnotation], time.Now())
		if err != nil || !report.ReservationsChecked {
			return nil, fmt.Errorf("E_GC_NODE_UNKNOWN: %s", node.Name)
		}
		valid, err := (&CapabilityReconciler{Client: r.Client, Namespace: r.Namespace}).ValidAgent(ctx, node, report)
		if err != nil {
			return nil, err
		}
		if !valid || !node.DeletionTimestamp.IsZero() {
			return nil, fmt.Errorf("E_GC_NODE_UNKNOWN: %s", node.Name)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func revisionConfigReferences(config resourceapi.DeviceConfiguration, ref api.RevisionRef) (bool, error) {
	if config.Opaque == nil || config.Opaque.Driver != api.Driver {
		return false, nil
	}
	var binding api.TopologyRootConfiguration
	if err := topology.Decode(config.Opaque.Parameters.Raw, &binding); err != nil {
		return false, err
	}
	if binding.Kind != "TopologyRootConfiguration" || binding.APIVersion != api.GroupVersion.String() {
		return false, fmt.Errorf("E_GC_BINDING_UNKNOWN")
	}
	return binding.RevisionRef.UID == ref.UID || binding.RevisionRef.Name == ref.Name, nil
}

func claimSpecReferences(spec resourceapi.ResourceClaimSpec, classes map[string]bool, ref api.RevisionRef) (bool, error) {
	for _, request := range spec.Devices.Requests {
		if request.Exactly != nil && classes[request.Exactly.DeviceClassName] {
			return true, nil
		}
		for _, alternative := range request.FirstAvailable {
			if classes[alternative.DeviceClassName] {
				return true, nil
			}
		}
	}
	for _, config := range spec.Devices.Config {
		used, err := revisionConfigReferences(config.DeviceConfiguration, ref)
		if used || err != nil {
			return used, err
		}
	}
	return false, nil
}

func (r *RetentionReconciler) revisionClasses(ctx context.Context, revision api.NetworkTopologyRevision) ([]resourceapi.DeviceClass, error) {
	var list resourceapi.DeviceClassList
	if err := r.List(ctx, &list); err != nil {
		return nil, err
	}
	ref := topology.RevisionIdentity(revision)
	var classes []resourceapi.DeviceClass
	for _, class := range list.Items {
		owned := slices.ContainsFunc(class.OwnerReferences, func(owner metav1.OwnerReference) bool {
			return owner.UID == revision.UID && owner.Kind == "NetworkTopologyRevision" && owner.APIVersion == api.GroupVersion.String()
		})
		bound := false
		for _, config := range class.Spec.Config {
			used, err := revisionConfigReferences(config.DeviceConfiguration, ref)
			if err != nil {
				return nil, err
			}
			bound = bound || used
		}
		if !owned && !bound {
			continue
		}
		if !owned || !bound || class.Labels[ManagedLabel] != ManagedLabelValue {
			return nil, fmt.Errorf("E_GC_CLASS_OWNER: %s", class.Name)
		}
		classes = append(classes, class)
	}
	return classes, nil
}

func (r *RetentionReconciler) revisionInUse(ctx context.Context, ref api.RevisionRef, classes []resourceapi.DeviceClass) (bool, error) {
	names := map[string]bool{}
	for _, class := range classes {
		names[class.Name] = true
	}
	var claims resourceapi.ResourceClaimList
	if err := r.List(ctx, &claims); err != nil {
		return false, err
	}
	for _, claim := range claims.Items {
		used, err := claimSpecReferences(claim.Spec, names, ref)
		if used || err != nil {
			return used, err
		}
		if claim.Status.Allocation != nil {
			for _, config := range claim.Status.Allocation.Devices.Config {
				used, err = revisionConfigReferences(config.DeviceConfiguration, ref)
				if used || err != nil {
					return used, err
				}
			}
		}
	}
	var templates resourceapi.ResourceClaimTemplateList
	if err := r.List(ctx, &templates); err != nil {
		return false, err
	}
	for _, template := range templates.Items {
		used, err := claimSpecReferences(template.Spec.Spec, names, ref)
		if used || err != nil {
			return used, err
		}
	}
	var attachments api.NetworkAttachmentStatusList
	if err := r.List(ctx, &attachments); err != nil {
		return false, err
	}
	for _, attachment := range attachments.Items {
		if attachment.Spec.RevisionRef.UID == ref.UID && attachment.Status.Phase != "Removed" {
			return true, nil
		}
	}
	return false, nil
}

func (r *RetentionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	retry := ctrl.Result{RequeueAfter: 10 * time.Second}
	var revision api.NetworkTopologyRevision
	if err := r.Get(ctx, req.NamespacedName, &revision); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if revision.Labels[ManagedLabel] != ManagedLabelValue || !slices.Contains(revision.Finalizers, RevisionFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := topology.VerifyRevision(revision.Spec); err != nil {
		return retry, err
	}
	var sources api.NetworkTopologyList
	if err := r.List(ctx, &sources); err != nil {
		return retry, err
	}
	for _, source := range sources.Items {
		if string(source.UID) == revision.Spec.SourceUID && source.DeletionTimestamp.IsZero() {
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
	}
	ref := topology.RevisionIdentity(revision)
	token := topology.RetirementToken(ref)
	if revision.Annotations[topology.RetirementAnnotation] != token {
		before := revision.DeepCopy()
		if revision.Annotations == nil {
			revision.Annotations = map[string]string{}
		}
		revision.Annotations[topology.RetirementAnnotation] = token
		return retry, r.Patch(ctx, &revision, client.MergeFrom(before))
	}
	classes, err := r.revisionClasses(ctx, revision)
	if err != nil {
		return retry, err
	}
	used, err := r.revisionInUse(ctx, ref, classes)
	if used || err != nil {
		return retry, err
	}
	reports, err := r.healthyReports(ctx)
	if err != nil {
		return retry, err
	}
	for _, report := range reports {
		if !slices.Contains(report.DrainedRevisions, ref) || slices.ContainsFunc(report.RetainedRevisions, func(retained api.RevisionRef) bool { return retained.UID == ref.UID }) {
			return retry, nil
		}
	}
	for _, class := range classes {
		if err := r.Delete(ctx, &class, client.Preconditions{UID: &class.UID, ResourceVersion: &class.ResourceVersion}); client.IgnoreNotFound(err) != nil {
			return retry, err
		}
	}
	// Recheck actual class deletion before releasing the revision's finalizer.
	classes, err = r.revisionClasses(ctx, revision)
	if err != nil || len(classes) != 0 {
		return retry, err
	}
	if revision.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, &revision, client.Preconditions{UID: &revision.UID, ResourceVersion: &revision.ResourceVersion}); err != nil {
			return retry, err
		}
		return retry, nil
	}
	before := revision.DeepCopy()
	revision.Finalizers = slices.DeleteFunc(revision.Finalizers, func(f string) bool { return f == RevisionFinalizer })
	return ctrl.Result{}, r.Patch(ctx, &revision, client.MergeFrom(before))
}

func (r *RetentionReconciler) descriptorInUse(ctx context.Context, name string) (bool, error) {
	var policies api.DeviceExposurePolicyList
	if err := r.List(ctx, &policies); err != nil {
		return false, err
	}
	for _, policy := range policies.Items {
		if policy.Spec.Exposure != nil && policy.Spec.Exposure.Plugin == name {
			return true, nil
		}
	}
	var sources api.NetworkTopologyList
	if err := r.List(ctx, &sources); err != nil {
		return false, err
	}
	for _, source := range sources.Items {
		for _, step := range source.Spec.Steps {
			if step.PluginRef.Name == name {
				return true, nil
			}
		}
	}
	var revisions api.NetworkTopologyRevisionList
	if err := r.List(ctx, &revisions); err != nil {
		return false, err
	}
	for _, revision := range revisions.Items {
		for _, locked := range revision.Spec.Plugins {
			if locked.Name == name {
				return true, nil
			}
		}
	}
	reports, err := r.healthyReports(ctx)
	if err != nil {
		return false, err
	}
	for _, report := range reports {
		if slices.Contains(report.RetainedPlugins, name) {
			return true, nil
		}
	}
	return false, nil
}

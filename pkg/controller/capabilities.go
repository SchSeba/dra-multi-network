// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package controller

import (
	"context"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type CapabilityReconciler struct {
	client.Client
	Namespace string
}

func (r *CapabilityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	allNodes := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []ctrl.Request {
		var nodes corev1.NodeList
		if err := r.List(ctx, &nodes); err != nil {
			return nil
		}
		requests := make([]ctrl.Request, 0, len(nodes.Items))
		for _, node := range nodes.Items {
			requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKey{Name: node.Name}})
		}
		return requests
	})
	return ctrl.NewControllerManagedBy(mgr).For(&corev1.Node{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []ctrl.Request {
			pod := obj.(*corev1.Pod)
			if pod.Namespace != r.Namespace || pod.Spec.NodeName == "" {
				return nil
			}
			return []ctrl.Request{{NamespacedName: client.ObjectKey{Name: pod.Spec.NodeName}}}
		})).Watches(&api.NetworkTopologyRevision{}, allNodes).Watches(&api.NetworkTopology{}, allNodes).Complete(r)
}

func (r *CapabilityReconciler) ValidAgent(ctx context.Context, node corev1.Node, report topology.NodeCapabilities) (bool, error) {
	if report.NodeUID != string(node.UID) || report.BootID != node.Status.NodeInfo.BootID {
		return false, nil
	}
	ready := false
	for _, condition := range node.Status.Conditions {
		ready = ready || condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
	}
	if !ready {
		return false, nil
	}
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: report.PodName}, &pod); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if string(pod.UID) != report.PodUID || pod.Spec.NodeName != node.Name || pod.Spec.ServiceAccountName != "dra-multi-network-node" || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning {
		return false, nil
	}
	ready = false
	for _, condition := range pod.Status.Conditions {
		ready = ready || condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
	}
	containerReady := false
	for _, status := range pod.Status.ContainerStatuses {
		containerReady = containerReady || status.Name == "plugin" && status.Ready && status.State.Running != nil && status.ContainerID == report.ContainerID && status.ImageID == report.ImageID
	}
	if !ready || !containerReady {
		return false, nil
	}
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.APIVersion != "apps/v1" || owner.Kind != "DaemonSet" {
		return false, nil
	}
	var ds appsv1.DaemonSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: owner.Name}, &ds); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return ds.UID == owner.UID && ds.DeletionTimestamp.IsZero() && ds.Spec.Template.Spec.ServiceAccountName == pod.Spec.ServiceAccountName, nil
}

func (r *CapabilityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := node.DeepCopy()
	for key := range node.Labels {
		if strings.HasPrefix(key, topology.CapabilityLabelPrefix) {
			delete(node.Labels, key)
		}
	}
	report, err := topology.ParseCapabilities(node.Annotations[topology.CapabilityAnnotation], time.Now())
	valid := false
	if err == nil {
		valid, err = r.ValidAgent(ctx, node, report)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	if valid {
		var revisions api.NetworkTopologyRevisionList
		if err := r.List(ctx, &revisions); err != nil {
			return ctrl.Result{}, err
		}
		var sources api.NetworkTopologyList
		if err := r.List(ctx, &sources); err != nil {
			return ctrl.Result{}, err
		}
		live := map[string]bool{}
		for _, source := range sources.Items {
			if source.DeletionTimestamp.IsZero() {
				live[string(source.UID)] = true
			}
		}
		for _, revision := range revisions.Items {
			if revision.DeletionTimestamp.IsZero() && revision.Annotations[topology.RetirementAnnotation] == "" && live[revision.Spec.SourceUID] && meta.IsStatusConditionTrue(revision.Status.Conditions, ReadyCondition) && topology.SupportsRevision(report, revision.Spec) {
				if node.Labels == nil {
					node.Labels = map[string]string{}
				}
				node.Labels[topology.RevisionCapabilityLabel(revision.Spec.Digest)] = topology.CapabilityReadyValue
			}
		}
	}
	if topology.Digest(before.Labels) != topology.Digest(node.Labels) {
		if err := r.Patch(ctx, &node, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

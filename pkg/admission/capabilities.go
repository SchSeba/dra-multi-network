// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/containerd/nri/pkg/plugin"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/controller"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func (s *Server) podRevision(ctx context.Context, pod corev1.Pod, requireActive bool) (*api.NetworkTopologyRevision, error) {
	var selected *api.NetworkTopologyRevision
	for _, ref := range pod.Spec.ResourceClaims {
		var spec resourceapi.ResourceClaimSpec
		switch {
		case ref.ResourceClaimTemplateName != nil:
			var t resourceapi.ResourceClaimTemplate
			if err := s.Client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: *ref.ResourceClaimTemplateName}, &t); err != nil {
				return nil, err
			}
			spec = t.Spec.Spec
		case ref.ResourceClaimName != nil:
			var c resourceapi.ResourceClaim
			if err := s.Client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: *ref.ResourceClaimName}, &c); err != nil {
				return nil, err
			}
			spec = c.Spec
		default:
			return nil, fmt.Errorf("E_CLAIM_REFERENCE")
		}
		revision, err := s.claimRevision(ctx, spec)
		if err != nil {
			return nil, err
		}
		if revision != nil {
			if selected != nil {
				return nil, fmt.Errorf("E_WORKLOAD_PROFILE: one project topology claim per Pod")
			}
			selected = revision
		}
	}
	if requireActive {
		if err := s.requireActiveRevision(ctx, selected); err != nil {
			return nil, err
		}
	}
	return selected, nil
}

// Existing consumers keep their locked cleanup contract. New consumers must
// check the API directly rather than wait for eventual node-label withdrawal.
func (s *Server) requireActiveRevision(ctx context.Context, revision *api.NetworkTopologyRevision) error {
	if revision == nil {
		return nil
	}
	if !revision.DeletionTimestamp.IsZero() || revision.Annotations[topology.RetirementAnnotation] != "" {
		return fmt.Errorf("E_REVISION_RETIRED")
	}
	for _, locked := range revision.Spec.Plugins {
		var p api.NetworkPlugin
		if err := s.Client.Get(ctx, client.ObjectKey{Name: locked.Name}, &p); err != nil {
			// Builtins are also available from the pinned executable registry.
			if apierrors.IsNotFound(err) && locked.Spec.Implementation.Type == topology.BuiltinImplementation {
				continue
			}
			return err
		}
		if !p.DeletionTimestamp.IsZero() {
			return fmt.Errorf("E_PLUGIN_RETIRED: %s", p.Name)
		}
	}
	// ponytail: scan the small topology set; add a source-name locator to
	// revisions if this lookup becomes costly, retaining UID verification.
	var sources api.NetworkTopologyList
	if err := s.Client.List(ctx, &sources); err != nil {
		return err
	}
	for _, source := range sources.Items {
		if string(source.UID) == revision.Spec.SourceUID && source.DeletionTimestamp.IsZero() {
			return nil
		}
	}
	return fmt.Errorf("E_TOPOLOGY_RETIRED: source topology is missing or deleting")
}

func requireRevisionAffinity(pod *corev1.Pod, digest string) {
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	if pod.Annotations[plugin.RequiredPluginsAnnotation] == "" {
		pod.Annotations[plugin.RequiredPluginsAnnotation] = `["dra-multi-network"]`
	}
	if pod.Spec.NodeSelector == nil {
		pod.Spec.NodeSelector = map[string]string{}
	}
	if _, exists := pod.Spec.NodeSelector[api.Driver+"/ready"]; !exists {
		pod.Spec.NodeSelector[api.Driver+"/ready"] = topology.CapabilityReadyValue
	}
	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &corev1.Affinity{}
	}
	if pod.Spec.Affinity.NodeAffinity == nil {
		pod.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	affinity := pod.Spec.Affinity.NodeAffinity
	if affinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		affinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: topology.RevisionCapabilityLabel(digest), Operator: corev1.NodeSelectorOpIn, Values: []string{topology.CapabilityReadyValue}}}}}}
		return
	}
	for i := range affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		term := &affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i]
		// An empty existing term matches no nodes. Do not make it schedulable.
		if len(term.MatchExpressions)+len(term.MatchFields) == 0 {
			continue
		}
		if !revisionTerm(*term, digest) {
			term.MatchExpressions = append(term.MatchExpressions, corev1.NodeSelectorRequirement{Key: topology.RevisionCapabilityLabel(digest), Operator: corev1.NodeSelectorOpIn, Values: []string{topology.CapabilityReadyValue}})
		}
	}
}

func revisionTerm(term corev1.NodeSelectorTerm, digest string) bool {
	return slices.ContainsFunc(term.MatchExpressions, func(expression corev1.NodeSelectorRequirement) bool {
		return expression.Key == topology.RevisionCapabilityLabel(digest) && expression.Operator == corev1.NodeSelectorOpIn && slices.Equal(expression.Values, []string{topology.CapabilityReadyValue})
	})
}

func hasRevisionAffinity(pod corev1.Pod, digest string) bool {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil || pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return false
	}
	for _, term := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		if len(term.MatchExpressions)+len(term.MatchFields) != 0 && !revisionTerm(term, digest) {
			return false
		}
	}
	return true
}

func (s *Server) mutate(ctx context.Context, req *admissionv1.AdmissionRequest) ([]byte, error) {
	if req.Kind.Kind != "Pod" || req.Operation != admissionv1.Create {
		return nil, nil
	}
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return nil, err
	}
	revision, err := s.podRevision(ctx, pod, true)
	if err != nil || revision == nil {
		return nil, err
	}
	requireRevisionAffinity(&pod, revision.Spec.Digest)
	const opAdd = "add"
	return json.Marshal([]struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}{
		{opAdd, "/metadata/annotations", pod.Annotations},
		{opAdd, "/spec/nodeSelector", pod.Spec.NodeSelector},
		{opAdd, "/spec/affinity", pod.Spec.Affinity},
	})
}

func (s *Server) validateRevisionAffinity(req *admissionv1.AdmissionRequest, pod corev1.Pod, digest string) error {
	if hasRevisionAffinity(pod, digest) {
		return nil
	}
	// Already running workloads from before this gate keep their immutable
	// placement. API-supplied oldObject, UID and unchanged claim references prove
	// the exemption; clients cannot claim it on a new Pod.
	if req.Operation == admissionv1.Update && len(req.OldObject.Raw) != 0 {
		var old corev1.Pod
		if err := json.Unmarshal(req.OldObject.Raw, &old); err != nil {
			return err
		}
		protected := strings.Contains(string(req.OldObject.Raw), topology.CapabilityLabelPrefix)
		if !protected && old.UID == pod.UID && topology.Digest(old.Spec.ResourceClaims) == topology.Digest(pod.Spec.ResourceClaims) {
			return nil
		}
	}
	return fmt.Errorf("E_REVISION_AFFINITY: every eligible node must support the bound revision")
}

func (s *Server) validateCapabilityMetadata(user authenticationv1.UserInfo, old, node corev1.Node) error {
	username := user.Username
	changedReport := old.Annotations[topology.CapabilityAnnotation] != node.Annotations[topology.CapabilityAnnotation]
	changedReady := old.Labels[api.Driver+"/ready"] != node.Labels[api.Driver+"/ready"]
	// The API server supplies verified Pod-bound token claims. A shared service
	// account name alone cannot authorize an agent to refresh another node.
	if s.node(username) && (changedReport || changedReady) {
		if !slices.Equal(user.Extra["authentication.kubernetes.io/node-name"], []string{node.Name}) || !slices.Equal(user.Extra["authentication.kubernetes.io/node-uid"], []string{string(node.UID)}) {
			return fmt.Errorf("E_NODE_CAPABILITY_IDENTITY: token is not bound to this node")
		}
	}
	for _, labels := range []map[string]string{old.Labels, node.Labels} {
		for key := range labels {
			if strings.HasPrefix(key, topology.CapabilityLabelPrefix) && old.Labels[key] != node.Labels[key] && !s.controller(username) {
				return fmt.Errorf("E_NODE_CAPABILITY_AUTHORITY: revision labels are controller owned")
			}
		}
	}
	if changedReady && !s.node(username) {
		return fmt.Errorf("E_NODE_CAPABILITY_AUTHORITY")
	}
	if changedReport {
		if !s.node(username) {
			return fmt.Errorf("E_NODE_CAPABILITY_AUTHORITY: report is node-agent owned")
		}
		if raw := node.Annotations[topology.CapabilityAnnotation]; raw != "" {
			report, err := topology.ParseCapabilities(raw, time.Now())
			if err != nil {
				return err
			}
			if report.NodeUID != string(node.UID) {
				return fmt.Errorf("E_NODE_CAPABILITY_IDENTITY")
			}
			if !slices.Equal(user.Extra["authentication.kubernetes.io/pod-name"], []string{report.PodName}) || !slices.Equal(user.Extra["authentication.kubernetes.io/pod-uid"], []string{report.PodUID}) {
				return fmt.Errorf("E_NODE_CAPABILITY_IDENTITY: report is not from the token's Pod")
			}
		}
	}
	return nil
}

func (s *Server) validateBinding(ctx context.Context, req *admissionv1.AdmissionRequest) error {
	var binding corev1.Binding
	if err := json.Unmarshal(req.Object.Raw, &binding); err != nil {
		return err
	}
	name := req.Name
	if name == "" {
		name = binding.Name // Legacy bindings have no Pod name in the URL.
	}
	var pod corev1.Pod
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: name}, &pod); err != nil {
		return err
	}
	revision, err := s.podRevision(ctx, pod, true)
	if err != nil || revision == nil {
		return err
	}
	if binding.UID != pod.UID {
		return fmt.Errorf("E_BINDING_POD_IDENTITY: bind the exact Pod UID")
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	for key, value := range binding.Annotations {
		pod.Annotations[key] = value
	}
	if err := s.validateRuntimeAnnotations(req.UserInfo.Username, pod, true); err != nil {
		return err
	}
	if binding.Target.Kind != "Node" {
		return fmt.Errorf("E_DIRECT_BINDING: expected a capable Node")
	}
	var node corev1.Node
	if err := s.Client.Get(ctx, client.ObjectKey{Name: binding.Target.Name}, &node); err != nil {
		return err
	}
	matches, err := nodeaffinity.GetRequiredNodeAffinity(&pod).Match(&node)
	if err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("E_NODE_CAPABILITY: binding target does not match required placement")
	}
	report, err := topology.ParseCapabilities(node.Annotations[topology.CapabilityAnnotation], time.Now())
	if err != nil {
		return err
	}
	valid, err := (&controller.CapabilityReconciler{Client: s.Client, Namespace: s.Namespace}).ValidAgent(ctx, node, report)
	if err != nil {
		return err
	}
	if !valid || node.Labels[topology.RevisionCapabilityLabel(revision.Spec.Digest)] != topology.CapabilityReadyValue || !topology.SupportsRevision(report, revision.Spec) {
		return fmt.Errorf("E_NODE_CAPABILITY: binding target has no verified revision capability")
	}
	return nil
}

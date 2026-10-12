// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package admission

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/containerd/nri/pkg/plugin"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/multinetwork"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type Server struct {
	Client    client.Client
	Namespace string
	Service   string
	Ready     chan struct{}
}

func (s *Server) Start(ctx context.Context) error {
	name := s.Service + "-tls"
	var secret corev1.Secret
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &secret)
	if apierrors.IsNotFound(err) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return err
		}
		cert := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: s.Service + "." + s.Namespace + ".svc"}, DNSNames: []string{s.Service + "." + s.Namespace + ".svc"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, &cert, &cert, &key.PublicKey, key)
		if err != nil {
			return err
		}
		secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "tls.key": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})}}
		if err := s.Client.Create(ctx, &secret); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	cert, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", ":9443") // #nosec G102 -- TLS webhook must be reachable through the cluster Service.
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/validate", s.handle)
	mux.HandleFunc("/mutate", s.handle)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			klog.ErrorS(err, "webhook shutdown")
		}
	}()
	// Configure trust only once the socket is listening. FailurePolicy remains Fail.
	var webhook admissionregistrationv1.ValidatingWebhookConfiguration
	if err := s.Client.Get(ctx, client.ObjectKey{Name: s.Service}, &webhook); err != nil {
		return errors.Join(err, listener.Close())
	}
	before := webhook.DeepCopy()
	for i := range webhook.Webhooks {
		webhook.Webhooks[i].ClientConfig.CABundle = secret.Data["tls.crt"]
	}
	if err := s.Client.Patch(ctx, &webhook, client.MergeFrom(before)); err != nil {
		return errors.Join(err, listener.Close())
	}
	var mutating admissionregistrationv1.MutatingWebhookConfiguration
	if err := s.Client.Get(ctx, client.ObjectKey{Name: s.Service}, &mutating); err != nil {
		return errors.Join(err, listener.Close())
	}
	beforeMutation := mutating.DeepCopy()
	for i := range mutating.Webhooks {
		mutating.Webhooks[i].ClientConfig.CABundle = secret.Data["tls.crt"]
	}
	if err := s.Client.Patch(ctx, &mutating, client.MergeFrom(beforeMutation)); err != nil {
		return errors.Join(err, listener.Close())
	}
	close(s.Ready)
	err = server.ServeTLS(listener, "", "")
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
func (s *Server) NeedLeaderElection() bool { return false }
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024)).Decode(&review); err != nil || review.Request == nil {
		http.Error(w, "invalid AdmissionReview", http.StatusBadRequest)
		return
	}
	response := &admissionv1.AdmissionResponse{UID: review.Request.UID, Allowed: true}
	var err error
	if r.URL.Path == "/mutate" {
		response.Patch, err = s.mutate(r.Context(), review.Request)
		if len(response.Patch) != 0 {
			patchType := admissionv1.PatchTypeJSONPatch
			response.PatchType = &patchType
		}
	} else {
		err = s.validate(r.Context(), review.Request)
	}
	if err != nil {
		response.Allowed = false
		response.Result = &metav1.Status{Message: err.Error(), Reason: metav1.StatusReasonInvalid, Code: 422}
	}
	review.Request = nil
	review.Response = response
	review.APIVersion = "admission.k8s.io/v1"
	review.Kind = "AdmissionReview"
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(review); err != nil {
		klog.ErrorS(err, "write admission response")
	}
}
func (s *Server) controller(username string) bool {
	return username == "system:serviceaccount:"+s.Namespace+":dra-multi-network-controller"
}
func (s *Server) node(username string) bool {
	return username == "system:serviceaccount:"+s.Namespace+":dra-multi-network-node"
}
func (s *Server) validate(ctx context.Context, req *admissionv1.AdmissionRequest) error {
	switch req.Kind.Kind {
	case "NetworkTopology":
		if req.Operation == admissionv1.Delete {
			return nil
		}
		var t api.NetworkTopology
		if err := topology.Decode(req.Object.Raw, &t); err != nil {
			return err
		}
		var list api.NetworkPluginList
		if err := s.Client.List(ctx, &list); err != nil {
			return err
		}
		plugins := map[string]api.NetworkPlugin{}
		for _, p := range list.Items {
			if p.DeletionTimestamp.IsZero() {
				plugins[p.Name] = p
			}
		}
		_, err := topology.Compile(t, plugins)
		return err
	case "NetworkPlugin":
		if req.Operation == admissionv1.Delete {
			var p api.NetworkPlugin
			if err := topology.Decode(req.OldObject.Raw, &p); err != nil {
				return err
			}
			for _, builtin := range topology.Builtins() {
				if p.Name == builtin.Name {
					return fmt.Errorf("E_BUILTIN_CONTRACT: installed build owns %s", p.Name)
				}
			}
			if !slices.Contains(p.Finalizers, topology.PluginRetentionFinalizer) {
				return fmt.Errorf("E_RETAINED_CONTRACT: wait for descriptor reconciliation before deletion")
			}
			return nil
		}
		var p api.NetworkPlugin
		if err := topology.Decode(req.Object.Raw, &p); err != nil {
			return err
		}
		if len(req.OldObject.Raw) > 0 && !s.controller(req.UserInfo.Username) {
			var old api.NetworkPlugin
			if err := topology.Decode(req.OldObject.Raw, &old); err != nil {
				return err
			}
			if slices.Contains(old.Finalizers, topology.PluginRetentionFinalizer) && !slices.Contains(p.Finalizers, topology.PluginRetentionFinalizer) {
				return fmt.Errorf("E_CONTRACT_FINALIZER_AUTHORITY")
			}
		}
		return topology.ValidatePlugin(p)
	case "DeviceExposurePolicy":
		if req.Operation == admissionv1.Delete {
			return nil
		}
		var p api.DeviceExposurePolicy
		if err := topology.Decode(req.Object.Raw, &p); err != nil {
			return err
		}
		var plugins api.NetworkPluginList
		if err := s.Client.List(ctx, &plugins); err != nil {
			return err
		}
		return multinetwork.ValidatePolicy(p, topology.PluginRegistry(plugins.Items))
	case "NetworkTopologyRevision":
		if !s.controller(req.UserInfo.Username) {
			return fmt.Errorf("E_REVISION_AUTHORITY: controller owned")
		}
		return nil
	case "NetworkAttachmentStatus":
		// Status is a projection, not cleanup authority. RBAC-authorized deletion
		// must work for namespace collection; the node journal retains ownership.
		if req.Operation == admissionv1.Delete {
			return nil
		}
		if !s.node(req.UserInfo.Username) {
			return fmt.Errorf("E_STATUS_AUTHORITY: node agent owned")
		}
		return nil
	case "DeviceClass":
		var class resourceapi.DeviceClass
		raw := req.Object.Raw
		if req.Operation == admissionv1.Delete {
			raw = req.OldObject.Raw
		}
		if err := json.Unmarshal(raw, &class); err != nil {
			return err
		}
		for _, config := range class.Spec.Config {
			if config.Opaque != nil && config.Opaque.Driver == api.Driver && !s.controller(req.UserInfo.Username) {
				return fmt.Errorf("E_CLASS_AUTHORITY: topology classes are controller owned")
			}
		}
		if len(req.OldObject.Raw) > 0 {
			var old resourceapi.DeviceClass
			if err := json.Unmarshal(req.OldObject.Raw, &old); err != nil {
				return err
			}
			for _, config := range old.Spec.Config {
				if config.Opaque != nil && config.Opaque.Driver == api.Driver && !s.controller(req.UserInfo.Username) {
					return fmt.Errorf("E_CLASS_AUTHORITY")
				}
			}
		}
	case "ResourceClaim", "ResourceClaimTemplate":
		if req.Operation == admissionv1.Delete || req.SubResource != "" {
			return nil
		}
		var spec resourceapi.ResourceClaimSpec
		if req.Kind.Kind == "ResourceClaim" {
			var claim resourceapi.ResourceClaim
			if err := json.Unmarshal(req.Object.Raw, &claim); err != nil {
				return err
			}
			spec = claim.Spec
		} else {
			var template resourceapi.ResourceClaimTemplate
			if err := json.Unmarshal(req.Object.Raw, &template); err != nil {
				return err
			}
			spec = template.Spec.Spec
		}
		return s.claimSpec(ctx, spec, req.Operation == admissionv1.Create)
	case "Pod":
		if req.Operation == admissionv1.Delete {
			return nil
		}
		var pod corev1.Pod
		if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
			return err
		}
		if err := s.validateRuntimeAnnotations(req.UserInfo.Username, pod, false); err != nil {
			return err
		}
		revision, err := s.podRevision(ctx, pod, req.Operation == admissionv1.Create)
		if err != nil {
			return err
		}
		if revision != nil {
			if err := s.validateRuntimeAnnotations(req.UserInfo.Username, pod, true); err != nil {
				return err
			}
			if pod.Spec.HostNetwork {
				return fmt.Errorf("E_HOST_NETWORK")
			}
			if pod.Spec.NodeName != "" && req.Operation == admissionv1.Create {
				return fmt.Errorf("E_DIRECT_BINDING: use scheduler")
			}
			if pod.Spec.NodeSelector[api.Driver+"/ready"] != topology.CapabilityReadyValue {
				return fmt.Errorf("E_NODE_CAPABILITY: set nodeSelector dra-multi-network.schseba.github.io/ready: 'true'")
			}
			if err := s.validateRevisionAffinity(req, pod, revision.Spec.Digest); err != nil {
				return err
			}
		}
	case "Binding":
		return s.validateBinding(ctx, req)
	case "Node":
		if req.Operation != admissionv1.Update && req.Operation != admissionv1.Create {
			return nil
		}
		var old, node corev1.Node
		if req.Operation == admissionv1.Update {
			if err := json.Unmarshal(req.OldObject.Raw, &old); err != nil {
				return err
			}
		}
		if err := json.Unmarshal(req.Object.Raw, &node); err != nil {
			return err
		}
		if err := s.validateCapabilityMetadata(req.UserInfo, old, node); err != nil {
			return err
		}
	}
	return nil
}
func (s *Server) validateRuntimeAnnotations(username string, pod corev1.Pod, project bool) error {
	for key, value := range pod.Annotations {
		if strings.HasPrefix(key, "cdi.k8s.io/") && strings.Contains(value, api.Driver+"/") {
			return fmt.Errorf("E_DEVICE_ACCESS_ORIGIN: project CDI access must come from the allocated claim")
		}
		bootstrap := api.Driver + "/bootstrap-nri-toleration"
		if (key == bootstrap || strings.HasPrefix(key, bootstrap+"/")) && (project || (!s.controller(username) && !s.node(username) && pod.Namespace != s.Namespace)) {
			return fmt.Errorf("E_BOOTSTRAP_AUTHORITY: only operator bootstrap Pods may tolerate an absent agent")
		}
	}
	if !project {
		return nil
	}
	var required []string
	if err := json.Unmarshal([]byte(pod.Annotations[plugin.RequiredPluginsAnnotation]), &required); err != nil || len(required) != 1 || required[0] != "dra-multi-network" {
		return fmt.Errorf("E_REQUIRED_PLUGIN: set required-plugins.noderesource.dev: '[\"dra-multi-network\"]'")
	}
	for key, value := range pod.Annotations {
		if strings.HasPrefix(key, plugin.RequiredPluginsAnnotation+"/") {
			var scoped []string
			if err := json.Unmarshal([]byte(value), &scoped); err != nil || len(scoped) != 1 || scoped[0] != "dra-multi-network" {
				return fmt.Errorf("E_REQUIRED_PLUGIN: container-scoped override cannot remove the agent")
			}
		}
	}
	return nil
}
func (s *Server) claimSpec(ctx context.Context, spec resourceapi.ResourceClaimSpec, requireActive bool) error {
	revision, err := s.claimRevision(ctx, spec)
	if err != nil || !requireActive {
		return err
	}
	return s.requireActiveRevision(ctx, revision)
}
func (s *Server) claimRevision(ctx context.Context, spec resourceapi.ResourceClaimSpec) (*api.NetworkTopologyRevision, error) {
	classes := map[string]resourceapi.DeviceClass{}
	owned := map[int]bool{}
	project := false
	for i, request := range spec.Devices.Requests {
		names := []string{}
		if request.Exactly != nil {
			names = append(names, request.Exactly.DeviceClassName)
		}
		for _, sub := range request.FirstAvailable {
			names = append(names, sub.DeviceClassName)
		}
		for _, name := range names {
			class, exists := classes[name]
			if !exists {
				if err := s.Client.Get(ctx, client.ObjectKey{Name: name}, &class); err != nil {
					return nil, err
				}
				classes[name] = class
			}
			for _, config := range class.Spec.Config {
				if config.Opaque != nil && config.Opaque.Driver == api.Driver {
					owned[i], project = true, true
				}
			}
		}
	}
	if !project {
		return nil, nil
	}
	for _, config := range spec.Devices.Config {
		if config.Opaque != nil && config.Opaque.Driver == api.Driver {
			return nil, fmt.Errorf("E_BINDING_ORIGIN: claim configuration cannot override topology")
		}
	}
	bindings := map[string]bool{}
	var ref api.RevisionRef
	var revision api.NetworkTopologyRevision
	for i, request := range spec.Devices.Requests {
		if !owned[i] {
			continue
		}
		if request.Exactly == nil || len(request.FirstAvailable) > 0 || request.Exactly.Count != 1 || request.Exactly.AdminAccess != nil && *request.Exactly.AdminAccess || request.Exactly.AllocationMode != "" && request.Exactly.AllocationMode != resourceapi.DeviceAllocationModeExactCount {
			return nil, fmt.Errorf("E_REQUEST_PROFILE")
		}
		class := classes[request.Exactly.DeviceClassName]
		count := 0
		var binding api.TopologyRootConfiguration
		for _, config := range class.Spec.Config {
			if config.Opaque != nil && config.Opaque.Driver == api.Driver {
				count++
				if err := topology.Decode(config.Opaque.Parameters.Raw, &binding); err != nil {
					return nil, err
				}
			}
		}
		if count != 1 || binding.Kind != "TopologyRootConfiguration" || binding.APIVersion != api.GroupVersion.String() || bindings[binding.RootStep] {
			return nil, fmt.Errorf("E_ROOT_COVERAGE")
		}
		bindings[binding.RootStep] = true
		if ref.Name == "" {
			ref = binding.RevisionRef
			if err := s.Client.Get(ctx, client.ObjectKey{Name: ref.Name}, &revision); err != nil {
				return nil, err
			}
			if string(revision.UID) != ref.UID || revision.Spec.Digest != ref.Digest || !meta.IsStatusConditionTrue(revision.Status.Conditions, "Ready") {
				return nil, fmt.Errorf("E_REVISION_BINDING")
			}
		} else if ref != binding.RevisionRef {
			return nil, fmt.Errorf("E_REVISION_MIXED")
		}
	}
	roots := 0
	for _, step := range revision.Spec.Topology.Steps {
		if step.Kind == "Allocation" {
			roots++
			if !bindings[step.ID] {
				return nil, fmt.Errorf("E_ROOT_COVERAGE: missing %s", step.ID)
			}
		}
	}
	if roots != len(bindings) {
		return nil, fmt.Errorf("E_ROOT_COVERAGE")
	}
	return &revision, nil
}

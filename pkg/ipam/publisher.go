// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package ipam

import (
	"context"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type Publisher struct {
	Provider   Provider
	Client     client.Client
	KubeClient kubernetes.Interface
}

func (*Publisher) NeedLeaderElection() bool { return true }
func (p *Publisher) Start(ctx context.Context) error {
	helper, err := resourceslice.StartController(ctx, resourceslice.Options{DriverName: topology.IPAMDriver, KubeClient: p.KubeClient, Resources: &resourceslice.DriverResources{Pools: map[string]resourceslice.Pool{}}, SyncDelay: ptr.To(time.Second)})
	if err != nil {
		return err
	}
	defer helper.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		resources, err := p.resources(ctx)
		if err != nil {
			klog.ErrorS(err, "IPAM inventory fenced")
			resources = resourceslice.DriverResources{Pools: map[string]resourceslice.Pool{}}
		}
		helper.Update(&resources)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (p *Publisher) resources(ctx context.Context) (resourceslice.DriverResources, error) {
	resources := resourceslice.DriverResources{Pools: map[string]resourceslice.Pool{}}
	var claims resourceapi.ResourceClaimList
	if err := p.Client.List(ctx, &claims); err != nil {
		return resources, err
	}
	fingerprints := map[string]string{}
	for _, claim := range claims.Items {
		if claim.Status.Allocation != nil {
			fingerprints[string(claim.UID)] = Fingerprint(&claim)
		}
	}
	pools, err := p.Provider.Inventory(ctx, fingerprints)
	if err != nil {
		return resources, err
	}
	for _, pool := range pools {
		var keys []string
		for key := range pool.Config.NodeSelector {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		term := corev1.NodeSelectorTerm{}
		for _, key := range keys {
			term.MatchExpressions = append(term.MatchExpressions, corev1.NodeSelectorRequirement{Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{pool.Config.NodeSelector[key]}})
		}
		d := resourceapi.Device{Name: "subnet", AllowMultipleAllocations: ptr.To(true), Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
			resourceapi.QualifiedName(topology.IPAMDriver + "/network"): {StringValue: ptr.To(pool.Config.Network)}, resourceapi.QualifiedName(topology.IPAMDriver + "/subnet"): {StringValue: ptr.To(pool.Config.Subnet)}, resourceapi.QualifiedName(topology.IPAMDriver + "/allocationReady"): {BoolValue: ptr.To(pool.AllocationReady)},
		}, Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{resourceapi.QualifiedName(topology.AddressCapacity): {Value: resource.MustParse(strconv.Itoa(pool.Capacity)), RequestPolicy: &resourceapi.CapacityRequestPolicy{Default: ptr.To(resource.MustParse("1")), ValidValues: []resource.Quantity{resource.MustParse("1")}}}}}
		resources.Pools[pool.Name] = resourceslice.Pool{NodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{term}}, Slices: []resourceslice.Slice{{Devices: []resourceapi.Device{d}}}}
	}
	return resources, nil
}

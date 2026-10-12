// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package ipam

import (
	"context"
	"fmt"
	"net/netip"
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
	Store      Store
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
	pools, err := p.Store.Pools(ctx)
	if err != nil {
		return resources, err
	}
	var claims resourceapi.ResourceClaimList
	if err := p.Store.Client.List(ctx, &claims); err != nil {
		return resources, err
	}
	fingerprints := map[string]string{}
	for _, claim := range claims.Items {
		if claim.Status.Allocation != nil {
			fingerprints[string(claim.UID)] = Fingerprint(&claim)
		}
	}
	for i, pool := range pools {
		if err := p.Store.ValidatePrimaryRanges(ctx, pool); err != nil {
			return resources, err
		}
		prefix, _ := netip.ParsePrefix(pool.Config.Subnet)
		for j := 0; j < i; j++ {
			other, _ := netip.ParsePrefix(pools[j].Config.Subnet)
			if prefix.Overlaps(other) {
				return resources, fmt.Errorf("E_IPAM_POOL_OVERLAP")
			}
		}
		if !pool.Object.DeletionTimestamp.IsZero() {
			continue
		}
		if err := p.Store.EnsureLedger(ctx, pool); err != nil {
			return resources, err
		}
		pool, err = p.Store.Load(ctx, pool.Object.Name)
		if err != nil {
			return resources, err
		}
		_, leases, err := p.Store.ledger(ctx, pool)
		if err != nil {
			return resources, err
		}
		ready := true
		for _, lease := range leases {
			if lease.State != Released && fingerprints[lease.ClaimUID] != lease.Fingerprint {
				ready = false
			}
		}
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
			resourceapi.QualifiedName(topology.IPAMDriver + "/network"): {StringValue: ptr.To(pool.Config.Network)}, resourceapi.QualifiedName(topology.IPAMDriver + "/subnet"): {StringValue: ptr.To(pool.Config.Subnet)}, resourceapi.QualifiedName(topology.IPAMDriver + "/allocationReady"): {BoolValue: ptr.To(ready)},
		}, Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{resourceapi.QualifiedName(topology.AddressCapacity): {Value: resource.MustParse(strconv.Itoa(len(pool.Addresses))), RequestPolicy: &resourceapi.CapacityRequestPolicy{Default: ptr.To(resource.MustParse("1")), ValidValues: []resource.Quantity{resource.MustParse("1")}}}}}
		resources.Pools[pool.Object.Name] = resourceslice.Pool{NodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{term}}, Slices: []resourceslice.Slice{{Devices: []resourceapi.Device{d}}}}
	}
	return resources, p.gcLedgers(ctx, pools)
}
func (p *Publisher) gcLedgers(ctx context.Context, pools []Pool) error {
	names := map[string]bool{}
	for _, pool := range pools {
		names[LedgerName(pool)] = true
	}
	var ledgers corev1.ConfigMapList
	if err := p.Store.Client.List(ctx, &ledgers, client.InNamespace(p.Store.Namespace), client.MatchingLabels{LedgerLabel: topology.CapabilityReadyValue}); err != nil {
		return err
	}
	for _, cm := range ledgers.Items {
		if names[cm.Name] {
			continue
		}
		var leases map[string]Lease
		if err := topology.Decode([]byte(cm.Data["leases"]), &leases); err != nil {
			return err
		}
		active := false
		for _, lease := range leases {
			active = active || lease.State != Released
		}
		if active {
			continue
		}
		cm.Finalizers = nil
		if err := p.Store.Client.Update(ctx, &cm); err != nil {
			return err
		}
		if err := p.Store.Client.Delete(ctx, &cm); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

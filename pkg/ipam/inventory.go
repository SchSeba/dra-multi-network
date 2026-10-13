// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package ipam

import (
	"context"
	"fmt"
	"net/netip"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

// Inventory reconciles backend state against the controller's verified claim
// snapshot. ResourceSlice publication remains a framework responsibility.
func (s Store) Inventory(ctx context.Context, fingerprints map[string]string) ([]PoolInfo, error) {
	pools, err := s.Pools(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]PoolInfo, 0, len(pools))
	for i, pool := range pools {
		if err := s.ValidatePrimaryRanges(ctx, pool); err != nil {
			return nil, err
		}
		prefix, _ := netip.ParsePrefix(pool.Config.Subnet)
		for j := 0; j < i; j++ {
			other, _ := netip.ParsePrefix(pools[j].Config.Subnet)
			if prefix.Overlaps(other) {
				return nil, fmt.Errorf("E_IPAM_POOL_OVERLAP")
			}
		}
		if !pool.Object.DeletionTimestamp.IsZero() {
			continue
		}
		if err := s.EnsureLedger(ctx, pool); err != nil {
			return nil, err
		}
		pool, err = s.Load(ctx, pool.Object.Name)
		if err != nil {
			return nil, err
		}
		_, leases, err := s.ledger(ctx, pool)
		if err != nil {
			return nil, err
		}
		info := poolInfo(pool)
		info.AllocationReady = true
		for _, lease := range leases {
			if lease.State != Released && fingerprints[lease.ClaimUID] != lease.Fingerprint {
				info.AllocationReady = false
			}
		}
		result = append(result, info)
	}
	if err := s.gcLedgers(ctx, pools); err != nil {
		return nil, err
	}
	return result, nil
}

func (s Store) gcLedgers(ctx context.Context, pools []Pool) error {
	names := map[string]bool{}
	for _, pool := range pools {
		names[LedgerName(pool)] = true
	}
	var ledgers corev1.ConfigMapList
	if err := s.Client.List(ctx, &ledgers, client.InNamespace(s.Namespace), client.MatchingLabels{LedgerLabel: topology.CapabilityReadyValue}); err != nil {
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
		if err := s.Client.Update(ctx, &cm); err != nil {
			return err
		}
		if err := s.Client.Delete(ctx, &cm); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

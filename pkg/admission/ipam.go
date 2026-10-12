// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package admission

import (
	"context"
	"encoding/json"
	"fmt"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/SchSeba/dra-multi-network/pkg/ipam"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func (s *Server) ipamConfigMap(ctx context.Context, req *admissionv1.AdmissionRequest) error {
	var cm, old corev1.ConfigMap
	if len(req.Object.Raw) > 0 {
		if err := json.Unmarshal(req.Object.Raw, &cm); err != nil {
			return err
		}
	}
	if len(req.OldObject.Raw) > 0 {
		if err := json.Unmarshal(req.OldObject.Raw, &old); err != nil {
			return err
		}
	}
	if cm.Namespace == "" {
		cm.Namespace = req.Namespace
	}
	if ipam.IsLedger(cm) || ipam.IsLedger(old) {
		if !s.controller(req.UserInfo.Username) && !s.node(req.UserInfo.Username) {
			return fmt.Errorf("E_IPAM_LEDGER_AUTHORITY: backend owned")
		}
		if req.Operation == admissionv1.Delete || len(old.Finalizers) > 0 && len(cm.Finalizers) == 0 {
			var leases map[string]ipam.Lease
			if err := topology.Decode([]byte(old.Data["leases"]), &leases); err != nil {
				return err
			}
			for _, lease := range leases {
				if lease.State != "Released" {
					return fmt.Errorf("E_IPAM_POOL_BUSY")
				}
			}
		}
		return nil
	}
	if cm.Labels[ipam.PoolLabel] != "true" && old.Labels[ipam.PoolLabel] != "true" {
		return nil
	}
	if req.Namespace != s.Namespace {
		return fmt.Errorf("E_IPAM_POOL_NAMESPACE")
	}
	if req.Operation == admissionv1.Delete {
		pool, err := ipam.DecodePool(old)
		if err != nil {
			return err
		}
		return (ipam.Store{Client: s.Client, Namespace: s.Namespace}).CanDelete(ctx, pool)
	}
	pool, err := ipam.DecodePool(cm)
	if err != nil {
		return err
	}
	if req.Operation == admissionv1.Update && (old.Data["config"] != cm.Data["config"] || old.Labels[ipam.PoolLabel] != cm.Labels[ipam.PoolLabel]) {
		return fmt.Errorf("E_IPAM_POOL_IMMUTABLE")
	}
	if old.Annotations[ipam.LedgerUID] != cm.Annotations[ipam.LedgerUID] && (!s.controller(req.UserInfo.Username) || old.Annotations[ipam.LedgerUID] != "") {
		return fmt.Errorf("E_IPAM_LEDGER_IDENTITY")
	}
	if err := (ipam.Store{Client: s.Client, Namespace: s.Namespace}).ValidatePrimaryRanges(ctx, pool); err != nil {
		return err
	}
	pools, err := (ipam.Store{Client: s.Client, Namespace: s.Namespace}).Pools(ctx)
	if err != nil {
		return err
	}
	for _, other := range pools {
		if other.Object.Name != cm.Name && ipam.Overlap(pool, other) {
			return fmt.Errorf("E_IPAM_POOL_OVERLAP")
		}
	}
	return nil
}

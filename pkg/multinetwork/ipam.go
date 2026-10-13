// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/ipam"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func (a *Agent) validateIPAMFabric(ctx context.Context, p Preparation) error {
	for _, root := range p.IPAMRoots {
		var currentNode corev1.Node
		if err := a.client.Get(ctx, client.ObjectKey{Name: a.node.Name}, &currentNode); err != nil {
			return err
		}
		pool, err := a.ipamProvider.GetPool(ctx, root.Lease.Pool)
		if err != nil {
			return err
		}
		for key, value := range pool.Config.NodeSelector {
			if currentNode.Labels[key] != value {
				return fmt.Errorf("E_IPAM_NODE_SCOPE")
			}
		}
		for _, physical := range p.Roots {
			parent := physical.Persona.Inventory.Parent
			if parent == "" {
				parent = physical.Persona.Inventory.PCI
			}
			if !slices.Contains(pool.Config.ParentPCIAddresses, parent) {
				return fmt.Errorf("E_IPAM_FABRIC: allocated root outside approved secondary PFs")
			}
			if !slices.Contains(physical.Persona.Policy.Spec.Exposure.EffectPermissions, topology.AddressConfigure) {
				return fmt.Errorf("E_EFFECT_DENIED: address configuration requires policy grant")
			}
		}
	}
	return nil
}
func (a *Agent) connectIPAM(ctx context.Context, p Preparation, at *Attachment, save func() error) error {
	for _, root := range p.IPAMRoots {
		lease, err := a.ipamProvider.Lookup(ctx, root.Lease)
		if err != nil {
			return err
		}
		lease.Holders = nil
		// Persist the holder identity before the backend can commit it.
		h := effects.Handle{Type: topology.AddressLeaseType, Scope: "ipam", ShareID: lease.ShareID, Address: lease.Address, Lease: &lease}
		if saved, ok := at.Execution.Roots[root.Step]; ok && topology.Digest(saved) != topology.Digest(h) {
			return fmt.Errorf("E_IPAM_ATTACHMENT_IDENTITY")
		}
		at.Execution.Roots[root.Step] = h
		if err := save(); err != nil {
			return err
		}
		if err := a.ipamProvider.AcquireHolder(ctx, ipam.HolderRequest{Lease: lease, Holder: at.Execution.ID}); err != nil {
			return err
		}
	}
	return nil
}
func (a *Agent) releaseIPAM(at Attachment) error {
	for _, h := range at.Execution.Roots {
		if h.Type == topology.AddressLeaseType && h.Lease != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := a.ipamProvider.ReleaseHolder(ctx, ipam.HolderRequest{Lease: *h.Lease, Holder: at.Execution.ID})
			cancel()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

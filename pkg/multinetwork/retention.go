// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	resourceapi "k8s.io/api/resource/v1"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

// Called under the existing mutation fence, just like preparation. A durable
// retirement acknowledgement closes the gap between the last report and GC:
// a delayed kubelet request cannot create new ownership for that revision.
func (a *Agent) reportReservations(ctx context.Context, revisions []api.NetworkTopologyRevision, report *topology.NodeCapabilities) error {
	prepared, err := a.store.List("prepare/")
	if err != nil {
		return err
	}
	saved := map[string]api.NetworkTopologyRevision{}
	retained := map[string]api.RevisionRef{}
	plugins := map[string]bool{}
	retain := func(ref api.RevisionRef) error {
		revision, exists := saved[ref.UID]
		if !exists || topology.RevisionIdentity(revision) != ref {
			return fmt.Errorf("E_RESERVATION_REVISION_IDENTITY")
		}
		if err := topology.VerifyRevision(revision.Spec); err != nil {
			return err
		}
		if old, exists := retained[ref.UID]; exists && old != ref {
			return fmt.Errorf("E_RESERVATION_REVISION_CONFLICT")
		}
		retained[ref.UID] = ref
		for _, plugin := range revision.Spec.Plugins {
			plugins[plugin.Name] = true
		}
		return nil
	}
	for _, raw := range prepared {
		var p Preparation
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		ref := topology.RevisionIdentity(p.Revision)
		if ref.UID == "" {
			return fmt.Errorf("E_RESERVATION_REVISION_IDENTITY")
		}
		if previous, exists := saved[ref.UID]; exists && topology.Digest(previous.Spec) != topology.Digest(p.Revision.Spec) {
			return fmt.Errorf("E_RESERVATION_REVISION_CONFLICT")
		}
		saved[ref.UID] = p.Revision
		if p.State != Released {
			if err := retain(ref); err != nil {
				return err
			}
		}
	}
	attachments, err := a.store.List("attach/")
	if err != nil {
		return err
	}
	for _, raw := range attachments {
		var at Attachment
		if err := json.Unmarshal(raw, &at); err != nil {
			return err
		}
		active := at.Phase != Removed
		for _, path := range []string{at.Pin, at.Stage} {
			if path == "" {
				continue
			}
			if _, err := os.Lstat(path); err == nil {
				active = true
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if active {
			if err := retain(at.Spec.RevisionRef); err != nil {
				return err
			}
		}
	}
	var claims resourceapi.ResourceClaimList
	if err := a.client.List(ctx, &claims); err != nil {
		return err
	}
	allocated := map[string]bool{}
	for _, claim := range claims.Items {
		if claim.Status.Allocation == nil {
			continue
		}
		for _, config := range claim.Status.Allocation.Devices.Config {
			if config.Opaque == nil || config.Opaque.Driver != api.Driver {
				continue
			}
			var binding api.TopologyRootConfiguration
			if err := topology.Decode(config.Opaque.Parameters.Raw, &binding); err != nil {
				return err
			}
			if binding.Kind != "TopologyRootConfiguration" || binding.RevisionRef.UID == "" {
				return fmt.Errorf("E_RESERVATION_ALLOCATION_IDENTITY")
			}
			allocated[binding.RevisionRef.UID] = true
		}
	}
	for _, revision := range revisions {
		// ponytail: acknowledge at most 128 retired revisions per heartbeat;
		// omitted revisions stay retained and enter subsequent batches after GC.
		if len(report.DrainedRevisions) >= 128 {
			break
		}
		ref := topology.RevisionIdentity(revision)
		if revision.Annotations[topology.RetirementAnnotation] != topology.RetirementToken(ref) {
			continue
		}
		if _, exists := retained[ref.UID]; exists || allocated[ref.UID] {
			continue
		}
		var previous api.RevisionRef
		found, err := a.store.Get("retire/"+ref.UID, &previous)
		if err != nil {
			return err
		}
		if found && previous != ref {
			return fmt.Errorf("E_RETIREMENT_IDENTITY")
		}
		if !found {
			if err := a.store.Put("retire/"+ref.UID, ref); err != nil {
				return err
			}
		}
		report.DrainedRevisions = append(report.DrainedRevisions, ref)
	}
	for _, ref := range retained {
		report.RetainedRevisions = append(report.RetainedRevisions, ref)
	}
	for name := range plugins {
		report.RetainedPlugins = append(report.RetainedPlugins, name)
	}
	slices.SortFunc(report.RetainedRevisions, func(a, b api.RevisionRef) int { return cmp.Compare(a.UID, b.UID) })
	slices.SortFunc(report.DrainedRevisions, func(a, b api.RevisionRef) int { return cmp.Compare(a.UID, b.UID) })
	slices.Sort(report.RetainedPlugins)
	report.ReservationsChecked = true
	return nil
}

func (a *Agent) checkRetirement(ref api.RevisionRef) error {
	var retired api.RevisionRef
	found, err := a.store.Get("retire/"+ref.UID, &retired)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("E_REVISION_DRAINED: retained retirement fence for %s", ref.Name)
	}
	return nil
}

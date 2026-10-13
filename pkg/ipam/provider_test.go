// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package ipam

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func wireCopy[T any](t *testing.T, value T) T {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type lostReserveResponse struct {
	Provider
	t       *testing.T
	dropped bool
	lease   Lease
}

func (p *lostReserveResponse) Reserve(ctx context.Context, request ReserveRequest) (Lease, error) {
	lease, err := p.Provider.Reserve(ctx, wireCopy(p.t, request))
	if err != nil {
		return Lease{}, err
	}
	if !p.dropped {
		p.dropped, p.lease = true, lease
		return Lease{}, context.DeadlineExceeded
	}
	return wireCopy(p.t, lease), nil
}

func TestDriverProviderLostReplyReplayAndHolderCleanup(t *testing.T) {
	store, node := testStore(t)
	ctx := context.Background()
	node.Name = "worker"
	claim, result := testClaim(1)
	claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results, resourceapi.DeviceRequestAllocationResult{Driver: "another-driver", Request: "other", Device: "other"})
	if err := store.Client.Create(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := store.Client.Create(ctx, &node); err != nil {
		t.Fatal(err)
	}
	provider := &lostReserveResponse{Provider: store, t: t}
	driver := Driver{Provider: provider, Client: store.Client, NodeName: node.Name}
	first, err := driver.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
	if err != nil || !errors.Is(first[claim.UID].Err, context.DeadlineExceeded) {
		t.Fatalf("lost committed reply was not returned: %v, %v", first, err)
	}
	retry, err := driver.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
	if err != nil || retry[claim.UID].Err != nil || len(retry[claim.UID].Devices) != 1 || retry[claim.UID].Devices[0].ShareID == nil || *retry[claim.UID].Devices[0].ShareID != *result.ShareID {
		t.Fatalf("prepare replay or foreign-driver isolation failed: %v, %v", retry, err)
	}
	pool, err := provider.GetPool(ctx, result.Pool)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.Lookup(ctx, wireCopy(t, Reservation(claim, result, wireCopy(t, pool))))
	if err != nil || lease.Address != provider.lease.Address || lease.Key != provider.lease.Key {
		t.Fatal("lost reply retry changed reservation", err)
	}
	holder := wireCopy(t, HolderRequest{Lease: lease, Holder: "sandbox-incarnation"})
	for range 2 {
		if err := provider.AcquireHolder(ctx, holder); err != nil {
			t.Fatal(err)
		}
	}
	claims := []kubeletplugin.NamespacedObject{{UID: claim.UID, Namespace: claim.Namespace, Name: claim.Name}}
	release, err := driver.UnprepareResourceClaims(ctx, claims)
	if err != nil || release[claim.UID] == nil {
		t.Fatal("unprepare released an active holder", err)
	}
	for range 2 {
		if err := provider.ReleaseHolder(ctx, holder); err != nil {
			t.Fatal("holder cleanup replay failed", err)
		}
	}
	release, err = driver.UnprepareResourceClaims(ctx, claims)
	if err != nil || release[claim.UID] != nil {
		t.Fatal("claim reservation was not released", err, release)
	}
	late, err := driver.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
	if err != nil || late[claim.UID].Err == nil {
		t.Fatal("delayed prepare reopened a released claim", err)
	}
}

func TestProviderInventoryFencesOrphansAndMissingLedger(t *testing.T) {
	store, node := testStore(t)
	var provider Provider = store
	ctx := context.Background()
	claim, result := testClaim(1)
	if _, err := reserveClaim(ctx, provider, claim, result, node); err != nil {
		t.Fatal(err)
	}
	for _, active := range []bool{true, false} {
		fingerprints := map[string]string{}
		if active {
			fingerprints[string(claim.UID)] = Fingerprint(claim)
		}
		pools, err := provider.Inventory(ctx, wireCopy(t, fingerprints))
		if err != nil || len(pools) != 1 {
			t.Fatal("inventory failed", err, pools)
		}
		pool := wireCopy(t, pools[0])
		if pool.Capacity != 5 || pool.AllocationReady != active || pool.ConfigDigest != topology.Digest(pool.Config) {
			t.Fatalf("inventory lost total capacity or orphan fence: %+v", pool)
		}
	}
	pool, err := store.Load(ctx, "blue")
	if err != nil {
		t.Fatal(err)
	}
	ledger := corev1.ConfigMap{}
	ledger.Name, ledger.Namespace = LedgerName(pool), store.Namespace
	if err := store.Client.Delete(ctx, &ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Inventory(ctx, map[string]string{}); err == nil {
		t.Fatal("inventory recreated missing durable state")
	}
}

func TestProviderRejectsMalformedReservationBeforeMutation(t *testing.T) {
	store, node := testStore(t)
	var provider Provider = store
	ctx := context.Background()
	claim, result := testClaim(1)
	valid, err := ReservationRequest(claim, result, node)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ReserveRequest){
		"claim":       func(r *ReserveRequest) { r.ClaimUID = "" },
		"fingerprint": func(r *ReserveRequest) { r.Fingerprint = "" },
		"request":     func(r *ReserveRequest) { r.Request = "" },
		"share":       func(r *ReserveRequest) { r.ShareID = "" },
		"driver":      func(r *ReserveRequest) { r.Driver = "other" },
		"pool":        func(r *ReserveRequest) { r.Pool = "other" },
		"device":      func(r *ReserveRequest) { r.Device = "other" },
		"subnet":      func(r *ReserveRequest) { r.Subnet = "not-a-subnet" },
		"node":        func(r *ReserveRequest) { r.NodeLabels = map[string]string{"fabric": "other"} },
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			change(&request)
			if _, err := provider.Reserve(ctx, request); err == nil {
				t.Fatal("malformed provider request allocated an address")
			}
		})
	}
	pool, err := store.Load(ctx, "blue")
	if err != nil {
		t.Fatal(err)
	}
	_, leases, err := store.ledger(ctx, pool)
	if err != nil || len(leases) != 0 {
		t.Fatal("rejected requests mutated the ledger", err, leases)
	}
}

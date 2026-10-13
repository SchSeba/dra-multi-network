// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package ipam

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func testStore(t *testing.T) (Store, corev1.Node) {
	t.Helper()
	config := PoolConfig{Network: "blue", Subnet: "192.0.2.0/29", Gateway: "192.0.2.1", NodeSelector: map[string]string{"fabric": "blue"}, ParentPCIAddresses: []string{"0000:29:00.0"}}
	raw, _ := json.Marshal(config)
	cm := corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "blue", Namespace: "driver", UID: "pool-uid", Labels: map[string]string{PoolLabel: "true"}, Annotations: map[string]string{LedgerUID: "ledger-uid"}}, Immutable: ptr.To(true), Data: map[string]string{"config": string(raw)}}
	pool, err := DecodePool(cm)
	if err != nil {
		t.Fatal(err)
	}
	ledger := corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: LedgerName(pool), Namespace: "driver", UID: "ledger-uid"}, Data: map[string]string{"leases": "{}"}}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := resourceapi.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&cm, &ledger).Build()
	return Store{Client: c, Namespace: "driver"}, corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"fabric": "blue"}}}
}
func testClaim(id int) (*resourceapi.ResourceClaim, resourceapi.DeviceRequestAllocationResult) {
	share := types.UID(fmt.Sprintf("00000000-0000-0000-0000-%012d", id))
	r := resourceapi.DeviceRequestAllocationResult{Request: "address", Driver: topology.IPAMDriver, Pool: "blue", Device: "subnet", ShareID: &share, ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{resourceapi.QualifiedName(topology.AddressCapacity): resource.MustParse("1")}}
	raw := []byte(`{"apiVersion":"ipam.schseba.github.io/v1alpha1","kind":"IPAMAllocationConfiguration","network":"blue","subnet":"192.0.2.0/29","addresses":1}`)
	c := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("claim-%d", id), Namespace: "app", UID: types.UID(fmt.Sprintf("claim-%d", id))}, Spec: resourceapi.ResourceClaimSpec{Devices: resourceapi.DeviceClaim{Requests: []resourceapi.DeviceRequest{{Name: "address", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "class", Count: 1, Capacity: &resourceapi.CapacityRequirements{Requests: map[resourceapi.QualifiedName]resource.Quantity{resourceapi.QualifiedName(topology.AddressCapacity): resource.MustParse("1")}}}}}}}, Status: resourceapi.ResourceClaimStatus{Allocation: &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{Results: []resourceapi.DeviceRequestAllocationResult{r}, Config: []resourceapi.DeviceAllocationConfiguration{{Source: resourceapi.AllocationConfigSourceClass, Requests: []string{"address"}, DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: topology.IPAMDriver, Parameters: runtime.RawExtension{Raw: raw}}}}}}}}}
	return c, r
}
func TestConcurrentReservationCapacityReplayAndCleanup(t *testing.T) {
	s, node := testStore(t)
	ctx := context.Background()
	leases := make([]Lease, 5)
	errs := make([]error, 5)
	var wg sync.WaitGroup
	for i := range leases {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, r := testClaim(i + 1)
			leases[i], errs[i] = reserveClaim(ctx, s, c, r, node)
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, l := range leases {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if seen[l.Address] {
			t.Fatal("duplicate address")
		}
		seen[l.Address] = true
		c, r := testClaim(i + 1)
		replayed, err := reserveClaim(ctx, s, c, r, node)
		if err != nil || replayed.Address != l.Address {
			t.Fatal("retry changed address", err)
		}
	}
	sixth, r := testClaim(6)
	if _, err := reserveClaim(ctx, s, sixth, r, node); err == nil {
		t.Fatal("exhausted pool allocated")
	}
	lease := leases[0]
	if err := s.AcquireHolder(ctx, HolderRequest{Lease: lease, Holder: "sandbox"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, lease.ClaimUID); err == nil {
		t.Fatal("unprepare freed an active address")
	}
	if err := s.AcquireHolder(ctx, HolderRequest{Lease: lease, Holder: "late-sandbox"}); err == nil {
		t.Fatal("release accepted a new holder")
	}
	if err := s.ReleaseHolder(ctx, HolderRequest{Lease: lease, Holder: "sandbox"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, lease.ClaimUID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseHolder(ctx, HolderRequest{Lease: lease, Holder: "sandbox"}); err != nil {
		t.Fatal("cleanup replay after lost response", err)
	}
	reused, err := reserveClaim(ctx, s, sixth, r, node)
	if err != nil || reused.Address != lease.Address {
		t.Fatal("capacity not released", err)
	}
	first, r := testClaim(1)
	if _, err := reserveClaim(ctx, s, first, r, node); err == nil {
		t.Fatal("released incarnation reopened")
	}
}
func TestLeaseAuthorityAndLedgerLoss(t *testing.T) {
	s, node := testStore(t)
	ctx := context.Background()
	c, r := testClaim(1)
	c.Status.Allocation.Devices.Config[0].Source = resourceapi.AllocationConfigSourceClaim
	if _, err := reserveClaim(ctx, s, c, r, node); err == nil {
		t.Fatal("claim-supplied IPAM contract accepted")
	}
	c.Status.Allocation.Devices.Config[0].Source = resourceapi.AllocationConfigSourceClass
	l, err := reserveClaim(ctx, s, c, r, node)
	if err != nil {
		t.Fatal(err)
	}
	foreign := l
	foreign.Fingerprint = "changed"
	if _, err := s.Lookup(ctx, foreign); err == nil {
		t.Fatal("foreign allocation obtained lease")
	}
	pool, err := s.Load(ctx, "blue")
	if err != nil {
		t.Fatal(err)
	}
	ledger := corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: LedgerName(pool), Namespace: "driver"}}
	if err := s.Client.Delete(ctx, &ledger); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLedger(ctx, pool); err == nil {
		t.Fatal("missing ledger recreated empty")
	}
	if _, err := reserveClaim(ctx, s, c, r, node); err == nil {
		t.Fatal("missing durable state allowed allocation")
	}
	var recreated corev1.ConfigMap
	if err := s.Client.Get(ctx, client.ObjectKeyFromObject(&ledger), &recreated); err == nil {
		t.Fatal("ledger was recreated")
	}
}

func TestPoolRejectsPrimaryRangesAndMalformedIdentities(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	p, err := s.Load(ctx, "blue")
	if err != nil {
		t.Fatal(err)
	}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Spec: corev1.NodeSpec{PodCIDRs: []string{"192.0.2.0/24"}}}
	if err := s.Client.Create(ctx, &node); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidatePrimaryRanges(ctx, p); err == nil {
		t.Fatal("secondary range overlaps primary Pod range")
	}
	p.Config.ParentPCIAddresses = []string{"not-a-pci-id!"}
	raw, _ := json.Marshal(p.Config)
	p.Object.Data["config"] = string(raw)
	if _, err := DecodePool(p.Object); err == nil {
		t.Fatal("invalid PF identity accepted")
	}
}

func TestNodeEntitlementRepeatsExplicitCapacityValidation(t *testing.T) {
	claim, result := testClaim(1)
	claim.Spec.Devices.Requests[0].Exactly.Capacity = nil
	if _, err := Entitlement(claim, result); err == nil {
		t.Fatal("node accepted omitted capacity despite consumed-capacity result")
	}
}

func reserveClaim(ctx context.Context, provider Provider, claim *resourceapi.ResourceClaim, result resourceapi.DeviceRequestAllocationResult, node corev1.Node) (Lease, error) {
	request, err := ReservationRequest(claim, result, node)
	if err != nil {
		return Lease{}, err
	}
	return provider.Reserve(ctx, request)
}

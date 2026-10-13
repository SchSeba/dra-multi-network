// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package ipam

import "context"

// Provider defines the future gRPC call points. Kubernetes entitlement checks
// stay with the framework; the provider owns durable reservations and holders.
type Provider interface {
	ListPools(context.Context) ([]PoolInfo, error)
	GetPool(context.Context, string) (PoolInfo, error)
	Inventory(context.Context, map[string]string) ([]PoolInfo, error)
	Reserve(context.Context, ReserveRequest) (Lease, error)
	Lookup(context.Context, Lease) (Lease, error)
	AcquireHolder(context.Context, HolderRequest) error
	ReleaseHolder(context.Context, HolderRequest) error
	Release(context.Context, string) error
}

type PoolInfo struct {
	Name            string     `json:"name"`
	UID             string     `json:"uid"`
	Config          PoolConfig `json:"config"`
	ConfigDigest    string     `json:"configDigest"`
	Capacity        int        `json:"capacity"`
	Deleting        bool       `json:"deleting"`
	AllocationReady bool       `json:"allocationReady"`
}

// ReserveRequest contains only the verified one-address allocation and node
// scope, never a ResourceClaim, Kubernetes client or mutable lease state.
type ReserveRequest struct {
	ClaimUID    string            `json:"claimUID"`
	Fingerprint string            `json:"fingerprint"`
	Request     string            `json:"request"`
	Driver      string            `json:"driver"`
	Pool        string            `json:"pool"`
	Device      string            `json:"device"`
	ShareID     string            `json:"shareID"`
	Network     string            `json:"network"`
	Subnet      string            `json:"subnet"`
	NodeLabels  map[string]string `json:"nodeLabels"`
}

type HolderRequest struct {
	Lease  Lease  `json:"lease"`
	Holder string `json:"holder"`
}

var _ Provider = Store{}

// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package ipam

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

type Driver struct {
	Store    Store
	NodeName string
	Cancel   func(error)
}

func (d *Driver) WatchHealthStatus(context.Context, chan<- kubeletplugin.DeviceHealthReport) error {
	return kubeletplugin.ErrHealthNotSupported
}
func (d *Driver) HandleError(ctx context.Context, err error, msg string) {
	klog.FromContext(ctx).Error(err, msg)
	d.Cancel(err)
}
func (d *Driver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	results := map[types.UID]kubeletplugin.PrepareResult{}
	for _, claim := range claims {
		response := kubeletplugin.PrepareResult{}
		var current resourceapi.ResourceClaim
		if err := d.Store.Client.Get(ctx, client.ObjectKeyFromObject(claim), &current); err != nil {
			response.Err = err
		} else if current.UID != claim.UID || current.Status.Allocation == nil || Fingerprint(&current) != Fingerprint(claim) {
			response.Err = fmt.Errorf("E_IPAM_CLAIM_IDENTITY")
		} else {
			var node corev1.Node
			response.Err = d.Store.Client.Get(ctx, client.ObjectKey{Name: d.NodeName}, &node)
			if response.Err == nil {
				for _, r := range claim.Status.Allocation.Devices.Results {
					if r.Driver != topology.IPAMDriver {
						continue
					}
					if _, err := d.Store.Reserve(ctx, claim, r, node); err != nil {
						response.Err = err
						break
					}
					response.Devices = append(response.Devices, kubeletplugin.Device{Requests: []string{r.Request}, PoolName: r.Pool, DeviceName: r.Device, ShareID: r.ShareID})
				}
			}
		}
		results[claim.UID] = response
	}
	return results, nil
}
func (d *Driver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	results := map[types.UID]error{}
	for _, claim := range claims {
		results[claim.UID] = d.Store.Release(ctx, claim.UID)
	}
	return results, nil
}

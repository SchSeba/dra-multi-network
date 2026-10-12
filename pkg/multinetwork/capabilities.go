// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/ipam"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func (a *Agent) capabilityReport(ctx context.Context) (topology.NodeCapabilities, error) {
	report := topology.NodeCapabilities{Version: topology.ContractVersion, NodeUID: string(a.node.UID), BootID: a.boot, PodName: a.podName, PodUID: a.podUID, Updated: time.Now().UTC(), Ready: a.synced, Executor: topology.ExecutorContractDigest()}
	var pod corev1.Pod
	if err := a.client.Get(ctx, client.ObjectKey{Namespace: a.config.Flags.Namespace, Name: a.podName}, &pod); err != nil {
		return report, err
	}
	if string(pod.UID) != a.podUID || pod.Spec.NodeName != a.node.Name {
		return report, fmt.Errorf("E_AGENT_POD_IDENTITY")
	}
	for _, c := range pod.Status.ContainerStatuses {
		if c.Name == "plugin" {
			report.ImageID, report.ContainerID = c.ImageID, c.ContainerID
		}
	}
	if report.ImageID == "" || report.ContainerID == "" {
		return report, apierrors.NewServiceUnavailable("agent container identity not reported yet")
	}
	plugins := map[string]api.PluginSpec{}
	digests := map[string]bool{}
	for _, builtin := range topology.Builtins() {
		plugins[builtin.Name] = builtin.Spec
		digests[topology.Digest(builtin.Spec)] = true
	}
	// Include supported saved contracts even after their current descriptors change.
	var revisions api.NetworkTopologyRevisionList
	if err := a.client.List(ctx, &revisions); err != nil {
		return report, err
	}
	if err := a.reportReservations(ctx, revisions.Items, &report); err != nil {
		return report, err
	}
	for _, revision := range revisions.Items {
		if err := topology.VerifyRevision(revision.Spec); err != nil {
			continue
		}
		for _, locked := range revision.Spec.Plugins {
			digests[locked.Digest] = true
		}
	}
	for digest := range digests {
		report.PluginDigests = append(report.PluginDigests, digest)
	}
	slices.Sort(report.PluginDigests)
	profiles := map[topology.HardwareProfile]bool{}
	for _, persona := range a.personas {
		if persona.Inventory.Protected || persona.Policy.Spec.Exposure == nil {
			continue
		}
		p, exists := plugins[persona.Policy.Spec.Exposure.Plugin]
		if persona.Plugin != nil {
			p, exists = *persona.Plugin, true
		}
		if !exists {
			continue
		}
		profile := topology.HardwareProfile{Contract: topology.ProfileContractDigest(p), Hardware: topology.Digest(struct {
			Kernel    string
			Inventory Inventory
			Exposure  api.Exposure
		}{a.node.Status.NodeInfo.KernelVersion, persona.Inventory, *persona.Policy.Spec.Exposure})}
		profiles[profile] = true
	}
	if a.ipamHelper != nil && a.ipamHelper.RegistrationStatus() != nil && a.ipamHelper.RegistrationStatus().PluginRegistered {
		var currentNode corev1.Node
		if err := a.client.Get(ctx, client.ObjectKey{Name: a.node.Name}, &currentNode); err != nil {
			return report, err
		}
		pools, err := (ipam.Store{Client: a.client, Namespace: a.config.Flags.Namespace}).Pools(ctx)
		if err != nil {
			return report, err
		}
		for _, pool := range pools {
			eligible := pool.Object.DeletionTimestamp.IsZero()
			for key, value := range pool.Config.NodeSelector {
				eligible = eligible && currentNode.Labels[key] == value
			}
			if eligible {
				profiles[topology.HardwareProfile{Contract: topology.ProfileContractDigest(topology.PluginRegistry(nil)[topology.IPAMPlugin].Spec), Hardware: topology.Digest(pool.Config)}] = true
			}
		}
	}
	for profile := range profiles {
		report.Profiles = append(report.Profiles, profile)
	}
	slices.SortFunc(report.Profiles, func(a, b topology.HardwareProfile) int {
		if order := cmp.Compare(a.Contract, b.Contract); order != 0 {
			return order
		}
		return cmp.Compare(a.Hardware, b.Hardware)
	})
	return report, nil
}

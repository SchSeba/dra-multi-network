// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
)

const CapabilityAnnotation = api.Driver + "/capabilities"
const CapabilityLabelPrefix = api.Driver + "/revision-"
const CapabilityReadyValue = "true"
const CapabilityTTL = 45 * time.Second
const MaxCapabilityBytes = 64 * 1024
const RetirementAnnotation = api.Driver + "/retirement"
const PluginRetentionFinalizer = api.Group + "/retain-contract"

func RevisionIdentity(revision api.NetworkTopologyRevision) api.RevisionRef {
	return api.RevisionRef{Name: revision.Name, UID: string(revision.UID), Digest: revision.Spec.Digest}
}

func RetirementToken(ref api.RevisionRef) string { return Digest(ref) }

type HardwareProfile struct {
	Contract string `json:"contract"`
	Hardware string `json:"hardware"`
}

// Bounded node-owned capabilities and reservation observations. Allocation and
// NRI still verify actual ownership; GC also checks current API references and
// requires an acknowledged durable retirement fence.
type NodeCapabilities struct {
	Version             string            `json:"version"`
	NodeUID             string            `json:"nodeUID"`
	BootID              string            `json:"bootID"`
	PodName             string            `json:"podName"`
	PodUID              string            `json:"podUID"`
	ImageID             string            `json:"imageID"`
	ContainerID         string            `json:"containerID"`
	Updated             time.Time         `json:"updated"`
	Ready               bool              `json:"ready"`
	Executor            string            `json:"executor"`
	PluginDigests       []string          `json:"pluginDigests"`
	Profiles            []HardwareProfile `json:"profiles"`
	ReservationsChecked bool              `json:"reservationsChecked,omitempty"`
	RetainedRevisions   []api.RevisionRef `json:"retainedRevisions,omitempty"`
	RetainedPlugins     []string          `json:"retainedPlugins,omitempty"`
	DrainedRevisions    []api.RevisionRef `json:"drainedRevisions,omitempty"`
}

func RevisionCapabilityLabel(digest string) string {
	return CapabilityLabelPrefix + Digest(digest)[:32]
}

// Bump the version when native execution or runtime gate semantics change.
func ExecutorContractDigest() string {
	return Digest([]string{"native-recorded-effects-with-roce-cm-and-scoped-runtime-access-v9", "nri-request-scoped-rdma-adjustment-and-recovery-access-v2", "durable-revision-retirement-fence-v1", "owned-link-mac-and-label-v1", "single-port-ib-kernel-resource-ownership-v1", "native-single-port-ib-ipoib-and-scoped-umad-v1", "devlink-local-vf-representor-and-controller-protection-v1", "host-representor-grants-and-recorded-linux-bridge-v1", "owned-ovsdb-representor-transactions-v1", "drained-eswitch-management-v1", "reference-owned-uplink-vlan-v1", IPv4Forwarding, AddressConfigure, UplinkConfigure, CompilerVersion, RepresentorMap, BridgeConfigure, OVSConfigure, Move, Configure, CreateBond, AttachMaster, CreateVLAN, CreateMacvlan, CreateIPvlan, DriverBind, DeviceAccess, VFConfigure, PCIReset, RDMANamespace})
}

func ProfileContractDigest(plugin api.PluginSpec) string {
	return Digest(struct {
		Version string
		Plugin  api.PluginSpec
	}{"ethernet-allocation-profile-v1", plugin})
}

func ParseCapabilities(raw string, now time.Time) (NodeCapabilities, error) {
	var report NodeCapabilities
	if len(raw) == 0 || len(raw) > MaxCapabilityBytes {
		return report, fmt.Errorf("E_CAPABILITY_SIZE")
	}
	if err := Decode([]byte(raw), &report); err != nil {
		return report, err
	}
	if report.Version != ContractVersion || report.NodeUID == "" || report.BootID == "" || report.PodUID == "" || report.PodName == "" || report.ImageID == "" || report.ContainerID == "" || !report.Ready || report.Executor != ExecutorContractDigest() {
		return report, fmt.Errorf("E_CAPABILITY_IDENTITY_CONTRACT")
	}
	if report.Updated.After(now.Add(5*time.Second)) || now.Sub(report.Updated) > CapabilityTTL {
		return report, fmt.Errorf("E_CAPABILITY_EXPIRED")
	}
	if len(report.PluginDigests) > 128 || len(report.Profiles) > 256 || len(report.RetainedRevisions) > 128 || len(report.DrainedRevisions) > 128 || len(report.RetainedPlugins) > 128 {
		return report, fmt.Errorf("E_CAPABILITY_COUNT")
	}
	validDigest := func(d string) bool {
		decoded, err := hex.DecodeString(d)
		return err == nil && len(decoded) == 32
	}
	for _, digest := range report.PluginDigests {
		if !validDigest(digest) {
			return report, fmt.Errorf("E_CAPABILITY_PLUGIN_DIGEST")
		}
	}
	for _, profile := range report.Profiles {
		if !validDigest(profile.Contract) || !validDigest(profile.Hardware) {
			return report, fmt.Errorf("E_CAPABILITY_PROFILE_DIGEST")
		}
	}
	for _, refs := range [][]api.RevisionRef{report.RetainedRevisions, report.DrainedRevisions} {
		seen := map[string]bool{}
		for _, ref := range refs {
			if len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || ref.UID == "" || len(ref.UID) > 128 || !validDigest(ref.Digest) || seen[ref.UID] {
				return report, fmt.Errorf("E_CAPABILITY_RESERVATION_IDENTITY")
			}
			seen[ref.UID] = true
		}
	}
	seen := map[string]bool{}
	for _, name := range report.RetainedPlugins {
		if len(validation.IsDNS1123Subdomain(name)) != 0 || seen[name] {
			return report, fmt.Errorf("E_CAPABILITY_RESERVED_PLUGIN")
		}
		seen[name] = true
	}
	for _, ref := range report.DrainedRevisions {
		if slices.ContainsFunc(report.RetainedRevisions, func(retained api.RevisionRef) bool { return retained.UID == ref.UID }) {
			return report, fmt.Errorf("E_CAPABILITY_DRAIN_CONFLICT")
		}
	}
	return report, nil
}

func SupportsRevision(report NodeCapabilities, revision api.RevisionSpec) bool {
	if err := VerifyRevision(revision); err != nil {
		return false
	}
	for _, locked := range revision.Plugins {
		if !slices.Contains(report.PluginDigests, locked.Digest) {
			return false
		}
		if locked.Spec.Kind == AllocationKind {
			contract := ProfileContractDigest(locked.Spec)
			if !slices.ContainsFunc(report.Profiles, func(p HardwareProfile) bool { return p.Contract == contract }) {
				return false
			}
		}
	}
	return true
}

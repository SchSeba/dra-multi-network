// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package ipam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

var pciAddress = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

const Prepared = "Prepared"
const Releasing = "Releasing"
const Released = "Released"

const PoolLabel = topology.IPAMDriver + "/pool"
const LedgerLabel = topology.IPAMDriver + "/ledger"
const LedgerUID = topology.IPAMDriver + "/ledger-uid"
const LedgerFinalizer = topology.IPAMDriver + "/retain-leases"

// Pool configuration is immutable. Only backend accounts may edit its ledger.
type PoolConfig struct {
	Network            string            `json:"network"`
	Subnet             string            `json:"subnet"`
	Gateway            string            `json:"gateway"`
	NodeSelector       map[string]string `json:"nodeSelector"`
	ParentPCIAddresses []string          `json:"parentPCIAddresses"`
}

type Lease struct {
	Key          string          `json:"key"`
	Pool         string          `json:"pool"`
	PoolUID      string          `json:"poolUID"`
	ConfigDigest string          `json:"configDigest"`
	ClaimUID     string          `json:"claimUID"`
	Fingerprint  string          `json:"fingerprint"`
	Request      string          `json:"request"`
	ShareID      string          `json:"shareID"`
	Address      string          `json:"address"`
	State        string          `json:"state"`
	Holders      map[string]bool `json:"holders,omitempty"`
}

type Store struct {
	Client    client.Client
	Namespace string
}

type Pool struct {
	Object    corev1.ConfigMap
	Config    PoolConfig
	Addresses []string
}

func DecodePool(cm corev1.ConfigMap) (Pool, error) {
	p := Pool{Object: cm}
	if cm.Labels[PoolLabel] != topology.CapabilityReadyValue || cm.Immutable == nil || !*cm.Immutable || cm.Data["config"] == "" {
		return p, fmt.Errorf("E_IPAM_POOL: immutable administrator pool required")
	}
	if err := topology.Decode([]byte(cm.Data["config"]), &p.Config); err != nil {
		return p, err
	}
	if err := topology.ValidateIPAMConfig(topology.Config{Network: p.Config.Network, Subnet: p.Config.Subnet}); err != nil {
		return p, err
	}
	prefix, _ := netip.ParsePrefix(p.Config.Subnet)
	gateway, err := netip.ParseAddr(p.Config.Gateway)
	if err != nil || !prefix.Contains(gateway) || len(p.Config.NodeSelector) == 0 || len(p.Config.ParentPCIAddresses) == 0 {
		return p, fmt.Errorf("E_IPAM_POOL: require gateway, approved nodes and secondary PFs")
	}
	if _, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: p.Config.NodeSelector}); err != nil {
		return p, err
	}
	for _, pci := range p.Config.ParentPCIAddresses {
		if !pciAddress.MatchString(pci) {
			return p, fmt.Errorf("E_IPAM_POOL: invalid PF PCI identity")
		}
	}
	// shortcut: pools are bounded to /20, use a larger transactional backend for larger ranges.
	for addr := prefix.Addr().Next(); prefix.Contains(addr.Next()); addr = addr.Next() {
		if addr != gateway {
			p.Addresses = append(p.Addresses, netip.PrefixFrom(addr, prefix.Bits()).String())
		}
	}
	if len(p.Addresses) == 0 || gateway == prefix.Addr() || !prefix.Contains(gateway.Next()) {
		return p, fmt.Errorf("E_IPAM_POOL: invalid usable range/gateway")
	}
	return p, nil
}

func (s Store) Pools(ctx context.Context) ([]Pool, error) {
	var list corev1.ConfigMapList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels{PoolLabel: topology.CapabilityReadyValue}); err != nil {
		return nil, err
	}
	var pools []Pool
	for _, cm := range list.Items {
		p, err := DecodePool(cm)
		if err != nil {
			return nil, err
		}
		pools = append(pools, p)
	}
	return pools, nil
}
func LedgerName(p Pool) string { return "ipam-ledger-" + topology.Digest(string(p.Object.UID))[:24] }
func (s Store) Load(ctx context.Context, name string) (Pool, error) {
	var cm corev1.ConfigMap
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &cm); err != nil {
		return Pool{}, err
	}
	return DecodePool(cm)
}
func (s Store) ForConfig(ctx context.Context, c topology.Config) (Pool, error) {
	pools, err := s.Pools(ctx)
	if err != nil {
		return Pool{}, err
	}
	var found *Pool
	for i := range pools {
		p := &pools[i]
		if p.Config.Network == c.Network && p.Config.Subnet == c.Subnet && p.Object.DeletionTimestamp.IsZero() {
			if found != nil {
				return Pool{}, fmt.Errorf("E_IPAM_POOL: ambiguous subnet")
			}
			found = p
		}
	}
	if found == nil {
		return Pool{}, fmt.Errorf("E_IPAM_POOL: approved subnet not found")
	}
	return *found, nil
}
func (s Store) ledger(ctx context.Context, p Pool) (corev1.ConfigMap, map[string]Lease, error) {
	var cm corev1.ConfigMap
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: LedgerName(p)}, &cm); err != nil {
		return cm, nil, err
	}
	leases := map[string]Lease{}
	if string(cm.UID) != p.Object.Annotations[LedgerUID] || cm.Data["leases"] == "" || !cm.DeletionTimestamp.IsZero() {
		return cm, nil, fmt.Errorf("E_IPAM_LEDGER_IDENTITY")
	}
	if err := topology.Decode([]byte(cm.Data["leases"]), &leases); err != nil {
		return cm, nil, err
	}
	if leases == nil {
		return cm, nil, fmt.Errorf("E_IPAM_LEDGER_CORRUPT")
	}
	used := map[string]bool{}
	for key, l := range leases {
		if key != l.Key || l.PoolUID != string(p.Object.UID) || l.ConfigDigest != topology.Digest(p.Config) || !slices.Contains(p.Addresses, l.Address) || !slices.Contains([]string{Prepared, Releasing, Released}, l.State) {
			return cm, nil, fmt.Errorf("E_IPAM_LEDGER_CORRUPT")
		}
		if l.State != Released {
			if used[l.Address] {
				return cm, nil, fmt.Errorf("E_IPAM_DUPLICATE_ADDRESS")
			}
			used[l.Address] = true
		}
	}
	return cm, leases, nil
}
func (s Store) update(ctx context.Context, p Pool, mutate func(map[string]Lease) error) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, leases, err := s.ledger(ctx, p)
		if err != nil {
			return err
		}
		if err := mutate(leases); err != nil {
			return err
		}
		raw, err := json.Marshal(leases)
		if err != nil {
			return err
		}
		// shortcut: retain replay tombstones in a bounded demo ledger; add safe GC before long-lived production use.
		if len(raw) > 240*1024 {
			return fmt.Errorf("E_IPAM_LEDGER_FULL")
		}
		cm.Data["leases"] = string(raw)
		return s.Client.Update(ctx, &cm)
	})
}
func Fingerprint(claim *resourceapi.ResourceClaim) string {
	return topology.Digest(struct {
		UID        types.UID
		Allocation *resourceapi.AllocationResult
	}{claim.UID, claim.Status.Allocation})
}
func Entitlement(claim *resourceapi.ResourceClaim, result resourceapi.DeviceRequestAllocationResult) (topology.Config, error) {
	var c topology.Config
	quantity := result.ConsumedCapacity[resourceapi.QualifiedName(topology.AddressCapacity)]
	if result.Driver != topology.IPAMDriver || result.ShareID == nil || *result.ShareID == "" || result.AdminAccess != nil && *result.AdminAccess || len(result.ConsumedCapacity) != 1 || quantity.Cmp(resource.MustParse("1")) != 0 {
		return c, fmt.Errorf("E_IPAM_ENTITLEMENT")
	}
	count := 0
	for _, request := range claim.Spec.Devices.Requests {
		if request.Name == result.Request && request.Exactly != nil && request.Exactly.Count == 1 && (request.Exactly.AllocationMode == "" || request.Exactly.AllocationMode == resourceapi.DeviceAllocationModeExactCount) && len(request.FirstAvailable) == 0 {
			if request.Exactly.Capacity == nil || len(request.Exactly.Capacity.Requests) != 1 {
				return c, fmt.Errorf("E_IPAM_REQUEST: explicitly request one address")
			}
			requested := request.Exactly.Capacity.Requests[resourceapi.QualifiedName(topology.AddressCapacity)]
			if requested.Cmp(resource.MustParse("1")) != 0 {
				return c, fmt.Errorf("E_IPAM_REQUEST: explicitly request one address")
			}
			count++
		}
	}
	if count != 1 {
		return c, fmt.Errorf("E_IPAM_REQUEST")
	}
	count = 0
	for _, config := range resourceclaim.ConfigForResult(claim.Status.Allocation.Devices.Config, result) {
		if config.Opaque == nil || config.Opaque.Driver != topology.IPAMDriver {
			continue
		}
		var parameters struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Network    string `json:"network"`
			Subnet     string `json:"subnet"`
			Addresses  int    `json:"addresses"`
		}
		if config.Source != resourceapi.AllocationConfigSourceClass {
			return c, fmt.Errorf("E_IPAM_CONFIG_ORIGIN")
		}
		if err := topology.Decode(config.Opaque.Parameters.Raw, &parameters); err != nil {
			return c, err
		}
		if parameters.APIVersion != topology.IPAMDriver+"/v1alpha1" || parameters.Kind != "IPAMAllocationConfiguration" || parameters.Addresses != 1 {
			return c, fmt.Errorf("E_IPAM_CONFIG")
		}
		c = topology.Config{Network: parameters.Network, Subnet: parameters.Subnet}
		count++
	}
	if count != 1 {
		return c, fmt.Errorf("E_IPAM_CONFIG_COVERAGE")
	}
	return c, topology.ValidateIPAMConfig(c)
}
func Reservation(claim *resourceapi.ResourceClaim, result resourceapi.DeviceRequestAllocationResult, p Pool) Lease {
	l := Lease{Pool: p.Object.Name, PoolUID: string(p.Object.UID), ConfigDigest: topology.Digest(p.Config), ClaimUID: string(claim.UID), Fingerprint: Fingerprint(claim), Request: result.Request, ShareID: string(*result.ShareID), State: Prepared}
	l.Key = topology.Digest([]string{l.ClaimUID, l.Request, result.Driver, result.Pool, result.Device, l.ShareID})
	return l
}
func (s Store) Reserve(ctx context.Context, claim *resourceapi.ResourceClaim, result resourceapi.DeviceRequestAllocationResult, node corev1.Node) (Lease, error) {
	c, err := Entitlement(claim, result)
	if err != nil {
		return Lease{}, err
	}
	p, err := s.ForConfig(ctx, c)
	if err != nil {
		return Lease{}, err
	}
	if result.Pool != p.Object.Name || result.Device != "subnet" {
		return Lease{}, fmt.Errorf("E_IPAM_ALLOCATION_SCOPE")
	}
	for key, value := range p.Config.NodeSelector {
		if node.Labels[key] != value {
			return Lease{}, fmt.Errorf("E_IPAM_NODE_SCOPE")
		}
	}
	l := Reservation(claim, result, p)
	err = s.update(ctx, p, func(leases map[string]Lease) error {
		if previous, ok := leases[l.Key]; ok {
			// shortcut: released claims require a new UID; add preparation-incarnation fencing before supporting claim reuse.
			if previous.Fingerprint != l.Fingerprint || previous.State != Prepared {
				return fmt.Errorf("E_IPAM_REPLAY_FENCED")
			}
			l = previous
			return nil
		}
		used := map[string]bool{}
		for _, previous := range leases {
			if previous.State != Released {
				used[previous.Address] = true
			}
		}
		for _, addr := range p.Addresses {
			if !used[addr] {
				l.Address = addr
				leases[l.Key] = l
				return nil
			}
		}
		return fmt.Errorf("E_IPAM_EXHAUSTED")
	})
	return l, err
}
func (s Store) Lookup(ctx context.Context, expected Lease) (Lease, error) {
	p, err := s.Load(ctx, expected.Pool)
	if err != nil {
		return Lease{}, err
	}
	_, leases, err := s.ledger(ctx, p)
	if err != nil {
		return Lease{}, err
	}
	l, ok := leases[expected.Key]
	if !ok || l.PoolUID != expected.PoolUID || l.ConfigDigest != expected.ConfigDigest || l.Fingerprint != expected.Fingerprint || l.ClaimUID != expected.ClaimUID || l.Request != expected.Request || l.ShareID != expected.ShareID || l.State != Prepared {
		return Lease{}, fmt.Errorf("E_IPAM_LEASE_IDENTITY")
	}
	return l, nil
}
func (s Store) Holder(ctx context.Context, expected Lease, holder string, add bool) error {
	if holder == "" {
		return fmt.Errorf("E_IPAM_HOLDER_IDENTITY")
	}
	p, err := s.Load(ctx, expected.Pool)
	if err != nil {
		return err
	}
	return s.update(ctx, p, func(leases map[string]Lease) error {
		l, ok := leases[expected.Key]
		if !ok || l.PoolUID != expected.PoolUID || l.Address != expected.Address || l.Fingerprint != expected.Fingerprint || add && l.State == Released {
			return fmt.Errorf("E_IPAM_LEASE_IDENTITY")
		}
		if add {
			if l.State != Prepared {
				return fmt.Errorf("E_IPAM_RELEASING")
			}
			if l.Holders == nil {
				l.Holders = map[string]bool{}
			}
			l.Holders[holder] = true
		} else {
			delete(l.Holders, holder)
		}
		leases[l.Key] = l
		return nil
	})
}
func (s Store) Release(ctx context.Context, uid types.UID) error {
	pools, err := s.Pools(ctx)
	if err != nil {
		return err
	}
	active := false
	for _, p := range pools {
		if err := s.update(ctx, p, func(leases map[string]Lease) error {
			for key, l := range leases {
				if l.ClaimUID != string(uid) || l.State == Released {
					continue
				}
				l.State = Releasing
				if len(l.Holders) == 0 {
					l.State = Released
				} else {
					active = true
				}
				leases[key] = l
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if active {
		return fmt.Errorf("E_IPAM_HOLDER_ACTIVE")
	}
	return nil
}
func (s Store) EnsureLedger(ctx context.Context, p Pool) error {
	if p.Object.Annotations[LedgerUID] != "" {
		_, _, err := s.ledger(ctx, p)
		return err
	}
	cm := corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: LedgerName(p), Namespace: s.Namespace, Labels: map[string]string{LedgerLabel: topology.CapabilityReadyValue}, Finalizers: []string{LedgerFinalizer}}, Data: map[string]string{"leases": "{}"}}
	if err := s.Client.Get(ctx, client.ObjectKeyFromObject(&cm), &cm); client.IgnoreNotFound(err) != nil {
		return err
	} else if err != nil {
		if err := s.Client.Create(ctx, &cm); err != nil {
			return err
		}
	}
	before := p.Object.DeepCopy()
	if p.Object.Annotations == nil {
		p.Object.Annotations = map[string]string{}
	}
	p.Object.Annotations[LedgerUID] = string(cm.UID)
	return s.Client.Patch(ctx, &p.Object, client.MergeFrom(before))
}
func IsLedger(cm corev1.ConfigMap) bool {
	return cm.Labels[LedgerLabel] == topology.CapabilityReadyValue || strings.HasPrefix(cm.Name, "ipam-ledger-")
}
func Overlap(a, b Pool) bool {
	left, _ := netip.ParsePrefix(a.Config.Subnet)
	right, _ := netip.ParsePrefix(b.Config.Subnet)
	return left.Overlaps(right)
}
func (s Store) CanDelete(ctx context.Context, p Pool) error {
	if p.Object.Annotations[LedgerUID] == "" {
		return nil
	}
	_, leases, err := s.ledger(ctx, p)
	if err != nil {
		return err
	}
	for _, l := range leases {
		if l.State != Released {
			return fmt.Errorf("E_IPAM_POOL_BUSY")
		}
	}
	var claims resourceapi.ResourceClaimList
	if err := s.Client.List(ctx, &claims); err != nil {
		return err
	}
	for _, claim := range claims.Items {
		if claim.Status.Allocation == nil {
			continue
		}
		for _, r := range claim.Status.Allocation.Devices.Results {
			if r.Driver == topology.IPAMDriver && r.Pool == p.Object.Name {
				return fmt.Errorf("E_IPAM_POOL_BUSY")
			}
		}
	}
	return nil
}

// Refuse primary Pod, Service and node management ranges before publishing capacity.
func (s Store) ValidatePrimaryRanges(ctx context.Context, p Pool) error {
	prefix, _ := netip.ParsePrefix(p.Config.Subnet)
	check := func(raw string) error {
		protected, err := netip.ParsePrefix(raw)
		if err != nil {
			return err
		}
		if prefix.Overlaps(protected) {
			return fmt.Errorf("E_IPAM_PRIMARY_RANGE: overlaps %s", raw)
		}
		return nil
	}
	var nodes corev1.NodeList
	if err := s.Client.List(ctx, &nodes); err != nil {
		return err
	}
	for _, node := range nodes.Items {
		cidrs := node.Spec.PodCIDRs
		if len(cidrs) == 0 && node.Spec.PodCIDR != "" {
			cidrs = []string{node.Spec.PodCIDR}
		}
		for _, cidr := range cidrs {
			if err := check(cidr); err != nil {
				return err
			}
		}
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP {
				ip, err := netip.ParseAddr(address.Address)
				if err != nil {
					return err
				}
				if prefix.Contains(ip) {
					return fmt.Errorf("E_IPAM_PRIMARY_RANGE: contains node management IP")
				}
			}
		}
	}
	var services networkingv1.ServiceCIDRList
	if err := s.Client.List(ctx, &services); err != nil {
		return err
	}
	for _, service := range services.Items {
		for _, cidr := range service.Spec.CIDRs {
			if err := check(cidr); err != nil {
				return err
			}
		}
	}
	return nil
}

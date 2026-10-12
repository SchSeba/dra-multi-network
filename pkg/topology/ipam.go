// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package topology

import (
	"fmt"
	"net/netip"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
)

const IPAMDriver = "ipam.schseba.github.io"
const IPAMPlugin = "ipam"
const AddressPlugin = "address-cni"
const AddressLeaseType = "AddressLease/v2"
const LeasePort = "lease"
const AddressConfigure = "linux/address-configure/v1"
const AddressCapacity = IPAMDriver + "/addresses"

func AllocationDriver(plugin api.PluginSpec) string {
	if plugin.AllocationDriver != "" {
		return plugin.AllocationDriver
	}
	return api.Driver
}

func ValidateIPAMConfig(c Config) error {
	prefix, err := netip.ParsePrefix(c.Subnet)
	if len(validation.IsDNS1123Label(c.Network)) != 0 || err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.Bits() < 20 || prefix.Bits() > 30 {
		return fmt.Errorf("E_IPAM_CONFIG: require a network name and canonical IPv4 /20 through /30 subnet")
	}
	return nil
}

//nolint:goconst // JSON Schema and canonical descriptor vocabulary remains literal.
func ipamPlugins() []api.NetworkPlugin {
	base := func(name, kind string) api.NetworkPlugin {
		return api.NetworkPlugin{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "NetworkPlugin"}, ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: api.PluginSpec{Implementation: api.Implementation{Type: BuiltinImplementation, Name: name, Version: ContractVersion}, Kind: kind, StateContract: api.StateContract{Version: ContractVersion, Cleanup: RecordedEffects}, AllowedEffects: []string{}}}
	}
	ipam := base(IPAMPlugin, AllocationKind)
	ipam.Spec.AllocationDriver = IPAMDriver
	ipam.Spec.ConfigSchema = schema(map[string]any{"network": map[string]any{"type": SchemaString}, "subnet": map[string]any{"type": SchemaString}}, []string{"network", "subnet"})
	ipam.Spec.Outputs = map[string]api.Port{LeasePort: {Type: AddressLeaseType, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}}
	address := base(AddressPlugin, OperationKind)
	address.Spec.ConfigSchema = schema(map[string]any{}, nil)
	address.Spec.Inputs = map[string]api.Port{InterfacePort: {Type: InterfaceType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Access: AccessOwned}, LeasePort: {Type: AddressLeaseType, MinItems: 1, MaxItems: 1, Access: "Lease"}}
	// A configured observation is not another interface-producing step.
	address.Spec.Outputs = map[string]api.Port{"configured": {Type: InterfaceType, LinkType: Ethernet, MinItems: 1, MaxItems: 1, Ownership: "Exclusive"}}
	address.Spec.AllowedEffects = []string{AddressConfigure}
	return []api.NetworkPlugin{ipam, address}
}

// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0
package multinetwork

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sigs.k8s.io/yaml"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/effects"
	"github.com/SchSeba/dra-multi-network/pkg/journal"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestSwitchdevPublicationRequiresMappingAndModeAndExplicitBridgeGrants(t *testing.T) {
	registry := topology.PluginRegistry(nil)
	data, err := os.ReadFile("../../test/integration/switchdev/fixtures/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var policy api.DeviceExposurePolicy
	if err := yaml.UnmarshalStrict(data, &policy); err != nil {
		t.Fatal(err)
	}
	plugin := registry[policy.Spec.Exposure.Plugin]
	if err := ValidatePolicy(policy, registry); err != nil {
		t.Fatal(err)
	}
	ovs := *policy.DeepCopy()
	ovs.Spec.Exposure.AllowedOVSBridges = []api.OVSBridgeGrant{{Name: "fabric0", UUID: "01234567-89ab-4cde-8012-3456789abcde", Identity: "site/fabric0", VLANs: []int{100, 200, 300}}}
	if err := ValidatePolicy(ovs, registry); err == nil {
		t.Fatal("OVS bridge without effect permission accepted")
	}
	ovs.Spec.Exposure.EffectPermissions = append(ovs.Spec.Exposure.EffectPermissions, topology.OVSConfigure)
	if err := ValidatePolicy(ovs, registry); err != nil {
		t.Fatal(err)
	}
	ovs.Spec.Exposure.AllowedOVSBridges[0].UUID = "bridge-name-is-not-identity"
	if err := ValidatePolicy(ovs, registry); err == nil {
		t.Fatal("invalid OVS UUID grant accepted")
	}
	uplink := *policy.DeepCopy()
	uplink.Spec.Exposure.AllowedHostBridges[0].Uplinks = []api.HostUplinkGrant{{Name: "uplink0", Alias: "site/uplink0", PCIAddress: "0000:2a:00.0", VLANs: []int{100, 200, 300}}}
	if err := ValidatePolicy(uplink, registry); err == nil {
		t.Fatal("uplink lacked separate permission")
	}
	uplink.Spec.Exposure.EffectPermissions = append(uplink.Spec.Exposure.EffectPermissions, topology.UplinkConfigure)
	if err := ValidatePolicy(uplink, registry); err != nil {
		t.Fatal(err)
	}
	uplink.Spec.Exposure.AllowedHostBridges[0].Uplinks[0].VLANs = []int{400}
	if err := ValidatePolicy(uplink, registry); err == nil {
		t.Fatal("uplink exceeded bridge VLAN grant")
	}
	device := Inventory{PCI: "0000:29:10.0", Parent: "0000:29:00.0", Kind: VFKind, IfName: "eth7", LinkType: topology.Ethernet, Driver: "igbvf"}
	for _, tc := range []struct {
		mapping   bool
		mode      string
		protected bool
		want      int
	}{{false, "switchdev", false, 0}, {true, "", false, 0}, {true, "legacy", false, 0}, {true, "switchdev", true, 0}, {true, "switchdev", false, 1}} {
		candidate := device
		candidate.EswitchMode = tc.mode
		candidate.Protected = tc.protected
		if tc.mapping {
			candidate.Representor = &effects.RepresentorIdentity{VFPCI: device.PCI, PFPCI: device.Parent, Name: "representor", IfIndex: 42}
		}
		personas, err := Personas(map[string]Inventory{device.PCI: candidate}, []api.DeviceExposurePolicy{policy}, corev1.Node{}, registry)
		if err != nil || len(personas) != tc.want {
			t.Fatalf("mapping=%t mode=%s protected=%t: %v %v", tc.mapping, tc.mode, tc.protected, personas, err)
		}
		for _, persona := range personas {
			if got := ptr.Deref(persona.Device.Attributes[resourceapi.QualifiedName(api.Driver+"/persona")].StringValue, ""); got != policy.Spec.Exposure.Persona {
				t.Fatalf("published persona = %q, want %q", got, policy.Spec.Exposure.Persona)
			}
		}
	}
	bad := *policy.DeepCopy()
	bad.Spec.Exposure.Plugin = "sriov-cni"
	if err := ValidatePolicy(bad, registry); err == nil {
		t.Fatal("ordinary VF persona gained host-bridge authority")
	}
	bad = *policy.DeepCopy()
	bad.Spec.Exposure.AllowedHostBridges[0].VLANs = []int{100, 100}
	if err := ValidatePolicy(bad, registry); err == nil {
		t.Fatal("duplicate VLAN grant accepted")
	}
	bad = *policy.DeepCopy()
	bad.Spec.Exposure.EffectPermissions = plugin.Spec.AllowedEffects
	if err := ValidatePolicy(bad, registry); err == nil {
		t.Fatal("bridge mutation lacked policy permission")
	}
}

func TestManagementBridgeProtectsRepresentorControllerWithoutPCISymlink(t *testing.T) {
	const pci = "0000:29:00.0"
	root := t.TempDir()
	if err := os.MkdirAll(root+"/bus/pci/devices/"+pci, 0700); err != nil {
		t.Fatal(err)
	}
	// The route belongs to the bridge, and only its slave is a devlink port.
	// No /class/net/anything/device symlink is present for the representor.
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "management", Index: 100}}
	rep := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "arbitrary", Index: 200, MasterIndex: 100}}
	port := &netlink.DevlinkPort{BusName: "pci", DeviceName: pci, PortFlavour: nl.DEVLINK_PORT_FLAVOUR_PCI_VF, NetdeviceName: "arbitrary", NetdevIfIndex: 200}
	protected, err := protectedDevlinkPCI(root, []*netlink.DevlinkPort{port}, []netlink.Link{bridge, rep}, []netlink.Route{{LinkIndex: 100}})
	if err != nil || !protected[pci] || len(protected) != 1 {
		t.Fatalf("management bridge did not protect the representor's controller: %v %v", protected, err)
	}
	protected, err = protectedDevlinkPCI(root, []*netlink.DevlinkPort{port}, []netlink.Link{bridge, rep}, nil)
	if err != nil || len(protected) != 0 {
		t.Fatalf("unrelated bridge acquired management authority: %v %v", protected, err)
	}
}

func TestRDMAManagementScopeProtectsAllObservedOwners(t *testing.T) {
	// The two HCAs connect through the second function. A third unrelated
	// device with the same system GUID must not acquire an invented dependency.
	inventory := map[string]Inventory{
		"pf0":   {PCI: "pf0", Parent: "pf0", Protected: true},
		"pf1":   {PCI: "pf1", Parent: "pf1"},
		"vf1":   {PCI: "vf1", Parent: "pf1", Kind: VFKind},
		"pf2":   {PCI: "pf2", Parent: "pf2"},
		"other": {PCI: "other", Parent: "other"},
	}
	devices := []effects.RDMADevice{
		{Name: "hca1", PCI: "pf1", RelatedPCI: []string{"pf1", "pf2"}, SystemImageGUID: "same"},
		{Name: "hca0", PCI: "pf0", RelatedPCI: []string{"pf0", "pf1"}, SystemImageGUID: "same"},
		{Name: "other_hca", PCI: "other", RelatedPCI: []string{"other"}, SystemImageGUID: "same"},
	}
	joinRDMAInventory(inventory, devices)
	for _, pci := range []string{"pf0", "pf1", "pf2", "vf1"} {
		if !inventory[pci].Protected {
			t.Fatalf("management dependency %s remained allocatable", pci)
		}
	}
	if inventory["other"].Protected || len(inventory["pf1"].RDMA) != 1 || inventory["pf1"].RDMA[0].Name != "hca1" || len(inventory["vf1"].RDMA) != 0 {
		t.Fatalf("invented GUID/VF device ownership: %+v", inventory)
	}
	// Startup refreshes current route protection while retaining the saved HCA
	// mappings, including devices which have moved out of the host namespace.
	var saved []effects.RDMADevice
	for pci, d := range inventory {
		d.Protected = pci == "pf0"
		inventory[pci] = d
		saved = append(saved, d.RDMA...)
	}
	protectRDMAInventory(inventory, saved)
	if !inventory["pf2"].Protected || !inventory["vf1"].Protected || inventory["other"].Protected {
		t.Fatalf("startup dropped management HCA protection: %+v", inventory)
	}
}

func TestRDMAFunctionsAreNotPublishedThroughEthernetHandoffProfiles(t *testing.T) {
	const owner, peer = "0000:29:10.0", "0000:29:10.2"
	registry := topology.PluginRegistry(nil)
	for _, plugin := range topology.Builtins() {
		if !strings.HasPrefix(plugin.Name, topology.HostPlugin) && !strings.HasPrefix(plugin.Name, topology.SRIOVPlugin) {
			continue
		}
		if plugin.Spec.Outputs[topology.RDMAPort].Type == topology.RDMAType {
			continue
		}
		t.Run(plugin.Name, func(t *testing.T) {
			kind, mode := "PF", topology.KernelMode
			if strings.HasPrefix(plugin.Name, topology.SRIOVPlugin) {
				kind = VFKind
			}
			if strings.Contains(plugin.Name, "vfio") {
				mode = topology.VFIOMode
			}
			exposure := &api.Exposure{Persona: "test", Plugin: plugin.Name, Mode: mode, AllowedDrivers: []string{"igbvf", topology.VFIODriver}, EffectPermissions: plugin.Spec.AllowedEffects}
			if strings.HasSuffix(plugin.Name, "-configured") {
				exposure.AllowedVFSettings = []api.VFSettingField{"mac"}
			}
			if strings.HasSuffix(plugin.Name, "-reset") {
				exposure.AllowedResetMethods = []api.ResetMethod{api.ResetMethod(topology.FLR)}
			}
			policy := api.DeviceExposurePolicy{ObjectMeta: metav1.ObjectMeta{Name: "exclusive"}, Spec: api.PolicySpec{Selector: api.DeviceSelector{Kind: kind}, Action: "Expose", Exposure: exposure}}
			inventory := map[string]Inventory{
				owner: {PCI: owner, Parent: owner, Kind: kind, Driver: "igbvf", LinkType: topology.Ethernet, RDMA: []effects.RDMADevice{{Name: "hca0", PCI: owner, RelatedPCI: []string{owner, peer}}}},
				peer:  {PCI: peer, Parent: peer, Kind: kind, Driver: "igbvf", LinkType: topology.Ethernet},
			}
			personas, err := Personas(inventory, []api.DeviceExposurePolicy{policy}, corev1.Node{}, registry)
			if err != nil || len(personas) != 0 {
				t.Fatalf("exclusive Ethernet profile offered an HCA owner or related function: %+v, %v", personas, err)
			}
		})
	}
}

func TestRDMAFencingKeepsSharedChildrenAndRetainsLiveAccounting(t *testing.T) {
	const owner, peer, unrelated = "0000:29:10.0", "0000:29:10.2", "0000:29:10.4"
	inventory := map[string]Inventory{}
	for _, pci := range []string{owner, peer, unrelated} {
		inventory[pci] = Inventory{PCI: pci, Parent: "0000:29:00.0", Kind: VFKind, LinkType: topology.Ethernet}
	}
	registry := topology.PluginRegistry(nil)
	policy := api.DeviceExposurePolicy{ObjectMeta: metav1.ObjectMeta{Name: "kernel"}, Spec: api.PolicySpec{Selector: api.DeviceSelector{Kind: VFKind}, Action: "Expose", Exposure: &api.Exposure{Persona: "kernel", Plugin: topology.SRIOVPlugin, Mode: topology.KernelMode, EffectPermissions: []string{topology.Move, topology.Configure}}}}
	before, err := Personas(inventory, []api.DeviceExposurePolicy{policy}, corev1.Node{}, registry)
	if err != nil || len(before) != 3 {
		t.Fatalf("initial Ethernet publication: %+v, %v", before, err)
	}
	d := inventory[owner]
	d.RDMA = []effects.RDMADevice{{Name: "hca0", PCI: owner, RelatedPCI: []string{owner, peer}}}
	inventory[owner] = d
	desired, err := Personas(inventory, []api.DeviceExposurePolicy{policy}, corev1.Node{}, registry)
	if err != nil || len(desired) != 1 {
		t.Fatalf("RDMA fencing removed unrelated Ethernet or kept an affected function: %+v, %v", desired, err)
	}
	retained := retainedPublication(desired, before, true, nil)
	if len(retained) != 3 || publicationContract(retained) != publicationContract(before) {
		t.Fatal("new HCA dependency dropped live identities or compatibility accounting")
	}
	for _, p := range retained {
		if ptr.Deref(p.Device.Attributes[resourceapi.QualifiedName(api.Driver+"/allocationReady")].BoolValue, true) {
			t.Fatal("new allocation admitted while HCA scope awaits drain")
		}
	}
	if len(retainedPublication(desired, retained, false, nil)) != 1 {
		t.Fatal("drained HCA declarations remained allocatable")
	}
	for _, plugin := range []string{topology.MacvlanPlugin, topology.IPvlanPlugin} {
		p := *policy.DeepCopy()
		p.Spec.Exposure.Plugin, p.Spec.Exposure.Persona = plugin, "shared"
		p.Spec.Exposure.Mode = topology.MacvlanBridgeMode
		if plugin == topology.IPvlanPlugin {
			p.Spec.Exposure.Mode = topology.IPvlanL2Mode
		}
		p.Spec.Exposure.AllowMultipleAllocations = true
		p.Spec.Exposure.Capacity = map[string]api.Capacity{"children": {Value: "2", Default: "1", ValidValues: []string{"1"}}}
		p.Spec.Exposure.EffectPermissions = registry[plugin].Spec.AllowedEffects
		children, err := Personas(inventory, []api.DeviceExposurePolicy{p}, corev1.Node{}, registry)
		if err != nil || len(children) != 3 {
			t.Fatalf("host-retained %s incorrectly grants/moves an HCA: %+v, %v", plugin, children, err)
		}
	}
}

func TestProtectedLinksNamespacePeerAndZeroIndices(t *testing.T) {
	links := []netlink.Link{
		&netlink.Device{LinkAttrs: netlink.LinkAttrs{Name: "secondary", Index: 2}},
		&netlink.Device{LinkAttrs: netlink.LinkAttrs{Name: "primary", Index: 3}},
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "cni0", Index: 15}},
		&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "pod-peer", Index: 16, ParentIndex: 2, MasterIndex: 15}},
		&netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "primary-vlan", Index: 17, ParentIndex: 3}},
	}
	protected := protectedLinks(links, []netlink.Route{{LinkIndex: 17}, {LinkIndex: 0, Dst: &net.IPNet{IP: net.IPv6zero}}})
	if protected[0] || protected[2] || !protected[3] || !protected[15] || !protected[16] || !protected[17] {
		t.Fatalf("incorrect dependency closure: %v", protected)
	}
}

func TestOrphanReservationBlocksPublicationUntilCleanup(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := resourceapi.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	claim := resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "network", Namespace: "test", UID: "claim"}, Status: resourceapi.ResourceClaimStatus{Allocation: &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{Results: []resourceapi.DeviceRequestAllocationResult{{Driver: api.Driver, Pool: "node", Device: "vf"}}}}}}
	a := Agent{store: store, node: corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}, client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(&claim).Build()}
	persona := func(pci, parent, kind string, shared bool) Persona {
		return Persona{Inventory: Inventory{PCI: pci, Parent: parent, Kind: kind}, Device: resourceapi.Device{Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}}, Policy: api.DeviceExposurePolicy{Spec: api.PolicySpec{Exposure: &api.Exposure{Plugin: topology.SRIOVPlugin, AllowMultipleAllocations: shared}}}}
	}
	vf := persona("vf0", "pf", VFKind, false)
	p := Preparation{Claim: claim, Fingerprint: allocationFingerprint(&claim), State: Prepared, Roots: []Root{{Persona: vf}}}
	if err := store.Put("prepare/claim", p); err != nil {
		t.Fatal(err)
	}
	active, orphans, err := a.activeClaims(context.Background())
	if err != nil || len(active) != 1 || len(orphans) != 0 {
		t.Fatalf("allocated reservation incorrectly orphaned: %v %v %v", active, orphans, err)
	}
	if err := a.client.Delete(context.Background(), &claim); err != nil {
		t.Fatal(err)
	}
	active, orphans, err = a.activeClaims(context.Background())
	if err != nil || len(active) != 1 || len(orphans) != 1 {
		t.Fatalf("API deletion released the journal reservation: %v %v %v", active, orphans, err)
	}
	personas := map[string]Persona{"vf0": vf, "vf1": persona("vf1", "pf", VFKind, false), "pf": persona("pf", "pf", "PF", false), "shared-vf0": persona("vf0", "pf", VFKind, true)}
	contract := publicationContract(personas)
	blocked := retainedPublication(personas, personas, true, orphans)
	for name, want := range map[string]bool{"vf0": false, "shared-vf0": false, "pf": false, "vf1": true} {
		ready := blocked[name].Device.Attributes[resourceapi.QualifiedName(api.Driver+"/allocationReady")].BoolValue
		if ready == nil || *ready != want {
			t.Fatalf("persona %s readiness = %v, want %v", name, ready, want)
		}
	}
	if publicationContract(blocked) != contract {
		t.Fatal("readiness incorrectly changed the accounting contract")
	}
	p.State = Released
	if err := store.Put("prepare/claim", p); err != nil {
		t.Fatal(err)
	}
	active, orphans, err = a.activeClaims(context.Background())
	if err != nil || len(active) != 0 || len(orphans) != 0 {
		t.Fatalf("cleanup did not release the journal reservation: %v %v %v", active, orphans, err)
	}
	// Another active allocation must not latch the old readiness hint forever.
	ready := retainedPublication(personas, blocked, true, nil)
	for name, p := range ready {
		if !ptr.Deref(p.Device.Attributes[resourceapi.QualifiedName(api.Driver+"/allocationReady")].BoolValue, false) {
			t.Fatalf("cleanup left persona %s drained", name)
		}
	}
}

func TestExclusiveVFProfilesCannotAllocateTheSameFunction(t *testing.T) {
	policies := []api.DeviceExposurePolicy{
		{ObjectMeta: metav1.ObjectMeta{Name: "kernel"}, Spec: api.PolicySpec{Selector: api.DeviceSelector{Kind: VFKind}, Action: "Expose", Exposure: &api.Exposure{Persona: "kernel", Plugin: topology.SRIOVPlugin, Mode: topology.KernelMode, EffectPermissions: []string{topology.Move, topology.Configure}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "configured"}, Spec: api.PolicySpec{Selector: api.DeviceSelector{Kind: VFKind}, Action: "Expose", Exposure: &api.Exposure{Persona: "configured", Plugin: topology.SRIOVPlugin + "-configured", Mode: topology.KernelMode, AllowedVFSettings: []api.VFSettingField{"mac"}, EffectPermissions: []string{topology.VFConfigure, topology.Move, topology.Configure}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "rebound"}, Spec: api.PolicySpec{Selector: api.DeviceSelector{Kind: VFKind}, Action: "Expose", Exposure: &api.Exposure{Persona: "rebound", Plugin: topology.SRIOVPlugin + "-rebind", Mode: topology.KernelMode, AllowedDrivers: []string{"igbvf"}, EffectPermissions: []string{topology.DriverBind, topology.Move, topology.Configure}}}},
	}
	personas, err := Personas(map[string]Inventory{"vf": {PCI: "0000:29:11.0", Parent: "0000:29:00.0", Kind: VFKind, Driver: "igbvf", LinkType: topology.Ethernet}}, policies, corev1.Node{}, topology.PluginRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(personas) != 3 {
		t.Fatalf("missing endpoint profiles: %+v", personas)
	}
	var domain string
	groups := map[string]bool{}
	for _, p := range personas {
		counter := p.Device.ConsumesCounters[0]
		if domain != "" && (domain != counter.CounterSet || groups[counter.CompatibilityGroups[0]]) {
			t.Fatal("different handoff profiles can allocate the same VF")
		}
		domain = counter.CounterSet
		groups[counter.CompatibilityGroups[0]] = true
	}
}

func TestProgrammableAllocationPublication(t *testing.T) {
	data, err := os.ReadFile("../../examples/root-effect-program/plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var plugin api.NetworkPlugin
	if err := yaml.UnmarshalStrict(data, &plugin); err != nil {
		t.Fatal(err)
	}
	registry := topology.PluginRegistry([]api.NetworkPlugin{plugin})
	policy := api.DeviceExposurePolicy{ObjectMeta: metav1.ObjectMeta{Name: "programmable"}, Spec: api.PolicySpec{Selector: api.DeviceSelector{Kind: VFKind}, Action: "Expose", Exposure: &api.Exposure{Persona: "programmed", Plugin: plugin.Name, Mode: topology.KernelMode, AllowedDrivers: []string{"igbvf"}, EffectPermissions: plugin.Spec.AllowedEffects}}}
	for _, tc := range []struct {
		name   string
		mutate func(*api.DeviceExposurePolicy)
		reason string
	}{
		{"missing driver grant", func(p *api.DeviceExposurePolicy) { p.Spec.Exposure.AllowedDrivers = nil }, "E_DRIVER_GRANT"},
		{"shared exclusive profile", func(p *api.DeviceExposurePolicy) { p.Spec.Exposure.AllowMultipleAllocations = true }, "E_POLICY_SHARING"},
		{"missing effect grant", func(p *api.DeviceExposurePolicy) {
			p.Spec.Exposure.EffectPermissions = []string{topology.Move, topology.Configure}
		}, "E_EFFECT_DENIED"},
		{"wrong mode", func(p *api.DeviceExposurePolicy) { p.Spec.Exposure.Mode = topology.VFIOMode }, "E_POLICY_PERSONA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policy.DeepCopy()
			tc.mutate(p)
			if err := ValidatePolicy(*p, registry); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s, got %v", tc.reason, err)
			}
		})
	}
	alias := *plugin.DeepCopy()
	alias.Name = "programmable-alias"
	registry[alias.Name] = alias
	second := *policy.DeepCopy()
	second.Name = "alias"
	second.Spec.Exposure.Plugin = alias.Name
	second.Spec.Exposure.Persona = "alias"
	inventory := map[string]Inventory{"vf": {PCI: "0000:29:11.0", Parent: "0000:29:00.0", Kind: VFKind, Driver: "igbvf", LinkType: topology.Ethernet}}
	personas, err := Personas(inventory, []api.DeviceExposurePolicy{policy, second}, corev1.Node{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(personas) != 2 {
		t.Fatalf("missing programmable personas: %+v", personas)
	}
	groups := map[string]bool{}
	var domain string
	for _, p := range personas {
		digest := p.Device.Attributes[resourceapi.QualifiedName(api.Driver+"/pluginContractDigest")].StringValue
		if p.Plugin == nil || digest == nil || *digest != topology.Digest(plugin.Spec) || topology.Digest(*p.Plugin) != *digest {
			t.Fatalf("descriptor/digest not retained: %+v", p)
		}
		counter := p.Device.ConsumesCounters[0]
		if groups[counter.CompatibilityGroups[0]] || domain != "" && domain != counter.CounterSet {
			t.Fatal("plugin aliases bypass physical VF exclusion")
		}
		domain = counter.CounterSet
		groups[counter.CompatibilityGroups[0]] = true
	}
	changed := map[string]Persona{}
	for name, p := range personas {
		q := p
		q.Plugin = p.Plugin.DeepCopy()
		q.Plugin.Implementation.Version = "different"
		changed[name] = q
	}
	retained := retainedPublication(changed, personas, true, nil)
	for name, p := range retained {
		if topology.Digest(*p.Plugin) != topology.Digest(*personas[name].Plugin) {
			t.Fatal("active publication changed its cleanup descriptor")
		}
	}
}

func TestResetPolicyRequiresExplicitScopedMethodGrant(t *testing.T) {
	p := api.DeviceExposurePolicy{Spec: api.PolicySpec{Action: "Expose", Selector: api.DeviceSelector{Kind: VFKind}, Exposure: &api.Exposure{Persona: "reset", Plugin: "sriov-cni-reset", Mode: topology.KernelMode, AllowedDrivers: []string{"igbvf"}, AllowedResetMethods: []api.ResetMethod{api.ResetMethod(topology.FLR)}, EffectPermissions: []string{topology.PCIReset, topology.DriverBind, topology.Move, topology.Configure}}}}
	if err := ValidatePolicy(p, topology.PluginRegistry(nil)); err != nil {
		t.Fatal(err)
	}
	for _, methods := range [][]api.ResetMethod{nil, {"bus"}, {"flr", "flr"}} {
		other := p.DeepCopy()
		other.Spec.Exposure.AllowedResetMethods = methods
		if err := ValidatePolicy(*other, topology.PluginRegistry(nil)); err == nil || !strings.Contains(err.Error(), "E_RESET_GRANT") {
			t.Fatalf("invalid reset grant accepted: %v", err)
		}
	}
	p.Spec.Exposure.Plugin = "sriov-cni-rebind"
	p.Spec.Exposure.EffectPermissions = []string{topology.DriverBind, topology.Move, topology.Configure}
	if err := ValidatePolicy(p, topology.PluginRegistry(nil)); err == nil || !strings.Contains(err.Error(), "E_RESET_GRANT") {
		t.Fatalf("reset authority granted to a non-reset profile: %v", err)
	}
}

func TestRoCEPublicationRequiresExplicitWholeFunctionCertificate(t *testing.T) {
	const pci = "0000:29:10.0"
	registry := topology.PluginRegistry(nil)
	p := api.DeviceExposurePolicy{ObjectMeta: metav1.ObjectMeta{Name: "roce"}, Spec: api.PolicySpec{Action: "Expose", Selector: api.DeviceSelector{Kind: VFKind}, Exposure: &api.Exposure{Persona: "roce", Plugin: "sriov-cni-roce", Mode: topology.RDMAMode, AllowedDrivers: []string{"mlx5_core"}, AllowedRDMAProfiles: []api.RDMAProfile{api.RDMAProfile(topology.RoCESinglePort)}, EffectPermissions: []string{topology.RDMANamespace, topology.DeviceAccess}}}}
	d := Inventory{PCI: pci, Parent: "0000:29:00.0", Kind: VFKind, IfName: "eth7", Driver: "mlx5_core", LinkType: topology.Ethernet, RDMA: []effects.RDMADevice{{PCI: pci, Name: "mlx5_0", Index: 7, Namespace: &effects.NamespaceIdentity{Device: 1, Inode: 2}, NamespaceMode: "exclusive", NodeGUID: "node", SystemImageGUID: "system", RelatedPCI: []string{pci}, NetDevices: []string{"eth7"}, Ports: []effects.RDMAPort{{Number: 1, LinkLayer: topology.Ethernet}}, CharDevices: []effects.RDMAChar{{Class: "infiniband_verbs", Name: "uverbs7"}}}}}
	if err := ValidatePolicy(p, registry); err != nil {
		t.Fatal(err)
	}
	candidates, err := Personas(map[string]Inventory{pci: d}, []api.DeviceExposurePolicy{p}, corev1.Node{}, registry)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("certified RoCE allocation withheld: %+v %v", candidates, err)
	}
	for _, change := range []func(*api.DeviceExposurePolicy){
		func(p *api.DeviceExposurePolicy) { p.Spec.Exposure.AllowedRDMAProfiles = nil }, func(p *api.DeviceExposurePolicy) { p.Spec.Exposure.AllowedDrivers = nil }, func(p *api.DeviceExposurePolicy) {
			p.Spec.Exposure.EffectPermissions = []string{topology.RDMANamespace}
		}, func(p *api.DeviceExposurePolicy) { p.Spec.Exposure.AllowMultipleAllocations = true },
	} {
		candidate := p.DeepCopy()
		change(candidate)
		if err := ValidatePolicy(*candidate, registry); err == nil {
			t.Fatal("incomplete RoCE policy grant accepted")
		}
	}
	for _, change := range []func(*Inventory){
		func(d *Inventory) { d.Protected = true }, func(d *Inventory) { d.Driver = "other" }, func(d *Inventory) { d.RDMA[0].NamespaceMode = "shared" }, func(d *Inventory) { d.RDMA[0].RelatedPCI = append(d.RDMA[0].RelatedPCI, "0000:29:10.2") }, func(d *Inventory) { d.RDMA[0].Ports = append(d.RDMA[0].Ports, d.RDMA[0].Ports[0]) }, func(d *Inventory) { d.RDMA[0].CharDevices = nil }, func(d *Inventory) { d.RDMA[0].NetDevices = append(d.RDMA[0].NetDevices, "other") },
	} {
		raw, _ := json.Marshal(d)
		var candidate Inventory
		if err := json.Unmarshal(raw, &candidate); err != nil {
			t.Fatal(err)
		}
		change(&candidate)
		candidates, err := Personas(map[string]Inventory{pci: candidate}, []api.DeviceExposurePolicy{p}, corev1.Node{}, registry)
		if err != nil || len(candidates) != 0 {
			t.Fatalf("uncertified/partial HCA published: %+v %v", candidates, err)
		}
	}
}

func TestIBPublicationDoesNotOfferEthernetOrPartialHCAScopes(t *testing.T) {
	const pci = "0000:29:10.0"
	registry := topology.PluginRegistry(nil)
	p := api.DeviceExposurePolicy{ObjectMeta: metav1.ObjectMeta{Name: "ib"}, Spec: api.PolicySpec{Action: "Expose", Selector: api.DeviceSelector{Kind: VFKind}, Exposure: &api.Exposure{Persona: "ipoib", Plugin: "sriov-cni-ipoib", Mode: topology.RDMAMode, AllowedDrivers: []string{"mlx5_core"}, AllowedRDMAProfiles: []api.RDMAProfile{api.RDMAProfile(topology.IPoIBSinglePort)}, AllowedMADAccess: true, EffectPermissions: []string{topology.RDMANamespace, topology.DeviceAccess}}}}
	d := Inventory{PCI: pci, Parent: "0000:29:00.0", Kind: VFKind, IfName: "ib0", Driver: "mlx5_core", LinkType: topology.InfiniBand, RDMA: []effects.RDMADevice{{PCI: pci, Name: "mlx5_0", Index: 7, Namespace: &effects.NamespaceIdentity{Device: 1, Inode: 2}, NamespaceMode: "exclusive", NodeGUID: "node", SystemImageGUID: "system", RelatedPCI: []string{pci}, NetDevices: []string{"ib0"}, Ports: []effects.RDMAPort{{Number: 1, LinkLayer: topology.InfiniBand}}, CharDevices: []effects.RDMAChar{{Class: "infiniband_verbs", Name: "uverbs7"}}}}}
	check := func(d Inventory, p api.DeviceExposurePolicy, expected int) {
		t.Helper()
		candidates, err := Personas(map[string]Inventory{pci: d}, []api.DeviceExposurePolicy{p}, corev1.Node{}, registry)
		if err != nil || len(candidates) != expected {
			t.Fatal("incorrect IB publication", candidates, err)
		}
		for _, persona := range candidates {
			if *persona.Device.Attributes[resourceapi.QualifiedName(api.Driver+"/linkType")].StringValue != topology.InfiniBand {
				t.Fatal("IB advertised as Ethernet")
			}
		}
	}
	d.RDMA[0].CharDevices = append(d.RDMA[0].CharDevices, effects.RDMAChar{Class: "infiniband_mad", Name: "umad7", Port: 1})
	check(d, p, 1)
	verbs := *p.DeepCopy()
	verbs.Spec.Exposure.Plugin, verbs.Spec.Exposure.AllowedRDMAProfiles = "sriov-cni-ib-verbs", []api.RDMAProfile{api.RDMAProfile(topology.IBVerbsSinglePort)}
	check(d, verbs, 0)
	d.IfName, d.RDMA[0].NetDevices = "", nil
	check(d, verbs, 1)
	for _, plugin := range []string{topology.SRIOVPlugin, "sriov-cni-roce"} {
		other := *p.DeepCopy()
		other.Spec.Exposure.Plugin = plugin
		if plugin == "sriov-cni-roce" {
			other.Spec.Exposure.AllowedRDMAProfiles = []api.RDMAProfile{api.RDMAProfile(topology.RoCESinglePort)}
			other.Spec.Exposure.AllowedMADAccess = false
		}
		if plugin == topology.SRIOVPlugin {
			other.Spec.Exposure.Mode, other.Spec.Exposure.AllowedRDMAProfiles, other.Spec.Exposure.AllowedMADAccess = topology.KernelMode, nil, false
		}
		other.Spec.Exposure.EffectPermissions = registry[plugin].Spec.AllowedEffects
		check(d, other, 0)
	}
	bad := *p.DeepCopy()
	bad.Spec.Exposure.AllowedRDMAProfiles = []api.RDMAProfile{api.RDMAProfile(topology.RoCESinglePort)}
	if ValidatePolicy(bad, registry) == nil {
		t.Fatal("mismatched RDMA profile grant accepted")
	}
}

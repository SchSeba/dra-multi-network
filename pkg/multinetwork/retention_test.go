package multinetwork

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/flags"
	"github.com/SchSeba/dra-multi-network/pkg/journal"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestRetirementReportRetainsOwnersAndPersistsDrainFence(t *testing.T) {
	ctx := context.Background()
	data, err := os.ReadFile("../../examples/bond-vlan/topology.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var source api.NetworkTopology
	if err := yaml.UnmarshalStrict(data, &source); err != nil {
		t.Fatal(err)
	}
	source.UID = "source-uid"
	spec, err := topology.Compile(source, topology.PluginRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	revision := api.NetworkTopologyRevision{ObjectMeta: metav1.ObjectMeta{Name: "retired", UID: "revision-uid"}, Spec: spec}
	ref := topology.RevisionIdentity(revision)
	revision.Annotations = map[string]string{topology.RetirementAnnotation: topology.RetirementToken(ref)}
	store, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	a := Agent{store: store, client: fake.NewClientBuilder().WithScheme(flags.Scheme).Build()}
	p := Preparation{Revision: revision, State: Prepared}
	if err := store.Put("prepare/claim", p); err != nil {
		t.Fatal(err)
	}
	check := func(retained bool) {
		t.Helper()
		var report topology.NodeCapabilities
		if err := a.reportReservations(ctx, []api.NetworkTopologyRevision{revision}, &report); err != nil {
			t.Fatal(err)
		}
		if !report.ReservationsChecked || slices.Contains(report.RetainedRevisions, ref) != retained || slices.Contains(report.DrainedRevisions, ref) == retained {
			t.Fatalf("incorrect retirement proof: %+v", report)
		}
		if retained && !slices.Contains(report.RetainedPlugins, topology.SRIOVPlugin) {
			t.Fatal("saved descriptor not retained")
		}
	}
	check(true)
	p.State = Released
	if err := store.Put("prepare/claim", p); err != nil {
		t.Fatal(err)
	}
	pin := filepath.Join(t.TempDir(), "pin")
	if err := os.WriteFile(pin, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	at := Attachment{Spec: api.AttachmentSpec{RevisionRef: ref}, Phase: Removed, Pin: pin}
	if err := store.Put("attach/sandbox", at); err != nil {
		t.Fatal(err)
	}
	check(true) // Terminal phase alone does not prove namespace release.
	if err := os.Remove(pin); err != nil {
		t.Fatal(err)
	}
	at.Phase = CleanupPending
	if err := store.Put("attach/sandbox", at); err != nil {
		t.Fatal(err)
	}
	check(true)
	at.Phase = Removed
	if err := store.Put("attach/sandbox", at); err != nil {
		t.Fatal(err)
	}
	binding, err := json.Marshal(api.TopologyRootConfiguration{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "TopologyRootConfiguration"}, RevisionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	claim := resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "allocated", Namespace: "test"}, Status: resourceapi.ResourceClaimStatus{Allocation: &resourceapi.AllocationResult{Devices: resourceapi.DeviceAllocationResult{Config: []resourceapi.DeviceAllocationConfiguration{{DeviceConfiguration: resourceapi.DeviceConfiguration{Opaque: &resourceapi.OpaqueDeviceConfiguration{Driver: api.Driver, Parameters: runtime.RawExtension{Raw: binding}}}}}}}}}
	if err := a.client.Create(ctx, &claim); err != nil {
		t.Fatal(err)
	}
	var report topology.NodeCapabilities
	if err := a.reportReservations(ctx, []api.NetworkTopologyRevision{revision}, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.DrainedRevisions) != 0 {
		t.Fatal("allocated but unprepared claim was acknowledged as drained")
	}
	if err := a.checkRetirement(ref); err != nil {
		t.Fatal("premature preparation fence", err)
	}
	if err := a.client.Delete(ctx, &claim); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := a.checkRetirement(ref); err == nil {
		t.Fatal("delayed preparation bypassed durable retirement fence")
	}
	var saved api.RevisionRef
	found, err := store.Get("retire/"+ref.UID, &saved)
	if err != nil || !found || saved != ref {
		t.Fatalf("missing durable retirement: %+v / %v", saved, err)
	}
	// Name reuse with another immutable UID is independent.
	other := ref
	other.UID = "another-revision-uid"
	if err := a.checkRetirement(other); err != nil {
		t.Fatal(err)
	}
	if err := a.client.Get(ctx, client.ObjectKeyFromObject(&claim), &claim); err == nil {
		t.Fatal("deleted claim reappeared")
	}
}

package controller_test

import (
	"context"
	"os"
	"strings"
	"testing"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	api "github.com/SchSeba/dra-multi-network/pkg/api/networking/v1alpha1"
	"github.com/SchSeba/dra-multi-network/pkg/controller"
	"github.com/SchSeba/dra-multi-network/pkg/flags"
	"github.com/SchSeba/dra-multi-network/pkg/topology"
)

func TestMVPController(t *testing.T) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	}()
	c, err := client.New(cfg, client.Options{Scheme: flags.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, p := range topology.Builtins() {
		if err := c.Create(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile("../../examples/bond-vlan/topology.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var object api.NetworkTopology
	if err := yaml.UnmarshalStrict(data, &object); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &object); err != nil {
		t.Fatal(err)
	}
	reconciler := controller.TopologyReconciler{Client: c}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&object)}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, &object); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(object.Status.Conditions, "Ready") || len(object.Status.RootClasses) != 2 {
		t.Fatalf("not Ready: %+v", object.Status)
	}
	var revision api.NetworkTopologyRevision
	if err := c.Get(ctx, client.ObjectKey{Name: object.Status.RevisionRef.Name}, &revision); err != nil {
		t.Fatal(err)
	}
	for _, root := range object.Status.RootClasses {
		var class resourceapi.DeviceClass
		if err := c.Get(ctx, client.ObjectKey{Name: root.ClassName}, &class); err != nil {
			t.Fatal(err)
		}
		if class.Spec.Config[0].Opaque.Driver != api.Driver {
			t.Fatal("wrong class authority")
		}
	}
	testNodeCapabilities(t, ctx, c, object, revision)
	testProgrammableRoot(t, ctx, c, reconciler)
	testIBController(t, ctx, c, reconciler)
	testIPAMController(t, ctx, c, reconciler)
	revision.Spec.Digest = "changed"
	if err := c.Update(ctx, &revision); err == nil {
		t.Fatal("revision mutation accepted")
	}
	// Invalid current generations must retain the earlier pinned allocation plan.
	oldRef := *object.Status.RevisionRef
	object.Spec.Steps[0].Config.Raw = []byte(`{"mode":"vfio"}`)
	if err := c.Update(ctx, &object); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, &object); err != nil {
		t.Fatal(err)
	}
	if meta.IsStatusConditionTrue(object.Status.Conditions, "Ready") || *object.Status.RevisionRef != oldRef {
		t.Fatal("invalid update changed pinned revision")
	}
}

func testIBController(t *testing.T, ctx context.Context, c client.Client, reconciler controller.TopologyReconciler) {
	t.Helper()
	data, err := os.ReadFile("../../examples/ipoib/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var policy api.DeviceExposurePolicy
	if err := yaml.UnmarshalStrict([]byte(strings.ReplaceAll(strings.ReplaceAll(string(data), "IB_PCI", "0000:29:00.0"), "IB_DRIVER", "mlx5_core")), &policy); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &policy); err != nil {
		t.Fatal("official CRD rejected IPoIB/MAD grant", err)
	}
	for _, file := range []string{"topology.yaml", "verbs-topology.yaml"} {
		data, err := os.ReadFile("../../examples/ipoib/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var network api.NetworkTopology
		if err := yaml.UnmarshalStrict(data, &network); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, &network); err != nil {
			t.Fatal(err)
		}
		req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&network)}
		if _, err := reconciler.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, req.NamespacedName, &network); err != nil {
			t.Fatal(err)
		}
		if len(network.Status.RootClasses) != 1 || !meta.IsStatusConditionTrue(network.Status.Conditions, "Ready") {
			t.Fatal("IB root not reconciled", network.Status)
		}
		var class resourceapi.DeviceClass
		if err := c.Get(ctx, client.ObjectKey{Name: network.Status.RootClasses[0].ClassName}, &class); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(class.Spec.Selectors[0].CEL.Expression, `.linkType == "infiniband"`) {
			t.Fatal("IB class retained the Ethernet selector", class.Spec.Selectors)
		}
	}
}

func testProgrammableRoot(t *testing.T, ctx context.Context, c client.Client, reconciler controller.TopologyReconciler) {
	t.Helper()
	read := func(path string, target any) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.UnmarshalStrict(data, target); err != nil {
			t.Fatal(err)
		}
	}
	var plugin api.NetworkPlugin
	var object api.NetworkTopology
	read("../../examples/root-effect-program/plugin.yaml", &plugin)
	read("../../examples/root-effect-program/topology.yaml", &object)
	if err := c.Create(ctx, &plugin); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &object); err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&object)}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, &object); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(object.Status.Conditions, "Ready") || len(object.Status.RootClasses) != 2 {
		t.Fatalf("programmable root not Ready: %+v", object.Status)
	}
	for _, root := range object.Status.RootClasses {
		var class resourceapi.DeviceClass
		if err := c.Get(ctx, client.ObjectKey{Name: root.ClassName}, &class); err != nil {
			t.Fatal(err)
		}
		expression := class.Spec.Selectors[0].CEL.Expression
		if !strings.Contains(expression, ".pluginContractDigest") || !strings.Contains(expression, topology.Digest(plugin.Spec)) {
			t.Fatalf("class did not lock descriptor: %s", expression)
		}
		var binding api.TopologyRootConfiguration
		if err := topology.Decode(class.Spec.Config[0].Opaque.Parameters.Raw, &binding); err != nil {
			t.Fatal(err)
		}
		if binding.PluginContractDigest != topology.Digest(plugin.Spec) || binding.RootStep != root.RootStep {
			t.Fatalf("wrong locked binding: %+v", binding)
		}
	}
	pinned := *object.Status.RevisionRef
	object.Spec.Steps[0].Config.Raw = []byte(strings.ReplaceAll(string(object.Spec.Steps[0].Config.Raw), `"selectedDriver":"igbvf"`, `"selectedDriver":"vfio-pci"`))
	if err := c.Update(ctx, &object); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, req.NamespacedName, &object); err != nil {
		t.Fatal(err)
	}
	if meta.IsStatusConditionTrue(object.Status.Conditions, "Ready") || *object.Status.RevisionRef != pinned {
		t.Fatal("invalid bound root plan replaced the pinned revision")
	}
}

func testIPAMController(t *testing.T, ctx context.Context, c client.Client, r controller.TopologyReconciler) {
	t.Helper()
	data, err := os.ReadFile("../../examples/ipam/two-vf/topology.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var object api.NetworkTopology
	if err := yaml.UnmarshalStrict(data, &object); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &object); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&object)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(&object), &object); err != nil {
		t.Fatal(err)
	}
	if len(object.Status.RootClasses) != 4 {
		t.Fatal("two VFs require two interface and two IPAM classes", object.Status)
	}
	for _, root := range object.Status.RootClasses {
		if !strings.HasPrefix(root.RootStep, "address") {
			continue
		}
		var class resourceapi.DeviceClass
		if err := c.Get(ctx, client.ObjectKey{Name: root.ClassName}, &class); err != nil {
			t.Fatal(err)
		}
		if len(class.Spec.Config) != 2 || class.Spec.Config[0].Opaque.Driver != api.Driver || class.Spec.Config[1].Opaque.Driver != topology.IPAMDriver || strings.Contains(class.Spec.Selectors[0].CEL.Expression, "pciAddress") {
			t.Fatal("IPAM root inherited physical authority", class.Spec)
		}
	}
}

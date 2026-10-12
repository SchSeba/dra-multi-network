# Programmable allocation roots

This advanced administrator example replaces native allocation **planning** with
typed templates while locking physical authority to a canonical profile.
`plugin.yaml` references the native rebind contract by name/digest; `topology.yaml`
composes two programmable VFs with a builtin bond and three VLANs. The allocation
profile, required config, output types/effects and cleanup contract remain fixed.

Commands below run from the repository root. Install the [driver](../../docs/installation.md),
set `KUBECONFIG`, and adapt policy/topology PCI addresses to dedicated secondary
hardware first. Administrative manifests require cluster-admin permissions;
workloads require only normal namespaced Pod/claim permissions.

Adapt PF/VF selectors and drivers. The digest must match the canonical profile
in the installed executable; changing the profile requires an intentionally
updated descriptor, not removing digest checks.

```bash
go run ./cmd/network-topology-compile examples/root-effect-program/plugin.yaml examples/root-effect-program/topology.yaml
./examples/root-effect-program/apply.sh
DRA_EXAMPLE_TOPOLOGY=programmable-vf-bond-vlan ./test/integration/bond-vlan/test-traffic.sh
```

Expect two VFs, bond0 and tenant100/200/300. Cleanup restores recorded connect
and preparation effects rather than evaluating an edited live template.

`device-plugins.yaml`, `device-shared.yaml` and `device-vfio.yaml` extend the same
pattern to shared child and VFIO profiles. They are regression fixtures rather
than a second simple quickstart. Their combined topology/policy documents are
used by:

```bash
./test/integration/root-effect-program/test-lifecycle.sh
./test/integration/root-effect-program/test-device-profiles.sh
```

Read the scripts before running: they use several personas, original fixture
policies and agent restart/cleanup checks. The plugin SDK remains separate from
DRA/Kubernetes/native execution; GA changes the transport to gRPC for all planners.

## Cleanup

Delete the example Pod(s) first and wait for attachment/claim cleanup. If this is
an isolated example namespace, delete it to remove Pods, generated claims and
templates together:

```bash
kubectl delete namespace multi-network-example --wait=true
```

Then delete this folder's policy/topology/custom plugin manifests, preserving any
objects used by another scenario. Do not force-remove retained finalizers or node
journals. See [operations](../../docs/operations.md).

```bash
kubectl delete -f examples/root-effect-program/topology.yaml -f examples/root-effect-program/policy.yaml
kubectl delete -f examples/root-effect-program/plugin.yaml
# If running the device-profile regression, also remove its custom manifests after drain.
```

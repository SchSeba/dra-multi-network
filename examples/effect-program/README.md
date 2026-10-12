# Declarative operation plugin

`plugin.yaml` defines `administrator-vlan`, a typed EffectProgram operation.
The topology keeps the builtin two-VF bond and uses the custom operation for VLAN
creation/configuration. It illustrates closed schema validation and parameter
bindings without moving physical authority into the planner.

Commands below run from the repository root. Install the [driver](../../docs/installation.md),
set `KUBECONFIG`, and adapt policy/topology PCI addresses to dedicated secondary
hardware first. Administrative manifests require cluster-admin permissions;
workloads require only normal namespaced Pod/claim permissions.

Use four free VFs for the traffic test, or two for one Pod. Adapt both root PF
selectors. Preview and install the administrator objects:

```bash
go run ./cmd/network-topology-compile examples/effect-program/plugin.yaml examples/effect-program/topology.yaml
kubectl apply -f examples/bond-vlan/policy.yaml -f examples/effect-program/plugin.yaml -f examples/effect-program/topology.yaml
```

Run the existing multi-root traffic fixture against this topology:

```bash
DRA_EXAMPLE_TOPOLOGY=dual-vf-bond-program-vlan ./test/integration/bond-vlan/test-traffic.sh
```

The script renders both root classes and creates the required workload/peer.
Expect the same bond/VLAN outputs and traffic behavior as the builtin example.
Do not use `connect.sh`, which supports one root. A normal application user
uses the same two-request claim pattern in [Pod usage](../../docs/user/pods.md).

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
kubectl delete -f examples/effect-program/topology.yaml -f examples/effect-program/plugin.yaml
# Remove the reused bond-vlan policy only if no other example uses it.
```

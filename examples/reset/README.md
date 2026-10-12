# Function-level reset


Commands below run from the repository root. Install the [driver](../../docs/installation.md),
set `KUBECONFIG`, and adapt policy/topology PCI addresses to dedicated secondary
hardware first. Administrative manifests require cluster-admin permissions;
workloads require only normal namespaced Pod/claim permissions.

## Prerequisites and result

A dedicated drained PCI function with verified FLR support/scope and an approved driver. Only flr is supported; this is a device mutation, not a generic PF reset API. Do not run on management functions or active peers. Expect reset preparation followed by secondary0 and recorded cleanup.

## Run

```bash
kubectl apply -f examples/reset/policy.yaml -f examples/reset/topology.yaml
./examples/connect.sh kernel-reset kernel-reset-mvp
kubectl -n multi-network-example get pod kernel-reset-mvp
kubectl -n multi-network-example get networkattachmentstatuses -o yaml
```

The helper reads the Ready revision and generated class rather than hardcoding
a DeviceClass name. `workload.yaml` files containing placeholders are templates,
not directly applicable manifests.

## Manual regression

```bash
./test/integration/reset/test-lifecycle.sh
./test/integration/reset/test-pf-lifecycle.sh
```

Read the script before running; it owns fixture resources and may restart the
agent or alter/restore device state. Drain the example workload before starting
a regression that needs the same device. Hardware-specific assertions use the
virtual lab identities and must be adapted for other nodes.

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
kubectl delete -f examples/reset/policy.yaml -f examples/reset/topology.yaml
```

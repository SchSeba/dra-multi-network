# Declarative VLAN, MAC and alias


Commands below run from the repository root. Install the [driver](../../docs/installation.md),
set `KUBECONFIG`, and adapt policy/topology PCI addresses to dedicated secondary
hardware first. Administrative manifests require cluster-admin permissions;
workloads require only normal namespaced Pod/claim permissions.

## Prerequisites and result

A free dedicated Ethernet VF selected in topology.yaml. The custom labeled-vlan descriptor validates tag, MAC and alias, and exports a typed VLAN interface. Expect secondary0 and tenant100 with the configured MAC/alias.

## Run

```bash
kubectl apply -f examples/bond-vlan/policy.yaml -f examples/link-config/plugin.yaml -f examples/link-config/topology.yaml
./examples/connect.sh labeled-vlan labeled-vlan-mvp
kubectl -n multi-network-example get pod labeled-vlan-mvp
kubectl -n multi-network-example get networkattachmentstatuses -o yaml
```

The helper reads the Ready revision and generated class rather than hardcoding
a DeviceClass name. `workload.yaml` files containing placeholders are templates,
not directly applicable manifests.

## Manual regression

```bash
./test/integration/link-config/test-lifecycle.sh
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
kubectl delete -f examples/link-config/plugin.yaml -f examples/link-config/topology.yaml
```

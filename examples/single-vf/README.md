# One SR-IOV VF

The smallest complete example: allocate one VF and move its Ethernet interface
into a Pod as `secondary0`, MTU 1500, up. No bond, VLAN or IPAM is involved.

Commands below run from the repository root. Install the [driver](../../docs/installation.md),
set `KUBECONFIG`, and adapt policy/topology PCI addresses to dedicated secondary
hardware first. Administrative manifests require cluster-admin permissions;
workloads require only normal namespaced Pod/claim permissions.

The PF in both `policy.yaml` and `topology.yaml` is `0000:29:00.0`. It must have
at least one free VF with its Ethernet driver bound and no protected management
ownership. The policy approves the VFs; the topology selects one; the claim/Pod
consumes the generated class.

```bash
./examples/single-vf/apply.sh
kubectl -n multi-network-example get pod single-vf
kubectl -n multi-network-example get resourceclaims
kubectl -n multi-network-example exec single-vf -- ip -d link show secondary0
kubectl -n multi-network-example get networkattachmentstatuses -o yaml
```

Expect the Pod Ready and `secondary0` present with MTU 1500. Admission supplies
NRI enforcement and revision placement; the workload has no plugin JSON. For
manual/user-only setup, apply the policy/topology as an administrator, then run
`./examples/connect.sh single-vf single-vf` with namespaced workload permission
and read access to the topology status.

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
kubectl delete -f examples/single-vf/topology.yaml -f examples/single-vf/policy.yaml
```

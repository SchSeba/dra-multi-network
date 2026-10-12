# Single-port RoCE

This hardware example requires a dedicated, drained, single-port HCA/function
with verified ownership and RDMA exclusive namespace mode. It is not runnable
on the current Ethernet-only QEMU lab. [Validation](../../docs/validation.md)
distinguishes software checks from hardware acceptance.

Commands below run from the repository root. Install the [driver](../../docs/installation.md),
set `KUBECONFIG`, and adapt policy/topology PCI addresses to dedicated secondary
hardware first. Administrative manifests require cluster-admin permissions;
workloads require only normal namespaced Pod/claim permissions.

## Run

Supply the dedicated function and its current driver; kind defaults to PCI and
may be PF or VF. The helper chooses the corresponding host/sriov profile, renders
policy placeholders, waits for the revision and creates the scoped workload.

```bash
DRA_RDMA_PCI=0000:aa:00.0 DRA_RDMA_DRIVER=YOUR_KERNEL_DRIVER \
DRA_RDMA_KIND=PCI ./examples/roce/apply.sh
kubectl -n multi-network-roce-example get pod roce-mvp
kubectl -n multi-network-roce-example get resourceclaims
kubectl -n multi-network-roce-example get networkattachmentstatuses -o yaml
```

Expect one dedicated whole-HCA RDMA allocation with its associated Ethernet link.
Only consuming containers receive the approved RDMA character-device access.
Do not expose a shared multi-port HCA by treating each function as independently
owned. Policy grants and hardware discovery must both permit the profile.

## Cleanup

```bash
kubectl delete namespace multi-network-roce-example --wait=true
kubectl delete networktopology single-port-roce
kubectl delete deviceexposurepolicy secondary-roce
```

Read `policy.yaml`'s actual metadata.name if adapting it. Wait for namespace,
claim and native cleanup before removing policy permissions or reinstalling the
runtime. Do not force-remove retained state.

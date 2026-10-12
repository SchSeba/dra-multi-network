# Single-port InfiniBand IPoIB

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
DRA_IB_PCI=0000:aa:00.0 DRA_IB_DRIVER=YOUR_KERNEL_DRIVER \
DRA_IB_KIND=PCI ./examples/ipoib/apply.sh
kubectl -n multi-network-ipoib-example get pod ipoib-mvp
kubectl -n multi-network-ipoib-example get resourceclaims
kubectl -n multi-network-ipoib-example get networkattachmentstatuses -o yaml
```

Expect one dedicated whole-HCA RDMA allocation and the ipoib0 interface.
Only consuming containers receive the approved RDMA character-device access.
Do not expose a shared multi-port HCA by treating each function as independently
owned. Policy grants and hardware discovery must both permit the profile.

`verbs-topology.yaml` uses the IB verbs profile without an IPoIB interface.
MAD access requires an explicit policy grant. The no-hardware regression checks
fail-closed publication, not successful InfiniBand execution:

```bash
./test/integration/ipoib/test-no-hardware.sh
```

## Cleanup

```bash
kubectl delete namespace multi-network-ipoib-example --wait=true
kubectl delete networktopology single-port-ipoib
kubectl delete deviceexposurepolicy secondary-ipoib
```

Read `policy.yaml`'s actual metadata.name if adapting it. Wait for namespace,
claim and native cleanup before removing policy permissions or reinstalling the
runtime. Do not force-remove retained state.

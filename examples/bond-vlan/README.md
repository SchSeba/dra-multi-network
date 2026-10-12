# Two VFs → bond → multiple VLANs

Two independently allocated VF roots become `vfleft` and `vfright`, members of
active-backup `bond0`. Three operation steps create `tenant100`, `tenant200` and
`tenant300`, carrying VLANs 100, 200 and 300. The claim requests both generated
DeviceClasses; bond/VLAN steps allocate no extra physical devices.

Commands below run from the repository root. Install the [driver](../../docs/installation.md),
set `KUBECONFIG`, and adapt policy/topology PCI addresses to dedicated secondary
hardware first. Administrative manifests require cluster-admin permissions;
workloads require only normal namespaced Pod/claim permissions.

Edit the PF address in `policy.yaml` and both root selectors in `topology.yaml`.
One Pod requires two free VFs; the peer/traffic check needs four total. The
fabric must permit these VLANs and the expected MAC/failover behavior.

```bash
./examples/bond-vlan/apply.sh
kubectl -n multi-network-example exec network-mvp -- ip -d link show
./test/integration/bond-vlan/test-traffic.sh
```

The first command waits for the current topology generation and both classes,
then creates a revision-named template and Pod. Expect two VFs, one bond and
three VLAN interfaces. The manual traffic script creates `network-mvp-peer`,
assigns isolated lab addresses, checks bidirectional traffic and member failure/
recovery. Read it before running on a non-lab network. It is not part of deployment.

Administrator-only setup is `kubectl apply -f policy.yaml -f topology.yaml` from
this folder. A user can render `workload.yaml` using the supplied left/right
classes and a unique template name, as explained in [Pod usage](../../docs/user/pods.md).

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
kubectl delete -f examples/bond-vlan/topology.yaml -f examples/bond-vlan/policy.yaml
```

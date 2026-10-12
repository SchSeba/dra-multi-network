# Shared macvlan/ipvlan and exclusive VF conflicts

Shared personas retain their parent on the host and allocate one child-capacity
unit. `policies.yaml`/`topologies.yaml` describe PF choices; `vf-policies.yaml`/
`vf-topologies.yaml` describe VF choices. Exclusive host/VF handoff choices share
physical compatibility domains with their corresponding children.

Commands below run from the repository root. Install the [driver](../../docs/installation.md),
set `KUBECONFIG`, and adapt policy/topology PCI addresses to dedicated secondary
hardware first. Administrative manifests require cluster-admin permissions;
workloads require only normal namespaced Pod/claim permissions.

For this example, update PF `0000:29:00.0`, VF `0000:29:10.0` and VF
`0000:29:10.2` consistently in all four manifests. Drain earlier exclusive VF/PF
workloads first. Parent interfaces must support the requested child type.

```bash
./examples/shared/apply.sh
kubectl -n multi-network-example exec macvlan -- ip -d link show secondary0
kubectl -n multi-network-example exec ipvlan -- ip -d link show secondary0
```

Expect both Pods Ready: macvlan uses the PF and ipvlan uses a different VF in the
same PF family. These are different parent netdevices. The initial helper does
not assign IP addresses; the manual regression below also checks traffic.

```bash
# Regression owns its own fixture lifecycle; first delete helper-created Pods.
kubectl -n multi-network-example delete pods macvlan ipvlan --wait=true
./test/integration/shared/test-vf-compatibility.sh
```

The regression proves macvlan/ipvlan coexistence on different parents, then
allocates an exclusive SR-IOV VF and shows a macvlan claim on that **same VF**
stays Pending until the handoff is released. It has lab-specific device/result
assertions; adapt the script as well as YAML for different hardware.

DRA compatibility is separate from native Linux constraints. Linux cannot keep
macvlan and ipvlan RX handlers on one parent simultaneously. Do not interpret
compatible persona scheduling as same-parent native coexistence. Shared mode
capacity remains enforced per advertised persona, and the node rechecks live
ownership/state before connection.

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
kubectl delete -f examples/shared/topologies.yaml -f examples/shared/vf-topologies.yaml
kubectl delete -f examples/shared/policies.yaml -f examples/shared/vf-policies.yaml
```

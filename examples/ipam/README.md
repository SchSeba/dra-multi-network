# Secondary network with DRA-backed IPAM

One topology instance requests a macvlan interface and one IPAM share in the
same ResourceClaim. The IPAM backend publishes the subnet's usable address
capacity, reserves a concrete IP during DRA preparation, and the address step
applies it before init/application containers start. Each instance has its own
claim and address. The [design](../../docs/ipam-plan.md) explains ownership,
backend persistence and failure behavior.
The backend is called through `ipam.Provider` with a local ConfigMap Store today.
The [production gRPC guide](../../docs/grpc-plugins.md) explains the future remote
provider/planner services and their deployment; these runnable manifests use
the implemented local backend.

Install the [driver](../../docs/installation.md) and set `KUBECONFIG`. Use a
**dedicated secondary PF** at `0000:29:00.0`, with its host link administratively
up and carrier available. This example grants six macvlan children and address
configuration on those children. Drain previous PF/VF consumers before changing
exposure. Adapt the PF in both policy/topology and the approved PF list in
`pool.yaml` when using different hardware.

The administrator's immutable pool ConfigMap lives in `dra-multi-network`:

| Setting | Value |
|---|---|
| Network | `blue` |
| CIDR | `192.0.2.0/29` |
| Reserved gateway | `192.0.2.1` |
| Usable range | `192.0.2.2`–`192.0.2.6` |
| Total DRA capacity | 5 addresses shared across eligible nodes |
| Eligible nodes | Nodes with the driver readiness label |

The documentation subnet is for the isolated lab. Choose a site-approved range
for other environments. Pool validation rejects overlap with other IPAM pools,
current node Pod CIDRs, ServiceCIDRs and node management IPs. The pool is private
to this allocator; DHCP or another IPAM must not use its range.

```bash
./examples/ipam/apply.sh
kubectl -n ipam-example exec ipam-demo-1 -- ip -4 addr show secondary0
kubectl -n ipam-example get resourceclaims -o yaml
kubectl -n ipam-example get networkattachmentstatuses -o yaml
```

Expect `secondary0` with one allocated `.2`–`.6` address and an attachment in
phase Ready. No secondary default route is installed. The reserved gateway is
not required for traffic between addresses in this subnet. The toolbox image
needs `NET_ADMIN` and `NET_RAW` to run its inspection/traffic tools; the framework
applies addresses independently of application capabilities.

`apply.sh` combines administrator and user steps for a lab. Normal application
users receive the revision-specific classes from `status.rootClasses` and render
[workload.yaml](workload.yaml) using those names. Its two requests explicitly
consume `children: 1` and `ipam.schseba.github.io/addresses: 1`. The controller
owns both classes. Native ResourceSlices are published by the backend; users do
not maintain slices or submit inline plugin configuration.

For [two VFs and two addresses](two-vf/README.md), use the second example after
draining this one. The complete [end-to-end suite](../../test/integration/ipam/README.md)
checks uniqueness, traffic, exhaustion, reuse, restart and both VF allocations.

## Cleanup

```bash
kubectl delete namespace ipam-example --wait=true
kubectl delete -f examples/ipam/topology.yaml -f examples/ipam/policy.yaml
kubectl delete -f examples/ipam/pool.yaml
```

Wait for consuming Pods and claims to drain before deleting administrator
configuration. A manually managed claim keeps its allocation after Pod deletion;
delete it explicitly when finished. The IPAM demo fences a released claim UID;
recreate manually managed claims for a new instance after unprepare. The controller removes a drained pool's
ledger and ResourceSlices. Missing/corrupt ledgers fence reuse, and active
reservations block pool deletion. Never force-remove ledger finalizers or erase
backend state to recover capacity.

# IPAM end-to-end checks

Use the virtual cluster with the dedicated secondary PF `0000:29:00.0`. Drain
older consumers of that PF/VF family before running. The dedicated PF must be administratively up with carrier before the test.
The pool configuration and
exposure policies are administrator-owned and require cluster-admin access.
The subnet is an isolated documentation range for this lab.

```bash
./test/integration/ipam/test-ipam.sh
```

The script checks five distinct macvlan addresses, secondary-network traffic,
Pending on the sixth complete claim, capacity reuse after cleanup and same-boot
node-agent restart. It then moves two distinct VFs into one Pod and checks two
IPAM share IDs, two different addresses and complete lease/holder release.
It deletes the example namespace between scenarios and after success. On failure
it retains Pods and claims for diagnosis; stop them and wait for cleanup before
rerunning. Administrator policies/topologies and the pool remain for inspection.

```bash
kubectl delete namespace ipam-example --wait=true
kubectl delete -f examples/ipam/two-vf/topology.yaml -f examples/ipam/two-vf/policy.yaml
kubectl delete -f examples/ipam/topology.yaml -f examples/ipam/policy.yaml
kubectl delete -f examples/ipam/pool.yaml
```

The IPAM controller removes the drained pool's backend ledger and inventory.
Do not erase ledgers or override finalizers while allocations/holders remain.

# Two VFs with two IPAM addresses

This topology moves two distinct SR-IOV VFs into a Pod as `secondary0` and
`secondary1`, then assigns one IP to each interface. One ResourceClaim requests
**four generated DeviceClasses**: `vf0`, `vf1`, `address0` and `address1`. Both
IPAM roots select the same subnet device, receive different share IDs and consume
one address unit each. The backend returns different IPs.

Use the [IPAM example prerequisites](../README.md), with at least two free VFs
under PF `0000:29:00.0`. Drain earlier consumers of that PF/VF family before
applying this policy. For other hardware, edit both policy/topology selectors and
the approved parent-PF list in the shared pool manifest.

```bash
./examples/ipam/two-vf/apply.sh
kubectl -n ipam-example exec two-vf-ipam -- ip -4 addr show secondary0
kubectl -n ipam-example exec two-vf-ipam -- ip -4 addr show secondary1
kubectl -n ipam-example get resourceclaims -o yaml
kubectl -n ipam-example get networkattachmentstatuses -o yaml
```

Expect two different `.2`–`.6` addresses with `/29` masks, two physical VF
allocation results and two IPAM share results. The interface names and IPs are
available before the init check runs. The example demonstrates two address
allocations; it does not add routing policy for multiple interfaces in the same
subnet. Both drivers participate in the same claim and only return their own
devices to kubelet.

The [integration suite](../../../test/integration/ipam/README.md) verifies the
four allocations, distinct VF identities and IPs, and complete lease cleanup.

```bash
kubectl delete namespace ipam-example --wait=true
kubectl delete -f examples/ipam/two-vf/topology.yaml -f examples/ipam/two-vf/policy.yaml
# Delete the shared pool only after every other IPAM example has drained.
kubectl delete -f examples/ipam/pool.yaml
```

# Native VF bond/VLAN and OVS switchdev together

This virtual-lab example runs four Pods on each workload node:

- `network-mvp` and `network-mvp-peer` each use two native `igbvf` VFs, an
  active-backup bond, and VLANs 100/200/300 with IPv4 forwarding enabled.
- `switchdev-ovs-a` and `switchdev-ovs-b` each use a VF on a separate compiled
  mock SmartNIC PF. Host representors connect through OVS `ovsbr0`, with native
  VLAN 100 and tagged VLANs 200/300.

Deploy either layout on the dedicated virtualization host, then run the checks:

```bash
make deploy-single-node-virtual-cluster
# Or: make deploy-multi-node-virtual-cluster
export KUBECONFIG="$HOME/.kcli/clusters/multi-network/auth/kubeconfig"
make e2e-workloads
```

The default deployment creates three NICs per workload node: management, native
igb, and mock switchdev. The mock is cloned from `main` and compiled for the
running guest kernel. VF creation is scoped to the two MAC/XML-attested test PFs;
management gets no VFs. Each test PF has four VFs. The multi-node layout has one
control plane and two workers; `NUM_OF_WORKERS` changes the worker count.

Run `apply.sh` from the virtualization host with kcli, kubectl and Python PyYAML
available. Drain existing driver claims first. The script creates the approved
OVS bridge on each node, cordons nodes for the managed eswitch transition, and
configures every exposure policy before creating claims. It discovers PCI
addresses and bridge UUIDs rather than assuming identical host identities.
Policies, topologies and namespaces are unique per node. Each node's four Pods
stay running after bidirectional traffic and bond failover checks pass.

In a multi-node cluster, both scenarios run and are tested on **each worker**.
The mock supports communication within one worker; this example does not claim
OVS mock traffic across workers. Native Pods use distinct lab IPs on each
worker, and the first two workers also exchange traffic on all three VLANs.

## Cleanup

```bash
make delete-example-bond-ovs
```

The target drains the four example Pods per node, removes their namespaces,
topologies and exposure policies, then cordons each node while the driver
restores its recorded original eswitch mode. Other driver claims must drain
before mode restoration. It uncordons nodes after successful cleanup and keeps
the shared administrator OVS bridges and installed NIC drivers. Remove an empty
`ovsbr0` manually only after verifying that no other user owns it. No journals
or finalizers are forcibly removed.

`make delete-virtual-k8s-cluster` removes the entire virtual lab instead.

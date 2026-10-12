# Switchdev with accelerated bridge

Two Pods receive distinct kernel VF endpoints as `vf0`. Their host representors
are attached to `swbr0` by `accelerated-bridge-cni`.
VLAN 100 is native/untagged; `tenant200` and `tenant300` are sandbox VLAN children.
The allocated VF and its host representor remain separate typed ports.

## Prerequisites

Install [dra-multi-network](../../../docs/installation.md), set `KUBECONFIG`, and
prepare the dedicated PF/VFs in [switchdev setup](../README.md). The lab uses
VF 0 and 1; adapt both policy and topology selectors for other hardware. The node
must already be in switchdev mode and have verified devlink PCI-VF ports.
Policies/topology are cluster-admin configuration; Pod/claim usage is namespaced.

Run the following **on that node** as an administrator, using an otherwise unused
bridge name. These commands create site-owned shared bridge state, not driver-owned
attachment state:

```bash
sudo ip link add swbr0 type bridge vlan_filtering 1
sudo ip link set swbr0 address 02:42:00:00:00:10
sudo ip link set swbr0 mtu 1500 alias site/swbr0 up
```

The bridge must retain its fixed administrator-set MAC, alias, MTU and VLAN filtering.

## Run

Run from the repository root. To run both examples together, first follow the
[configure-both sequence](../README.md#run-both-examples-together); adding policies
while another claim is active fences publication until the node drains.

```bash
./examples/switchdev/accelerated-bridge/apply.sh
kubectl -n multi-network-example get pods switchdev-bridge-a switchdev-bridge-b
kubectl -n multi-network-example exec switchdev-bridge-a -- ip -d link show
./test/integration/switchdev/test-traffic.sh bridge
```

Expect both Pods Ready, with `vf0`, `tenant200` and `tenant300`. The traffic script
assigns isolated lab addresses and checks bidirectional ping on all three
networks. It does not alter the primary network or deploy the driver. Configure
site-appropriate addressing for other environments; the POC has no IPAM.

On the node, verify `bridge vlan show` and `ip link show master swbr0`.
The representors remain in the host namespace; endpoints move into the Pods.
The [full lifecycle regression](../../../test/integration/switchdev/README.md)
also checks recorded cleanup and same-boot agent restart.

## Cleanup

```bash
./examples/switchdev/accelerated-bridge/cleanup.sh
```

This removes only the two example Pods, their template, topology and exposure
policy, after waiting for claim release. It preserves the shared administrator
bridge and eswitch policy. Verify representor port/membership, VLANs, MTU, alias,
MAC and link state restored before removing the host bridge:

```bash
sudo ip link delete swbr0
```

Run the last command on the node only after all bridge users drain. To restore
eswitch mode, follow the shared README's drain/cordon policy removal procedure.
No finalizers, journals or foreign bridge ports should be force-deleted.

## Validation limits

The mock exercises the Linux/OVS switchdev integration contract using real PCI
identities and a software packet engine. It does not certify physical offload,
external uplinks, cross-node switching, VFIO or RDMA. See
[validation](../../../docs/validation.md) for actual live results.

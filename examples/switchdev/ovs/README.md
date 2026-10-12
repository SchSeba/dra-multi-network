# Switchdev with OVS

Two Pods receive distinct kernel VF endpoints as `vf0`. Their host representors
are attached to `ovsbr0` by `ovs-cni`.
VLAN 100 is native/untagged; `tenant200` and `tenant300` are sandbox VLAN children.
The allocated VF and its host representor remain separate typed ports.

## Prerequisites

Install [dra-multi-network](../../../docs/installation.md), set `KUBECONFIG`, and
prepare the dedicated PF/VFs in [switchdev setup](../README.md). The lab uses
VF 2 and 3; adapt both policy and topology selectors for other hardware. The node
must already be in switchdev mode and have verified devlink PCI-VF ports.
Policies/topology are cluster-admin configuration; Pod/claim usage is namespaced.

Run the following **on that node** as an administrator, using an otherwise unused
bridge name. These commands create site-owned shared bridge state, not driver-owned
attachment state:

```bash
sudo ovs-vsctl --may-exist add-br ovsbr0
sudo ovs-vsctl set Bridge ovsbr0 \
  external_ids:dra-multi-network.schseba.github.io/bridge-id=site/ovsbr0 \
  other_config:hwaddr=02:42:00:00:00:20
sudo ovs-vsctl set Interface ovsbr0 mtu_request=1500
sudo ip link set ovsbr0 mtu 1500 up
sudo ovs-vsctl get Bridge ovsbr0 _uuid
```

Enable the existing host OVSDB socket mount in the chart:

```bash
helm upgrade dra-multi-network deployments/helm/dra-multi-network \
  --namespace dra-multi-network --reuse-values --set kubeletPlugin.ovs.enabled=true
```

OVS is a node prerequisite; the chart does not install/start it. Copy the actual
bridge UUID into `OVS_BRIDGE_UUID`. The helper validates/renders that identity;
`policy.yaml` is a template and must not be applied with its placeholder intact.
The bridge must use the system datapath, a fixed approved MAC and matching MTU.

## Run

Run from the repository root. To run both examples together, first follow the
[configure-both sequence](../README.md#run-both-examples-together); adding policies
while another claim is active fences publication until the node drains.

```bash
OVS_BRIDGE_UUID=THE_ACTUAL_UUID ./examples/switchdev/ovs/apply.sh
kubectl -n multi-network-example get pods switchdev-ovs-a switchdev-ovs-b
kubectl -n multi-network-example exec switchdev-ovs-a -- ip -d link show
./test/integration/switchdev/test-traffic.sh ovs
```

Expect both Pods Ready, with `vf0`, `tenant200` and `tenant300`. The traffic script
assigns isolated lab addresses and checks bidirectional ping on all three
networks. It does not alter the primary network or deploy the driver. Configure
site-appropriate addressing for other environments; the POC has no IPAM.

On the node, verify `ovs-vsctl list-ports ovsbr0` and the approved Port tag/trunks.
The representors remain in the host namespace; endpoints move into the Pods.
The [full lifecycle regression](../../../test/integration/switchdev/README.md)
also checks recorded cleanup and same-boot agent restart.

## Cleanup

```bash
./examples/switchdev/ovs/cleanup.sh
```

This removes only the two example Pods, their template, topology and exposure
policy, after waiting for claim release. It preserves the shared administrator
bridge and eswitch policy. Verify representor port/membership, VLANs, MTU, alias,
MAC and link state restored before removing the host bridge:

```bash
sudo ovs-vsctl --if-exists del-br ovsbr0
```

Run the last command on the node only after all bridge users drain. To restore
eswitch mode, follow the shared README's drain/cordon policy removal procedure.
No finalizers, journals or foreign bridge ports should be force-deleted.

## Validation limits

The mock exercises the Linux/OVS switchdev integration contract using real PCI
identities and a software packet engine. It does not certify physical offload,
external uplinks, cross-node switching, VFIO or RDMA. See
[validation](../../../docs/validation.md) for actual live results.

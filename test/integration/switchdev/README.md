# Manual switchdev integration checks

These checks target the documented four-VF `multi-network` mock lab. They do not
run during deployment or `go test`, reboot the node, or launch runtime binaries.
Use the actual hardware identities when adapting this fixture.

Prerequisites: Kubernetes 1.37/DRA gates, working NRI, mock PF `0000:29:00.0`,
VFs 0–3, managed switchdev mode, prepared `swbr0`/`ovsbr0`, and the OVSDB socket
mount. Follow [setup](../../../examples/switchdev/README.md) and both example
READMEs. Run Kubernetes commands from the repository root with `KUBECONFIG` set.
Node commands below run over an existing SSH connection with passwordless sudo.
The host checker uses Python 3, iproute2, devlink/ethtool and OVS node tools.

## Capture, connect and traffic

Start with every project claim drained. Set `NODE_SSH` to that node (for the
hypervisor-side lab commands, `cloud-user@192.168.130.65`). The checker is read-only;
it asserts the mock fixture and preserves proof of management PF/VF identities.

```bash
export NODE_SSH=cloud-user@192.168.130.65
scp test/integration/switchdev/check-host-state.py "$NODE_SSH:/tmp/"
ssh "$NODE_SSH" sudo python3 /tmp/check-host-state.py snapshot > /tmp/switchdev-before.json
scp /tmp/switchdev-before.json "$NODE_SSH:/tmp/"
./examples/switchdev/accelerated-bridge/apply.sh --configure-only
OVS_BRIDGE_UUID=THE_ACTUAL_UUID ./examples/switchdev/ovs/apply.sh --configure-only
./examples/switchdev/accelerated-bridge/apply.sh --connect-only
./examples/switchdev/ovs/apply.sh --connect-only
ssh "$NODE_SSH" sudo python3 /tmp/check-host-state.py active
./test/integration/switchdev/test-traffic.sh bridge
./test/integration/switchdev/test-traffic.sh ovs
```

The checker verifies the endpoints left the host, representors remain there,
bridge port VLAN/PVID flags match, OVS ports have tag 100/trunks 200+300 and
successful ofports, and the native management PF/five VFs/default route are intact.
Traffic assigns addresses to `vf0`, `tenant200` and `tenant300` and checks both
directions. It uses isolated lab subnets, without IPAM.

## Same-boot restart and active-claim fencing

```bash
kubectl patch deviceexposurepolicy secondary-eswitch-mode --type=merge \
  -p '{"spec":{"management":{"mode":"legacy"}}}'
kubectl wait --for='jsonpath={.status.eswitchNodes[0].phase}=PendingDrain' \
  deviceexposurepolicy/secondary-eswitch-mode --timeout=120s
ssh "$NODE_SSH" sudo devlink dev eswitch show pci/0000:29:00.0
# Must still report mode switchdev. Restore the intended request before draining.
kubectl patch deviceexposurepolicy secondary-eswitch-mode --type=merge \
  -p '{"spec":{"management":{"mode":"switchdev"}}}'
kubectl -n dra-multi-network rollout restart ds/dra-multi-network-kubeletplugin
kubectl -n dra-multi-network rollout status ds/dra-multi-network-kubeletplugin --timeout=180s
ssh "$NODE_SSH" sudo python3 /tmp/check-host-state.py active
./test/integration/switchdev/test-traffic.sh bridge
./test/integration/switchdev/test-traffic.sh ovs
```

## Release and exact restoration

```bash
./examples/switchdev/accelerated-bridge/cleanup.sh
./examples/switchdev/ovs/cleanup.sh
ssh "$NODE_SSH" sudo python3 /tmp/check-host-state.py restored /tmp/switchdev-before.json
```

Both helpers wait for generated claims to disappear. The checker compares saved
admin link state, aliases/MACs/MTUs, bridge VLANs, and exact OVS bridge/port/interface
configuration rows. It ignores transient carrier/statistics, bridge timers and VF ifindices that
can change across namespace moves. Shared administrator bridges remain present.

## Real eswitch mode restoration

After all claims/retained ownership drain:

```bash
kubectl cordon multi-network-ctlplane-0.multi-network.lab
kubectl delete deviceexposurepolicy secondary-eswitch-mode
ssh "$NODE_SSH" sudo devlink dev eswitch show pci/0000:29:00.0
# Wait until legacy is observed; native restoration is asynchronous.
ssh "$NODE_SSH" sudo devlink port show
# PCI-VF representors must be absent in legacy mode.
kubectl apply -f examples/switchdev/eswitch-policy.yaml
kubectl wait --for='jsonpath={.status.eswitchNodes[0].phase}=Ready' \
  deviceexposurepolicy/secondary-eswitch-mode --timeout=180s
ssh "$NODE_SSH" sudo devlink dev eswitch show pci/0000:29:00.0
kubectl uncordon multi-network-ctlplane-0.multi-network.lab
```

Capture a fresh host snapshot after this cycle: recreated representors have new
kernel incarnations. Repeat the configure/connect/traffic/release checks to
verify allocation and restoration against the new ports. Leave both examples
running if handing the lab over for manual POC review.

## Shared-uplink ownership acceptance

The advanced [policy](../../../examples/switchdev/uplink-policy.yaml) and
[topology](../../../examples/switchdev/uplink-topology.yaml) require an
administrator-approved physical uplink already attached to the bridge. Adapt
bridge/uplink name, aliases, PCI and exact VF selectors before applying them;
use a separate persona (`switchdev-uplink`) and drain earlier claims first.

The live mock check used `swbr0`, uplink `msnicp0` with alias `site/msnicp0` and
PCI `0000:29:00.0`. An administrator set it up/up on the bridge and added tagged
VLAN 200 as a foreign before-image. Two Pods using VF 0/1 and
`accelerated-bridge-cni-uplink` then requested VLANs 100/200/300. Native checks
confirmed all three remained after the first Pod/claim released; the last
release removed only 100/300 and preserved administrator VLAN 200 and PVID 1.
The administrator removed test VLAN 200 and restored the uplink's original
master/admin-state/alias, then the full original host snapshot matched.

The mock uplink has no external wire. This verifies real PCI identity grants,
shared VLAN refcounts and recorded restoration; external traffic requires a
physical fabric. The framework owns these leases; plugins receive typed opaque
representor inputs and cannot discover or mutate uplinks themselves.

## Optional OVS TC offload check

This was tested on the mock with both OVS Pods already addressed by the traffic
script. Use a dedicated OVS installation: the global option and service restart
affect all OVS bridges. The mock supports plain terminal redirect/drop and rejects
VLAN rewrite, CT and other unsupported actions; normal VLAN traffic is separately
validated through the software bridge path.

On the node, save global configuration and qdisc state first. The tested fixture
had only `ovsbr0`, `other_config={}`, one administrator `NORMAL` flow and no
representor ingress filters. Then:

```bash
sudo ovs-vsctl set Open_vSwitch . other_config:hw-offload=true
sudo systemctl restart ovs-vswitchd
p2=$(sudo ovs-vsctl get Interface msnicr2 ofport)
p3=$(sudo ovs-vsctl get Interface msnicr3 ofport)
sudo ovs-ofctl add-flow ovsbr0 "cookie=0x4d4e0001,priority=100,in_port=$p2,icmp,nw_src=192.168.216.10,nw_dst=192.168.216.11,actions=output:$p3"
sudo ovs-ofctl add-flow ovsbr0 "cookie=0x4d4e0001,priority=100,in_port=$p3,icmp,nw_src=192.168.216.11,nw_dst=192.168.216.10,actions=output:$p2"
sudo cat /sys/kernel/debug/mock_smartnic/0000:29:00.0/stats
```

From the Kubernetes client:

```bash
kubectl -n multi-network-example exec switchdev-ovs-a -- ping -I vf0 -c 100 -i .1 -W 2 192.168.216.11
kubectl -n multi-network-example exec switchdev-ovs-b -- ping -I vf0 -c 100 -i .1 -W 2 192.168.216.10
```

On the node, require actual offloaded flows, `in_hw` rules with nonzero hardware
packet counters, and increased mock `offload_hits`:

```bash
sudo ovs-appctl dpctl/dump-flows -m --names type=offloaded
sudo tc -s -d -j filter show dev msnicr2 ingress
sudo tc -s -d -j filter show dev msnicr3 ingress
sudo cat /sys/kernel/debug/mock_smartnic/0000:29:00.0/stats
# Remove only test-cookie flows; restore the saved global option (originally absent).
sudo ovs-ofctl del-flows ovsbr0 'cookie=0x4d4e0001/-1'
sudo ovs-vsctl remove Open_vSwitch . other_config hw-offload
sudo systemctl restart ovs-vswitchd
```

Verify no ingress filters remain. Remove an empty `clsact` qdisc only if the saved
before-image proves this test created it; preserve any preexisting qdisc. Repeat
normal VLAN traffic and exact release checks after restoring the administrator's
original OVS configuration.

## Scope

These are Linux/OVS integration and mock forwarding checks. Physical offload,
external uplinks, RDMA/InfiniBand and reboot recovery remain separate acceptance
work. Optional OVS TC checks must show `in_hw`, actual offloaded flows and increased
mock `offload_hits`; `hw-offload=true` alone is not an execution result. Linux-bridge
FDB offload is outside the mock driver's scope. See [validation](../../../docs/validation.md).

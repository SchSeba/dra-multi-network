# Switchdev examples

Both examples allocate kernel VFs through DRA, move endpoints into Pods with
NRI, and keep separately typed VF representors on the host. The host port is
attached by an internal planner using the shared plugin SDK; the framework owns
native execution, policy grants, journaling and recorded cleanup.

| Example | Plugin | Host bridge | Dedicated lab VFs |
|---|---|---|---|
| [Accelerated bridge](accelerated-bridge/README.md) | `accelerated-bridge-cni` | Linux `swbr0` | VF 0 and 1 |
| [OVS](ovs/README.md) | `ovs-cni` | OVS `ovsbr0` | VF 2 and 3 |

Each creates two Pods and demonstrates untagged VLAN 100 plus tagged VLANs 200
and 300. The policies select disjoint VF pairs; topology selectors also require the exact exposure persona and repeat that
restriction so a class cannot accidentally choose the other example's host
grant. Both examples can run together on a PF with at least four eligible VFs.

## Run both examples together

Configure **both** policies before starting any Pods. Publication contract
changes require a full node drain; disjoint VF pairs do not remove that rule.
A topology's `Ready` condition confirms compilation, not free matching devices.

```bash
./examples/switchdev/accelerated-bridge/apply.sh --configure-only
OVS_BRIDGE_UUID=THE_ACTUAL_UUID ./examples/switchdev/ovs/apply.sh --configure-only
./examples/switchdev/accelerated-bridge/apply.sh --connect-only
./examples/switchdev/ovs/apply.sh --connect-only
./test/integration/switchdev/test-traffic.sh bridge
./test/integration/switchdev/test-traffic.sh ovs
```

The default helper mode configures and connects one example. Drain existing
claims before introducing another exposure policy. Obtain the bridge UUID and
prepare host bridges using each example's README first.

## Hardware and mock-driver setup

Use a switchdev-capable device with verified local PCI/devlink representor
identity. The mock lab uses [switchdev-mock-driver](https://github.com/SchSeba/switchdev-mock-driver)
on its pinned kernel and a dedicated, XML-attested QEMU igb PF. It exercises real
PCI VFs, devlink, Linux/OVS forwarding and kernel namespace moves. Its guest-memory
packet engine is not a physical NIC performance or external-wire test.

The example addresses are PF `0000:29:00.0` and VFs `0000:29:10.0`, `.2`, `.4`, `.6`.
For other hardware, edit policy and topology selectors consistently. Create VFs
before exposing them; these examples do not install the mock, create VFs, change
PF drivers or replace the primary network. Never select the management PF.

For the mock installer, first drain all consumers, attest the dedicated PF using
libvirt XML, follow its pinned-kernel/signing requirements and use its existing
`setup-worker.sh`. It ends in legacy mode with zero VFs. A cluster administrator
then creates the intended VFs on that exact PF. Stop the old native-VF lab boot
service before installing the mock's persistence to prevent conflicting owners.

The current `multi-network` lab uses a scoped, **current-boot-only** binding:
its unrelated primary PF and five native `igbvf` VFs prevented the mock installer's
global VF-driver switch. Only secondary PF `0000:29:00.0` and its four VFs were
bound with per-device `driver_override`; no global native VF driver was unloaded
or blacklisted. The original `create-sriov-vfs.service` is disabled to avoid
conflicting owners. Reboot persistence/recovery remains deferred; rebooting this
lab requires administrator re-preparation before running these examples.

## Managed eswitch mode

The shared `eswitch-policy.yaml` is an explicit administrator transition to
switchdev using `ManageEswitch`. The agent requires a drained, cordoned node;
publication is fenced before the native change. For the lab:

```bash
# Use the actual selected node name; all project claims must be drained first.
kubectl cordon multi-network-ctlplane-0.multi-network.lab
kubectl apply -f examples/switchdev/eswitch-policy.yaml
kubectl wait --for='jsonpath={.status.eswitchNodes[0].phase}=Ready' \
  deviceexposurepolicy/secondary-eswitch-mode --timeout=180s
kubectl uncordon multi-network-ctlplane-0.multi-network.lab
```

Verify `status.eswitchNodes` and actual devlink state on the node. To restore the
recorded original mode, drain both examples, cordon the node, delete the policy
and wait for devlink to report legacy before uncordoning. Do not manually switch
modes under active attachments or erase the management journal.

## Extra profiles and validation

`uplink-policy.yaml` and `uplink-topology.yaml` illustrate approved shared-uplink
VLAN ownership. They require a real approved uplink name/alias/PCI identity; the
mock's synthetic uplink has no external wire. These advanced manifests are not
part of either two-Pod quickstart.

[Live regression](../../test/integration/switchdev/README.md) checks publication,
endpoint/representor separation, traffic, host VLAN/OVS state, agent restart and
release/restoration. [Validation](../../docs/validation.md) records the tested
scope. Every planner remains behind the [SDK boundary](../../pkg/pluginapi/README.md);
GA moves all planners to gRPC while DRA/native authority stays in the framework.

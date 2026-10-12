# POC validation and limits

This describes tested behavior, not a production certification. Installation and
redeployment do not automatically run manual verification.

## Software checks

The Go suite covers CRD/admission boundaries, immutable revision/class binding,
plugin schemas and composition, full root coverage, capability placement,
retention, serializable plugin isolation, physical ownership, scoped access,
prepare/connect compensation and selected native journal/recovery paths.
Controller envtest uses Kubernetes 1.37 assets. Generated artifacts, lint, build,
Helm rendering and offline example compilation are checked separately.
The suite does **not** launch containerd or CRI-O processes.

## Live POC scope

The existing QEMU SR-IOV/CRI-O lab exercised kernel VF attachment, two VFs →
active-backup bond → VLANs 100/200/300, bidirectional traffic/failover, shared
child capacity/conflicts, VF handoff versus shared VF child exclusion, scoped
VFIO access, approved VF settings/binding/reset and selected same-boot agent
restart/compensation/retention paths. A two-Pod baseline used four VFs and had
successful traffic on VLAN 100 at the prior MVP handoff.

After repository cleanup, the redeployed controller and node agent reached
Ready. The single-VF quickstart and prior bond/VLAN smoke checks passed on the
native QEMU SR-IOV device. That dedicated secondary PF was subsequently drained
and rebound to the switchdev mock for the acceptance below; the earlier bond
baseline is no longer deployed on this node.

The shared regression distinguishes a scheduling-compatible mixed persona
allocation from native Linux RX-handler conflicts on one parent. This POC does
not claim simultaneous macvlan+ipvlan native interfaces on one netdevice. They
can be used together on different parents. The VF conflict test also verifies
that an exclusive VF handoff prevents macvlan use on that same VF.

## Switchdev mock acceptance (2026-10-06)

Kubernetes 1.37 `multi-network`, CRI-O, pinned guest kernel
`5.14.0-427.el9.x86_64`, Open vSwitch 3.5, and `mock_smartnic` on dedicated PF
`0000:29:00.0` with four real PCI VFs. The primary PF `0000:15:00.0`, its five
native `igbvf` VFs, management addresses and default route were preserved.

- Two accelerated-bridge Pods use VF 0/1 with representors on Linux `swbr0`;
  two OVS Pods use VF 2/3 with representors on `ovsbr0`.
- Both paths passed bidirectional traffic on native VLAN 100 and tagged VLANs
  200/300, with endpoints in sandbox namespaces and typed representors on host.
- Same-boot agent restart retained both attachments and all traffic paths.
- A real legacy request stayed `PendingDrain` while claims were active; actual
  devlink mode stayed switchdev. After full release and cordon, policy deletion
  restored the original legacy mode. Reapplying it recreated switchdev ports.
- Release restored saved mock PF/VF/representor and shared bridge link state,
  bridge VLANs and exact OVS configuration rows, including their UUIDs.
- With administrator-enabled OVS hardware offload and two explicit test-cookie
  IPv4 ICMP forwarding rules, both TC filters reported `in_hw`; OVS dumped two
  `offloaded:yes, dp:tc` flows and the mock recorded 399 offload hits. Each
  direction passed 100/100 ping replies. The test flows/global option and
  test-created empty qdiscs were then removed, restoring the original OVS setup.

The live run found and fixed an OVS internal-port identity check which assumed
all valid system-datapath bridges had a Linux master. It also exposed allocation
against a stale persona during policy publication; the examples now select exact
personas and offer a configure-both-before-connect sequence. Redeployment retains
existing Helm settings, including the OVSDB socket mount.

[Examples](../examples/switchdev/README.md) and [manual checks](../test/integration/switchdev/README.md)
contain the runnable setup, traffic and lifecycle verification. The mock binding
on this node is current-boot-only; reboot recovery remains deferred. This validates
real Linux/OVS/PCI integration with a guest-memory packet engine, not physical NIC
performance or external-wire connectivity. Linux-bridge FDB offload is outside the
mock's supported scope. Shared-uplink ownership was also exercised on the mock PF's real PCI uplink,
admin-attached to `swbr0`: two attachments requested tagged VLANs 100/200/300,
with VLAN 200 already administrator-owned. The first release kept all VLANs;
the last removed only project-added 100/300 and preserved 200. Administrator
test changes were then reversed and the original host snapshot matched. This
checks native ownership/refcount and before-image restoration, without claiming
external-wire connectivity.

## Hardware acceptance still needed

The VM has no RDMA/InfiniBand device or physical switchdev NIC. Physical RDMA
namespace/device access, InfiniBand verbs/IPoIB/MAD, physical representor offload
and external fabric behavior require suitable hardware and remain unverified.
Software validation and fail-closed handling exist; do not infer hardware
acceptance from those tests.

## Deferred for POC review

Host reboot recovery, broader hardware restoration/composition, upgrades/migration
and the final complete live regression/feature-gate recheck remain deferred for
manual review. Earlier manual feature-gate checks are not deployment hooks.
No new runtime-binary tests are required. See [roadmap](roadmap.md) and
[manual integration checks](../test/integration/README.md).

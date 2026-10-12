# DeviceExposurePolicy configuration

DeviceExposurePolicy is a **cluster-scoped administrator** permission boundary.
Discovery observes devices; policies decide which eligible devices/personas are
advertised and which mutations are allowed. No default policy exposes every
PCI function. Protected primary/default-route/management devices are withheld.

## Select a device and node

`nodeSelector` selects participating nodes. `selector.kind` is `PF`, `VF` or
`PCI`; optional PCI/parent PCI addresses, vendor, driver and link type narrow the
match. Inspect PCI identity and routes on the actual node before writing policy.
Policies are evaluated by descending priority, then name. An `Exclude` stops
later matches for that device; an earlier exposure is not retroactively removed.
Put exclusions at a higher priority than exposures when they must take precedence.
The first matching exposure for a particular plugin wins; different plugin
personas can coexist subject to derived physical compatibility.

```yaml
apiVersion: networking.dra-multi-network.schseba.github.io/v1alpha1
kind: DeviceExposurePolicy
metadata:
  name: secondary-vfs
spec:
  priority: 100
  nodeSelector:
    network.example.com/secondary: "true"
  selector:
    kind: VF
    parentPCIAddresses: ["0000:29:00.0"]
  action: Expose
  exposure:
    persona: kernel
    plugin: sriov-cni
    mode: kernel
    effectPermissions:
    - linux/link-move/v1
    - linux/link-configure/v1
```

Adapt both label and PCI address. `persona` distinguishes advertised choices;
`plugin` references the allocation descriptor, and `mode` must match its profile.
The topology must also select that plugin/mode. An invalid policy fences
publication rather than advertising a partially authorized inventory.

## Ownership and shared capacity

Physical compatibility is derived from inventory, not supplied by a tenant or
weakened by policy. Personas consume interface and PF-family counter domains.
An exclusive host PF handoff conflicts with its VFs. A VF handoff conflicts with
shared children on that same VF. Shared children retain their parent on the
host and consume the `children` capacity of their persona.

For macvlan/ipvlan, set `allowMultipleAllocations: true` and:

```yaml
capacity:
  children:
    value: "2"
    default: "1"
    validValues: ["1"]
```

The POC uses one child per allocation. It advertises the two personas and their
compatibility domains; the node rechecks live ownership before connection.
Linux parent state constrains coexistence: independently allocated macvlan and
ipvlan share modes can be eligible in DRA, but Linux cannot attach both RX-handler
families simultaneously to one netdevice. Runtime rejects/compensates an invalid
live mix rather than bypassing this restriction. Use separate parents to keep
macvlan and ipvlan interfaces active together. The [shared example](../../examples/shared/README.md)
and integration test distinguish scheduling compatibility from actual native
coexistence and demonstrate that exclusive VF handoff blocks macvlan on the VF.

`exposure.compatibility` cannot override the derived physical domains in this
POC. Unsupported custom compatibility is rejected. Increasing child capacity
does not authorize different modes or additional physical devices.

## Hardware and host object grants

| Field | Purpose |
|---|---|
| `effectPermissions` | Explicit hardware/shared-host effect names permitted by policy |
| `allowedDrivers` | Source/target drivers for approved binding/RDMA profiles |
| `allowedVFSettings` | Permitted VF fields: MAC, trust, spoof check, link state, VLAN ID/QoS/protocol and min/max rates |
| `allowedResetMethods` | POC supports function-level `flr`, subject to verified scope |
| `allowedRDMAProfiles` | `roce-single-port-v1`, `ib-verbs-single-port-v1`, `ib-ipoib-single-port-v1` |
| `allowedMADAccess` | Explicit InfiniBand MAD permission, scoped to the verified HCA |
| `allowedHostBridges` | Existing bridge name, administrator alias, VLANs and optional verified uplink identities/VLANs |
| `allowedOVSBridges` | Existing OVS bridge name, UUID, administrator identity and VLANs |

A grant is necessary but not sufficient. The executor verifies IOMMU/FLR scope,
whole-HCA single-port ownership, RDMA namespace mode, representor PCI/devlink
identity, bridge/uplink identity and current state as appropriate. Missing or
ambiguous metadata withholds publication or rejects the effect. VFIO uses
scoped CDI device specs; RDMA access is granted through NRI to actual consuming
containers. Multi-function IOMMU groups or shared multi-port HCAs are not
silently split into independent roots.

## Eswitch mode management

`action: ManageEswitch` uses `management` instead of `exposure`. The supported
profile is `ethernet-pf-vfs-v1`, with target `mode`, `allowedModes` and explicit
`linux/eswitch-configure/v1` permission. Select the PF by verified identity.
This is an administrative transition, not a tenant effect. The agent withdraws
family advertisements, waits for allocations/attachments/preparation and
retained ownership to drain, captures original state, then performs and verifies
the transition. `status.eswitchNodes` reports each node/PF phase, mode and error.
Deleting the management policy requests restoration through the recorded fence.

Approved shared-uplink VLANs use `allowedHostBridges[].uplinks` identities
(name, alias, PCI address) and VLAN grants. The executor journals/refcounts
project-owned uplink VLAN memberships across attachments and restores only its
owned change after the last holder leaves; it does not alter bridge-wide
configuration. Both mechanisms are implemented, with hardware acceptance limits
in [validation](../validation.md). See the [switchdev example](../../examples/switchdev/README.md)
for complete manifests.

# NetworkPlugin configuration

NetworkPlugin is a **cluster-scoped administrator** contract. It says which
configuration is valid, which typed ports a plugin consumes/produces, which
effects it may request and how its recorded state is cleaned up. It does not
approve a physical device: DeviceExposurePolicy supplies that authority.

## Shipped implementations

The controller seeds canonical builtin descriptors. A builtin descriptor must
match the supported contract exactly; editing its schema to weaken validation
does not create a new native implementation. Use an EffectProgram descriptor
for supported declarative customization. Every profile and its exact schema is
listed in the [API reference](../api-reference.md).

| Family | Topology role | Supported baseline |
|---|---|---|
| `host-cni` | Allocation | Exclusive host PF/PCI interface; hardware/RDMA variants |
| `sriov-cni` | Allocation | Exclusive VF interface; configured VF, binding/reset, VFIO, RDMA and switchdev variants |
| `macvlan-cni` | Allocation | Shared child on host-retained Ethernet parent, bridge mode |
| `ipvlan-cni` | Allocation | Shared child on host-retained Ethernet parent, L2 mode |
| `bond-cni` | Operation | 2–8 exclusive Ethernet members, active-backup, active failover MAC |
| `vlan-cni` | Operation | VLAN 1–4094 on an approved Ethernet parent |
| `accelerated-bridge-cni` | Operation | Host representor attachment to an approved existing Linux bridge |
| `ovs-cni` | Operation | Host representor attachment to an approved existing OVS bridge |

Macvlan/ipvlan are allocation roots in this POC: their parent capacity is managed
by DRA. They are not general arbitrary child operations on every other output.
RDMA, VFIO and representor ports have distinct types and cannot silently become
bond/VLAN inputs.

## Descriptor fields

- `implementation`: `type`, implementation `name`, and supported `version`.
  `Builtin` calls a compiled internal planner; `EffectProgram` binds a typed
  declarative program. Neither launches a CNI executable.
- `kind`: `Allocation` owns a DRA-selected root; `Operation` uses prior outputs.
- `configSchema`: closed JSON object schema. Unknown fields, bad types, missing
  required fields and out-of-range values fail before revision publication.
- `inputs`/`outputs`: named ports with `type`, optional `linkType`, cardinality,
  input `access` and output `ownership`. Topology references use `step.port`.
- `allowedEffects`: descriptor ceiling for native operations.
- `stateContract`: supported contract version and `cleanup: RecordedEffects`.
- `allocationProfileRef`: for programmable allocation roots, the canonical
  native profile name and exact digest. Custom planning must preserve its
  physical ownership, native required fields, output/effect contract and mode.
- `hooks`: typed templates for prepare/connect and recorded cleanup declarations.

`status.digest`, `status.observedGeneration` and `Accepted` report validation.
Descriptors used by revisions are copied into immutable locked contracts, so an
edit cannot change cleanup of an existing allocation.

## Lifecycle and framework separation

| Hook | When | Result |
|---|---|---|
| `NodePrepare` | DRA prepares the allocated claim | Approved hardware/device-access preparation |
| `ConnectNetwork` | NRI supplies the sandbox | Network construction and typed exports |
| `RemoveNetwork` | Sandbox cleanup or connect compensation | Restore recorded `ConnectNetwork` execution |
| `NodeUnprepare` | Claim release or prepare compensation | Restore recorded `NodePrepare` execution |

Cleanup does not accept new arbitrary deletion targets. It identifies the
original framework execution and hook. The framework checks the saved contract,
allocation identity, live state and journal before restoring before-images.

All eight families use the [serializable plugin SDK](../../pkg/pluginapi/README.md).
Only the DRA framework discovers devices, publishes slices, authorizes effects,
executes Linux/OVS changes and writes state. GA will replace internal calls with
gRPC for every implementation; no plugin gains Kubernetes or journal authority.

## Declarative EffectProgram

The POC includes a typed effect language for administrator-authored planners.
It supports explicit input references, schema-checked parameters/bindings,
requirements, postconditions, recovery strategies and exported ports. It has no
arbitrary shell, arbitrary filesystem target, runtime namespace path or direct
Kubernetes access. Effect IDs and references must form an ordered valid program.
The framework still intersects descriptor, topology budget, physical grants and
live ownership before execution.

Start with runnable [operation customization](../../examples/effect-program/README.md),
[link configuration](../../examples/link-config/README.md) or
[programmable allocation roots](../../examples/root-effect-program/README.md).
Preview before applying:

```bash
go run ./cmd/network-topology-compile \
  examples/effect-program/plugin.yaml examples/effect-program/topology.yaml
kubectl apply -f examples/effect-program/plugin.yaml
kubectl wait --for=condition=Accepted networkplugin/administrator-vlan --timeout=120s
kubectl apply -f examples/effect-program/topology.yaml
```

Read the example's actual `metadata.name` if customizing it. Offline preview
validates compilation and outputs a preview revision; it does not publish API
objects, assign an authoritative UID, check hardware or authorize execution.

Supported opcode families include link move/configure, bond/master/VLAN,
macvlan/ipvlan creation, driver bind, VF settings, PCI FLR, RDMA namespace,
runtime device access, representor mapping/bridge/OVS attachment and shared
uplink VLANs. Eswitch mode management is an administrator policy operation;
placing its name in a topology budget alone cannot authorize a PF transition.
Consult [API reference](../api-reference.md) for schemas, not CNI JSON examples
from unrelated plugin versions.

## Retirement

Deleting a custom plugin stops new consumers of its locked revisions. Existing
claims retain the exact cleanup contract. Retention finalizers wait for cluster
and node ownership to drain; do not remove them to force deletion. See
[operations](../operations.md).

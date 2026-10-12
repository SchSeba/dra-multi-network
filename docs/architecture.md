# Architecture

The controller turns administrator intent into immutable contracts; the node
agent turns an allocated contract into a Pod network. Kubernetes DRA schedules
physical resources and NRI supplies the Pod sandbox lifecycle. The primary CNI
continues to create the normal Pod network.

```mermaid
sequenceDiagram
  participant Admin
  participant Controller
  participant Node as Node agent
  participant API as Kubernetes DRA
  participant Runtime as Runtime / NRI
  Admin->>Controller: NetworkPlugin + NetworkTopology
  Controller->>API: Revision + DeviceClass per root
  Admin->>Node: DeviceExposurePolicy
  Node->>API: Inventory personas + ResourceSlices
  API->>Node: NodePrepareResources for allocated claim
  Node->>Node: Verify locked contract, grants; journal preparation
  Runtime->>Node: Pod sandbox available
  Node->>Node: ConnectNetwork in DAG order
  Node->>API: NetworkAttachmentStatus
  Runtime->>Node: Create/Start consuming containers
  Node->>Runtime: Scoped device access; verified attachment
  Runtime->>Node: Sandbox removal
  Node->>Node: RemoveNetwork in reverse order
  API->>Node: NodeUnprepareResources
  Node->>Node: Restore prepared hardware; release journal ownership
```

## Responsibilities

| Component | Owns |
|---|---|
| Controller | Plugin acceptance, topology compilation, immutable revisions, DeviceClasses, capability labels, retention, admission service |
| Node agent | Discovery/protection, exposure decisions, ResourceSlice publication, DRA prepare/unprepare, NRI callbacks, claim/runtime identity, status projection |
| Plugin planner | Normalize its approved config and granted references into a serializable effect program; request restoration of recorded execution on cleanup |
| Framework executor | Policy intersection, live ownership checks, native Linux/OVS mutations, before-images, durable intent, compensation and recovery |
| Kubernetes | Claim lifetime, scheduling/allocation, capacity and compatibility accounting |

Internal planners in `pkg/networkplugins` depend on the stdlib-only
`pkg/pluginapi` contract. They receive opaque allocation and port references;
they cannot read Kubernetes objects, discover new hardware, enter namespaces or
write the journal. `pkg/topology` validates and binds their results, and
`pkg/effects` executes authorized effects. OVSDB communication belongs to the
framework executor. There is no internal gRPC or external plugin process in the
POC. GA will move every planner behind gRPC without transferring framework
ownership; the transport implementation remains future work.

## State and identity

A revision locks the source topology UID, normalized config, plugin descriptors,
execution order and digests. A generated DeviceClass embeds the revision name,
UID, digest, root step and plugin contract digest. NodePrepare verifies the
class-origin configuration and allocated physical identities against the locked
revision and policy. A workload cannot substitute its own opaque configuration
or request only part of a multi-root topology.

The host-mounted journal under `/var/lib/dra-multi-network` records allocations,
attachment identity, native intent, applied state, original state and cleanup
progress. Runtime attachment identity includes Pod UID, sandbox, node UID,
boot ID, claim UID and allocation fingerprint. Status CRDs project this state
for observation; editing or deleting status does not grant access or erase
ownership. Cleanup uses the saved revision and saved original execution, even
if live administrative configuration changes.

## Failure behavior

Prepare/connect persist intent before mutation. Failed connection compensates
owned effects in reverse order. Cleanup verifies physical identity and expected
applied state before restoring before-images; unexpected foreign state blocks
cleanup and retains ownership instead of overwriting it. Readiness/capability
publication fences nodes that cannot enforce the revision. NRI disconnection
ends the node process so normal restart can reconnect.

Same-boot agent restart and selected lost-acknowledgement paths have been tested.
Do not equate these checks with complete host-reboot recovery; see
[validation](validation.md) and [operations](operations.md).

## IPAM allocation roots

The controller also publishes subnet ResourceSlices for `ipam.schseba.github.io`,
and the existing node process registers that second DRA driver. A topology can
request network and address roots in one claim. IPAM prepares concrete addresses
in a protected, persistent ConfigMap ledger; NRI acquires attachment holders and
executes recorded address effects before containers start. Cleanup removes owned
address use before releasing holders. DRA capacity and concrete address ownership
remain separate accounting layers. See [IPAM](ipam-plan.md) and its examples.

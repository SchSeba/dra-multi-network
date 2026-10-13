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

## Current POC responsibilities

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
POC. These describe the implemented boundary, not the production execution API.

## Production target: vendor-owned network execution

External gRPC plugins own both config/plan validation and native network operations.
Vendors add supported network behavior by registering a descriptor and deploying
a plugin, without adding vendor-specific compiler branches or core effect handlers.
The core retains DRA discovery/publication/allocation verification, generic schema
and DAG checks, immutable revisions, grants, runtime identity and lifecycle
coordination. This revises the earlier planner-only gRPC proposal.

Topology apply invokes mutation-free plugin validation/planning and publishes the
locked revision/classes. Allocated Pod execution invokes node-local plugin hooks:
`NodePrepare`, `ConnectNetwork`, `RemoveNetwork`, `NodeUnprepare`. Their executor
performs Linux/OVS/vendor changes and restores recorded native before-images. The
core journals coordination/receipts and retains physical ownership until cleanup
is verified; plugins persist native intent, applied state and restoration progress.
A lost reply or unavailable executor cannot make a resource free.

Controller-accessible validation and node-local execution endpoints must resolve
the same locked release. Generic grants bind the allocated node, sandbox and
physical resources; they do not sandbox arbitrary privileged vendor code. Node
executors are operator-approved and need scoped native access. Versioned dispatch,
namespace delegation, execution receipts, replay/query, fencing and cross-process
recovery remain unimplemented. The current planning SDK is insufficient for this
execution protocol. Third-party descriptors declare ownership, compatibility mode
and mutation scope; core binds those declarations to discovered physical
interface/PF-family domains and owns ResourceSlice groups/capacity. Plugins cannot
choose domains or duplicate capacity to bypass conflicts. The
[production gRPC guide](grpc-plugins.md#compatibility-ownership-for-third-party-plugins)
defines that boundary and the six reference profile mappings.

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

The IPAM driver, node attachment lifecycle and controller inventory publisher
call `ipam.Provider`. Its local `ipam.Store` implementation owns the protected
ledger; requests and pool/lease results are serializable values. Kubernetes
claim verification and DRA publication remain with callers. Production uses a
separate gRPC IPAM provider service. Network plugin validation remains pure; the
node-local address executor performs authorized native address changes and cleanup.
Provider mutations/storage credentials remain separate from network execution,
and holder release waits for verified native cleanup.

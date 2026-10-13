# Plugin boundary: current SDK and production target

This package is the **current planning-only** request/result contract for internal
POC plugins. It imports only the Go standard library. Planners do not receive
ResourceClaims, DeviceClasses, ResourceSlices, clientsets, namespace paths or
journal handles. It is not a native-execution RPC API.

| Current POC owner | Responsibility |
|---|---|
| DRA/framework | Discovery/publication, allocation binding, grants, descriptor/config validation, ordering, live ownership, native execution, journal and recovery |
| Internal planner | Turn normalized config and granted references into an effect program; request recorded cleanup |
| `pluginapi` | Serializable config, opaque references, effects, four hook names and response-scope checks |

Builtin planners in `pkg/networkplugins` run through ordinary Go calls.
`pkg/topology/plan.go` binds inputs and validates results. `pkg/effects` performs
Linux/OVS mutations and recorded compensation. `EffectProgram` CR templates are
current framework compilation; they do not launch external services or gain
new allocation authority.

The four hooks are `NodePrepare`, `ConnectNetwork`, `RemoveNetwork` and
`NodeUnprepare`. Current cleanup identifies the original framework execution/hook;
it never selects fresh deletion targets or replans prepare/connect. Saved revision,
claim/allocation/attachment identity and expected native state fence restoration.
Editing or deleting a live CRD does not select new cleanup targets. Dependency
isolation and JSON round-trip tests cover these pure internal planners; OVSDB
currently belongs to the core executor.

For example, `tuning-cni` consumes a granted interface plus `ipv4Forward`, returns
`linux/ipv4-forwarding/v1`, and exports `configured`. Namespace entry, sysctl
mutation/read-back and before-image restoration happen in the framework today.

## Production target

External gRPC plugins own **both validation/planning and native execution**.
Vendors add supported interface operations and restoration in their own plugin,
without editing core compiler switches or native effect handlers. Core discovery,
DRA publication/allocation verification, generic policy/schema/DAG checks,
immutable revision binding and lifecycle coordination remain shared mechanisms.
This replaces the earlier planner-only transport proposal.

Topology compilation invokes mutation-free validation/planning. At runtime the
core authorizes and coordinates the four hooks on a node-local plugin executor;
the executor performs Linux/OVS/vendor operations and records native intent,
before-images, applied state and cleanup progress. The core durably records
operation dispatch/receipts and ownership. Both sides reconcile uncertain outcomes
using the same execution identity; cleanup restores saved native state, not
newly chosen targets. Retention waits for verified executor restoration.

The hook names remain, but current `Request`/`Result` values are insufficient for
that distributed execution contract. Versioned envelopes, authenticated scope,
namespace/resource delegation, output receipts, query/replay, fencing, durable
state ownership and upgrade-compatible cleanup still require design/implementation.
External executors need operator-approved scoped native privileges; current
planner dependency restrictions do not imply future executors cannot use netlink
or OVSDB. Validation remains pure. Grant tokens alone do not sandbox privileged code.

The separate [`ipam.Provider`](../ipam/provider.go) owns inventory, concrete address
reservations, holders and release. Current callers inject local `ipam.Store`;
production callers will inject a gRPC client. Node address executors consume
verified leases, but do not get reservation authority or backend credentials.
Holder release follows verified native address cleanup. See the
[production guide](../../docs/grpc-plugins.md) for vendor registration, node-local
execution, generic action permissions, IPAM RPC mapping and acceptance requirements.

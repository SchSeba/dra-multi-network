# Plugin boundary

This package is the shared, versioned request/result contract for all plugins.
It imports only the Go standard library. Plugins do not receive ResourceClaims,
DeviceClasses, ResourceSlices, clientsets, namespace paths or journal handles.

| Owner | Responsibility |
|---|---|
| DRA/framework | Discovery, publication, allocation binding, exposure-policy grants, descriptor/config validation, topology ordering, effect-budget intersection, live ownership, execution, journal, readiness, retention and recovery |
| Plugin | Turn one normalized configuration and its explicitly granted port references into an effect program; declare restoration of the original execution for cleanup |
| `pluginapi` | Serializable configuration, opaque references, effects, four lifecycle hook names and response-scope checks |

MVP builtin planners live in `pkg/networkplugins` and execute through ordinary
Go calls. All plugin families, including OVS, accelerated bridge and the IPAM/address planners,
use this boundary. `pkg/topology/plan.go` binds inputs and validates results. CRD effect
templates remain declarative framework compilation; they cannot become new
physical authority. Native execution and recorded compensation stay behind the
framework's grant and journal checks.

GA moves **all plugin implementations**, including OVS, accelerated bridge and the IPAM/address planners,
outside the DRA process behind gRPC. The same request/result contract is used;
changing transport does not transfer DRA publication, admission, journal or
allocation authority to a plugin. A remote result still passes scope, schema,
descriptor, budget and live ownership checks before execution. The GA adapter
and plugin services are future work; MVP does not add gRPC scaffolding.

The four hooks are `NodePrepare`, `ConnectNetwork`, `RemoveNetwork` and
`NodeUnprepare`. Cleanup identifies the framework-owned original execution and
original hook; fresh deletion targets are forbidden. Normal removal, failed
connect compensation and preparation compensation invoke this same contract.
The framework uses the saved revision and checks the attachment's saved claim,
allocation fingerprint and revision identity before restoring recorded effects
and before-images. Deleting or editing a live CRD does not select new cleanup
targets. Cleanup checks the saved contract without invoking prepare/connect
planning again. A dependency test prevents either plugin package from importing DRA,
Kubernetes, netlink, runtime or journal code. The OVSDB client belongs only to
the framework's native executor. A JSON round-trip check covers all four hooks
for every builtin family.

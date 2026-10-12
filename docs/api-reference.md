# API reference

Project API group: `networking.dra-multi-network.schseba.github.io/v1alpha1`.
Driver name: `dra-multi-network.schseba.github.io`.

The five CRDs below use standard Kubernetes metadata. This reference lists their
spec/status fields; generated [CRD schemas](../config/crd/bases) are authoritative
for defaults, bounds, immutability and CEL rules. `TopologyRootConfiguration` is
opaque native DeviceClass configuration, not a sixth CRD. See
[resource translation](admin/resource-translation.md) for generated native objects.

## CRD scope and ownership

| Kind | Scope | Writer |
|---|---|---|
| NetworkPlugin | Cluster | Administrator; controller owns status |
| NetworkTopology | Cluster | Administrator; controller owns status |
| DeviceExposurePolicy | Cluster | Administrator; node agent reports eswitch status |
| NetworkTopologyRevision | Cluster | Controller; immutable locked execution contract |
| NetworkAttachmentStatus | Namespace | Node agent; users can observe outputs/errors |

## DeviceExposurePolicy

| Field | Type | Required / constraints |
|---|---|---|
| `spec` | object | required |
| `spec.action` | string | required; one of `Expose`, `Exclude`, `ManageEswitch` |
| `spec.exposure` | object | optional |
| `spec.exposure.allowMultipleAllocations` | boolean | optional |
| `spec.exposure.allowedDrivers` | array | optional |
| `spec.exposure.allowedDrivers[]` | string | optional |
| `spec.exposure.allowedHostBridges` | array | maxItems=8; Approved existing Linux bridges. The administrator alias is an identity fence; a bridge name alone is not authority to attach a representor. |
| `spec.exposure.allowedHostBridges[]` | object | optional |
| `spec.exposure.allowedHostBridges[].alias` | string | required; minLength=1; maxLength=128 |
| `spec.exposure.allowedHostBridges[].name` | string | required; pattern=^[A-Za-z0-9_.-]{1,15}$ |
| `spec.exposure.allowedHostBridges[].uplinks` | array | maxItems=8 |
| `spec.exposure.allowedHostBridges[].uplinks[]` | object | optional |
| `spec.exposure.allowedHostBridges[].uplinks[].alias` | string | required; minLength=1; maxLength=128 |
| `spec.exposure.allowedHostBridges[].uplinks[].name` | string | required; pattern=^[A-Za-z0-9_.-]{1,15}$ |
| `spec.exposure.allowedHostBridges[].uplinks[].pciAddress` | string | required; pattern=^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$ |
| `spec.exposure.allowedHostBridges[].uplinks[].vlans` | array | required; maxItems=4094 |
| `spec.exposure.allowedHostBridges[].uplinks[].vlans[]` | integer | minimum=1; maximum=4094 |
| `spec.exposure.allowedHostBridges[].vlans` | array | required; maxItems=4094 |
| `spec.exposure.allowedHostBridges[].vlans[]` | integer | minimum=1; maximum=4094 |
| `spec.exposure.allowedMADAccess` | boolean | optional |
| `spec.exposure.allowedOVSBridges` | array | maxItems=8; Approved existing OVS bridges. UUID and administrator external ID fence replacement and foreign dataplanes; only explicitly granted VLANs apply. |
| `spec.exposure.allowedOVSBridges[]` | object | optional |
| `spec.exposure.allowedOVSBridges[].identity` | string | required; minLength=1; maxLength=128 |
| `spec.exposure.allowedOVSBridges[].name` | string | required; pattern=^[A-Za-z0-9_.-]{1,15}$ |
| `spec.exposure.allowedOVSBridges[].uuid` | string | required; pattern=^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ |
| `spec.exposure.allowedOVSBridges[].vlans` | array | required; maxItems=4094 |
| `spec.exposure.allowedOVSBridges[].vlans[]` | integer | minimum=1; maximum=4094 |
| `spec.exposure.allowedRDMAProfiles` | array | maxItems=1 |
| `spec.exposure.allowedRDMAProfiles[]` | string | one of `roce-single-port-v1`, `ib-verbs-single-port-v1`, `ib-ipoib-single-port-v1` |
| `spec.exposure.allowedResetMethods` | array | maxItems=1 |
| `spec.exposure.allowedResetMethods[]` | string | one of `flr` |
| `spec.exposure.allowedVFSettings` | array | maxItems=9 |
| `spec.exposure.allowedVFSettings[]` | string | one of `mac`, `trust`, `spoofCheck`, `linkState`, `vlanId`, `vlanQoS`, `vlanProtocol`, `minTxRate`, `maxTxRate` |
| `spec.exposure.capacity` | object | optional |
| `spec.exposure.capacity.<key>` | object | optional |
| `spec.exposure.capacity.<key>.default` | string | required |
| `spec.exposure.capacity.<key>.validValues` | array | required |
| `spec.exposure.capacity.<key>.validValues[]` | string | optional |
| `spec.exposure.capacity.<key>.value` | string | required |
| `spec.exposure.compatibility` | array | maxItems=2 |
| `spec.exposure.compatibility[]` | object | optional |
| `spec.exposure.compatibility[].domain` | string | required |
| `spec.exposure.compatibility[].groups` | array | required |
| `spec.exposure.compatibility[].groups[]` | string | optional |
| `spec.exposure.compatibility[].scope` | string | required |
| `spec.exposure.effectPermissions` | array | required |
| `spec.exposure.effectPermissions[]` | string | optional |
| `spec.exposure.mode` | string | required |
| `spec.exposure.persona` | string | required |
| `spec.exposure.plugin` | string | required |
| `spec.management` | object | EswitchManagement is an administrator's persistent desired mode. It is not a workload allocation or a grant to change a mode while consumers exist. |
| `spec.management.allowedModes` | array | required; maxItems=2 |
| `spec.management.allowedModes[]` | string | one of `legacy`, `switchdev` |
| `spec.management.effectPermissions` | array | required; maxItems=1 |
| `spec.management.effectPermissions[]` | string | one of `linux/eswitch-configure/v1` |
| `spec.management.mode` | string | required; one of `legacy`, `switchdev` |
| `spec.management.profile` | string | required; one of `ethernet-pf-vfs-v1` |
| `spec.nodeSelector` | object | A label selector is a label query over a set of resources. The result of matchLabels and matchExpressions are ANDed. An empty label selector matches all objects. A null label selector matches no objects. |
| `spec.nodeSelector.matchExpressions` | array | matchExpressions is a list of label selector requirements. The requirements are ANDed. |
| `spec.nodeSelector.matchExpressions[]` | object | A label selector requirement is a selector that contains values, a key, and an operator that relates the key and values. |
| `spec.nodeSelector.matchExpressions[].key` | string | required; key is the label key that the selector applies to. |
| `spec.nodeSelector.matchExpressions[].operator` | string | required; operator represents a key's relationship to a set of values. Valid operators are In, NotIn, Exists and DoesNotExist. |
| `spec.nodeSelector.matchExpressions[].values` | array | values is an array of string values. If the operator is In or NotIn, the values array must be non-empty. If the operator is Exists or DoesNotExist, the values array must be empty. This array is replaced during a strategic merge patch. |
| `spec.nodeSelector.matchExpressions[].values[]` | string | optional |
| `spec.nodeSelector.matchLabels` | object | matchLabels is a map of {key,value} pairs. A single {key,value} in the matchLabels map is equivalent to an element of matchExpressions, whose key field is "key", the operator is "In", and the values array contains only "value". The requirements are ANDed. |
| `spec.priority` | integer | optional |
| `spec.selector` | object | required |
| `spec.selector.driver` | string | optional |
| `spec.selector.kind` | string | required; one of `PF`, `VF`, `PCI` |
| `spec.selector.linkType` | string | optional |
| `spec.selector.parentPCIAddresses` | array | optional |
| `spec.selector.parentPCIAddresses[]` | string | optional |
| `spec.selector.pciAddresses` | array | maxItems=128 |
| `spec.selector.pciAddresses[]` | string | optional |
| `spec.selector.vendor` | string | optional |
| `status` | object | optional |
| `status.conditions` | array | optional |
| `status.conditions[]` | Kubernetes Condition | type, status, reason, message, observedGeneration, lastTransitionTime |
| `status.eswitchNodes` | array | optional |
| `status.eswitchNodes[]` | object | optional |
| `status.eswitchNodes[].message` | string | optional |
| `status.eswitchNodes[].mode` | string | optional |
| `status.eswitchNodes[].node` | string | required |
| `status.eswitchNodes[].observedGeneration` | integer | required |
| `status.eswitchNodes[].pci` | string | required |
| `status.eswitchNodes[].phase` | string | required |
| `status.observedGeneration` | integer | optional |

## NetworkAttachmentStatus

| Field | Type | Required / constraints |
|---|---|---|
| `spec` | object | required; attachment identity is immutable |
| `spec.bootID` | string | required |
| `spec.claimUID` | string | required |
| `spec.fingerprint` | string | required |
| `spec.nodeName` | string | required |
| `spec.nodeUID` | string | required |
| `spec.podUID` | string | required |
| `spec.revisionRef` | object | required |
| `spec.revisionRef.digest` | string | required |
| `spec.revisionRef.name` | string | required |
| `spec.revisionRef.uid` | string | required |
| `spec.sandboxID` | string | required |
| `status` | object | optional |
| `status.conditions` | array | optional |
| `status.conditions[]` | Kubernetes Condition | type, status, reason, message, observedGeneration, lastTransitionTime |
| `status.lastError` | string | optional |
| `status.outputs` | array | optional |
| `status.outputs[]` | object | optional |
| `status.outputs[].ips` | array of CIDR strings | optional; assigned address observations |
| `status.outputs[].ifName` | string | optional |
| `status.outputs[].pciAddress` | string | optional |
| `status.outputs[].port` | string | required |
| `status.outputs[].rdmaDevice` | string | optional |
| `status.outputs[].shareID` | string | optional |
| `status.outputs[].step` | string | required |
| `status.outputs[].type` | string | required |
| `status.phase` | string | optional |

## NetworkPlugin

| Field | Type | Required / constraints |
|---|---|---|
| `spec` | object | required |
| `spec.allocationProfileRef` | object | Programmable roots retain a certified allocation/ownership contract. |
| `spec.allocationProfileRef.digest` | string | required; pattern=^[0-9a-f]{64}$ |
| `spec.allocationProfileRef.name` | string | required |
| `spec.allowedEffects` | array | required |
| `spec.allowedEffects[]` | string | optional |
| `spec.allocationDriver` | string | optional; only the certified IPAM builtin may set `ipam.schseba.github.io`; omission uses the network driver |
| `spec.configSchema` | JSON | required; preserved JSON; framework validates contract |
| `spec.hooks` | object | preserved JSON; framework validates contract |
| `spec.implementation` | object | required |
| `spec.implementation.name` | string | optional |
| `spec.implementation.type` | string | required; one of `Builtin`, `EffectProgram` |
| `spec.implementation.version` | string | required |
| `spec.inputs` | object | optional |
| `spec.inputs.<key>` | object | optional |
| `spec.inputs.<key>.access` | string | optional |
| `spec.inputs.<key>.linkType` | string | optional |
| `spec.inputs.<key>.maxItems` | integer | required |
| `spec.inputs.<key>.minItems` | integer | required |
| `spec.inputs.<key>.ownership` | string | optional |
| `spec.inputs.<key>.type` | string | required |
| `spec.kind` | string | required; one of `Allocation`, `Operation` |
| `spec.outputs` | object | required |
| `spec.outputs.<key>` | object | optional |
| `spec.outputs.<key>.access` | string | optional |
| `spec.outputs.<key>.linkType` | string | optional |
| `spec.outputs.<key>.maxItems` | integer | required |
| `spec.outputs.<key>.minItems` | integer | required |
| `spec.outputs.<key>.ownership` | string | optional |
| `spec.outputs.<key>.type` | string | required |
| `spec.stateContract` | object | required |
| `spec.stateContract.cleanup` | string | required |
| `spec.stateContract.version` | string | required |
| `status` | object | optional |
| `status.conditions` | array | optional |
| `status.conditions[]` | Kubernetes Condition | type, status, reason, message, observedGeneration, lastTransitionTime |
| `status.digest` | string | optional |
| `status.observedGeneration` | integer | optional |

## NetworkTopology

| Field | Type | Required / constraints |
|---|---|---|
| `spec` | object | required |
| `spec.effectBudget` | object | required |
| `spec.effectBudget.allow` | array | required; maxItems=32 |
| `spec.effectBudget.allow[]` | string | optional |
| `spec.steps` | array | required; minItems=1; maxItems=32 |
| `spec.steps[]` | object | optional |
| `spec.steps[].config` | JSON | required; preserved JSON; framework validates contract |
| `spec.steps[].id` | string | required; pattern=^[a-z][a-z0-9-]{0,39}$ |
| `spec.steps[].inputs` | object | preserved JSON; framework validates contract; Inputs contain a reference object or an array of reference objects per port. |
| `spec.steps[].kind` | string | required; one of `Allocation`, `Operation` |
| `spec.steps[].pluginRef` | object | required |
| `spec.steps[].pluginRef.name` | string | required |
| `spec.steps[].selectors` | array | maxItems=8; Administrator CEL selectors are intersected with mandatory persona selectors. |
| `spec.steps[].selectors[]` | string | optional |
| `status` | object | optional |
| `status.conditions` | array | optional |
| `status.conditions[]` | Kubernetes Condition | type, status, reason, message, observedGeneration, lastTransitionTime |
| `status.observedGeneration` | integer | optional |
| `status.revisionRef` | object | optional |
| `status.revisionRef.digest` | string | required |
| `status.revisionRef.name` | string | required |
| `status.revisionRef.uid` | string | required |
| `status.rootClasses` | array | optional |
| `status.rootClasses[]` | object | optional |
| `status.rootClasses[].className` | string | required |
| `status.rootClasses[].rootStep` | string | required |

## NetworkTopologyRevision

| Field | Type | Required / constraints |
|---|---|---|
| `spec` | object | required; revision spec is immutable |
| `spec.compilerVersion` | string | required |
| `spec.digest` | string | required |
| `spec.order` | array | required |
| `spec.order[]` | string | optional |
| `spec.plugins` | array | required; maxItems=32 |
| `spec.plugins[]` | object | optional |
| `spec.plugins[].digest` | string | required |
| `spec.plugins[].name` | string | required |
| `spec.plugins[].spec` | object | required |
| `spec.plugins[].spec.allocationProfileRef` | object | Programmable roots retain a certified allocation/ownership contract. |
| `spec.plugins[].spec.allocationProfileRef.digest` | string | required; pattern=^[0-9a-f]{64}$ |
| `spec.plugins[].spec.allocationProfileRef.name` | string | required |
| `spec.plugins[].spec.allowedEffects` | array | required |
| `spec.plugins[].spec.allowedEffects[]` | string | optional |
| `spec.plugins[].spec.allocationDriver` | string | optional; locked allocating-driver contract |
| `spec.plugins[].spec.configSchema` | JSON | required; preserved JSON; framework validates contract |
| `spec.plugins[].spec.hooks` | object | preserved JSON; framework validates contract |
| `spec.plugins[].spec.implementation` | object | required |
| `spec.plugins[].spec.implementation.name` | string | optional |
| `spec.plugins[].spec.implementation.type` | string | required; one of `Builtin`, `EffectProgram` |
| `spec.plugins[].spec.implementation.version` | string | required |
| `spec.plugins[].spec.inputs` | object | optional |
| `spec.plugins[].spec.inputs.<key>` | object | optional |
| `spec.plugins[].spec.inputs.<key>.access` | string | optional |
| `spec.plugins[].spec.inputs.<key>.linkType` | string | optional |
| `spec.plugins[].spec.inputs.<key>.maxItems` | integer | required |
| `spec.plugins[].spec.inputs.<key>.minItems` | integer | required |
| `spec.plugins[].spec.inputs.<key>.ownership` | string | optional |
| `spec.plugins[].spec.inputs.<key>.type` | string | required |
| `spec.plugins[].spec.kind` | string | required; one of `Allocation`, `Operation` |
| `spec.plugins[].spec.outputs` | object | required |
| `spec.plugins[].spec.outputs.<key>` | object | optional |
| `spec.plugins[].spec.outputs.<key>.access` | string | optional |
| `spec.plugins[].spec.outputs.<key>.linkType` | string | optional |
| `spec.plugins[].spec.outputs.<key>.maxItems` | integer | required |
| `spec.plugins[].spec.outputs.<key>.minItems` | integer | required |
| `spec.plugins[].spec.outputs.<key>.ownership` | string | optional |
| `spec.plugins[].spec.outputs.<key>.type` | string | required |
| `spec.plugins[].spec.stateContract` | object | required |
| `spec.plugins[].spec.stateContract.cleanup` | string | required |
| `spec.plugins[].spec.stateContract.version` | string | required |
| `spec.sourceUID` | string | required |
| `spec.topology` | object | required |
| `spec.topology.effectBudget` | object | required |
| `spec.topology.effectBudget.allow` | array | required; maxItems=32 |
| `spec.topology.effectBudget.allow[]` | string | optional |
| `spec.topology.steps` | array | required; minItems=1; maxItems=32 |
| `spec.topology.steps[]` | object | optional |
| `spec.topology.steps[].config` | JSON | required; preserved JSON; framework validates contract |
| `spec.topology.steps[].id` | string | required; pattern=^[a-z][a-z0-9-]{0,39}$ |
| `spec.topology.steps[].inputs` | object | preserved JSON; framework validates contract; Inputs contain a reference object or an array of reference objects per port. |
| `spec.topology.steps[].kind` | string | required; one of `Allocation`, `Operation` |
| `spec.topology.steps[].pluginRef` | object | required |
| `spec.topology.steps[].pluginRef.name` | string | required |
| `spec.topology.steps[].selectors` | array | maxItems=8; Administrator CEL selectors are intersected with mandatory persona selectors. |
| `spec.topology.steps[].selectors[]` | string | optional |
| `status` | object | optional |
| `status.conditions` | array | optional |
| `status.conditions[]` | Kubernetes Condition | type, status, reason, message, observedGeneration, lastTransitionTime |
| `status.observedGeneration` | integer | optional |
| `status.revisionRef` | object | optional |
| `status.revisionRef.digest` | string | required |
| `status.revisionRef.name` | string | required |
| `status.revisionRef.uid` | string | required |
| `status.rootClasses` | array | optional |
| `status.rootClasses[]` | object | optional |
| `status.rootClasses[].className` | string | required |
| `status.rootClasses[].rootStep` | string | required |

## Canonical builtin profiles

These are descriptors, not separate executable plugins. Profiles with a shared
implementation name run the same internal planner with a locked mode contract.
The following configuration/ports are taken from the executable registry at the
first-push preparation. After changing that registry, update this reference.
For the complete schema including nested constraints, inspect a seeded object
with `kubectl get networkplugin NAME -o yaml`.

### `host-cni`

Kind: `Allocation`; implementation: `host-cni`; version: `v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["kernel"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[false] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/link-move/v1`, `linux/link-configure/v1`.

### `sriov-cni`

Kind: `Allocation`; implementation: `sriov-cni`; version: `v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["kernel"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[false] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/link-move/v1`, `linux/link-configure/v1`.

### `bond-cni`

Kind: `Operation`; implementation: `bond-cni`; version: `v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `failOverMac` | yes | string; enum=["active"] |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `mode` | yes | string; enum=["active-backup"] |
| `monitorIntervalMs` | yes | integer; minimum=1; maximum=10000 |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| inputs | `members` | `KernelInterface/v1` / ethernet | 2–8 | ExclusiveMaster |
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/create-bond/v1`, `linux/attach-master/v1`, `linux/link-configure/v1`.

### `vlan-cni`

Kind: `Operation`; implementation: `vlan-cni`; version: `v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `up` | yes | boolean |
| `vlanId` | yes | integer; minimum=1; maximum=4094 |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| inputs | `parent` | `KernelInterface/v1` / ethernet | 1–1 | CreateChild |
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/create-vlan/v1`, `linux/link-configure/v1`.

### `macvlan-cni`

Kind: `Allocation`; implementation: `macvlan-cni`; version: `v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["bridge"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Created |

Allowed effects: `linux/create-macvlan/v1`, `linux/link-configure/v1`.

### `ipvlan-cni`

Kind: `Allocation`; implementation: `ipvlan-cni`; version: `v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["l2"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Created |

Allowed effects: `linux/create-ipvlan/v1`, `linux/link-configure/v1`.

### `host-cni-vfio`

Kind: `Allocation`; implementation: `host-cni`; version: `vfio-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["cdi"] |
| `driver` | yes | string; enum=["vfio-pci"] |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["vfio"] |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `vfio` | `VFIODevice/v1` / — | 1–1 | Exclusive |

Allowed effects: `linux/driver-bind/v1`, `runtime/device-access/v1`.

### `sriov-cni-vfio`

Kind: `Allocation`; implementation: `sriov-cni`; version: `vfio-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["cdi"] |
| `driver` | yes | string; enum=["vfio-pci"] |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["vfio"] |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `vfio` | `VFIODevice/v1` / — | 1–1 | Exclusive |

Allowed effects: `linux/driver-bind/v1`, `runtime/device-access/v1`.

### `sriov-cni-configured`

Kind: `Allocation`; implementation: `sriov-cni`; version: `vf-config-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["kernel"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[false] |
| `up` | yes | boolean |
| `vf` | yes | object; minProperties=1; additionalProperties=false |
| `vf.linkState` | no | string; enum=["auto","enable","disable"] |
| `vf.mac` | no | string; pattern="^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$" |
| `vf.maxTxRate` | no | integer; minimum=0; maximum=1000000 |
| `vf.minTxRate` | no | integer; minimum=0; maximum=1000000 |
| `vf.spoofCheck` | no | boolean |
| `vf.trust` | no | boolean |
| `vf.vlanId` | no | integer; minimum=0; maximum=4094 |
| `vf.vlanProtocol` | no | string; enum=["802.1Q","802.1ad"] |
| `vf.vlanQoS` | no | integer; minimum=0; maximum=7 |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/vf-configure/v1`, `linux/link-move/v1`, `linux/link-configure/v1`.

### `sriov-cni-vfio-configured`

Kind: `Allocation`; implementation: `sriov-cni`; version: `vf-config-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["cdi"] |
| `driver` | yes | string; enum=["vfio-pci"] |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["vfio"] |
| `vf` | yes | object; minProperties=1; additionalProperties=false |
| `vf.linkState` | no | string; enum=["auto","enable","disable"] |
| `vf.mac` | no | string; pattern="^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$" |
| `vf.maxTxRate` | no | integer; minimum=0; maximum=1000000 |
| `vf.minTxRate` | no | integer; minimum=0; maximum=1000000 |
| `vf.spoofCheck` | no | boolean |
| `vf.trust` | no | boolean |
| `vf.vlanId` | no | integer; minimum=0; maximum=4094 |
| `vf.vlanProtocol` | no | string; enum=["802.1Q","802.1ad"] |
| `vf.vlanQoS` | no | integer; minimum=0; maximum=7 |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `vfio` | `VFIODevice/v1` / — | 1–1 | Exclusive |

Allowed effects: `linux/vf-configure/v1`, `linux/driver-bind/v1`, `runtime/device-access/v1`.

### `host-cni-rebind`

Kind: `Allocation`; implementation: `host-cni`; version: `kernel-bind-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `driver` | yes | string; pattern="^[A-Za-z0-9_-]{1,64}$" |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["kernel"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[false] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/driver-bind/v1`, `linux/link-move/v1`, `linux/link-configure/v1`.

### `sriov-cni-rebind`

Kind: `Allocation`; implementation: `sriov-cni`; version: `kernel-bind-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `driver` | yes | string; pattern="^[A-Za-z0-9_-]{1,64}$" |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["kernel"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[false] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/driver-bind/v1`, `linux/link-move/v1`, `linux/link-configure/v1`.

### `host-cni-reset`

Kind: `Allocation`; implementation: `host-cni`; version: `kernel-reset-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `driver` | yes | string; pattern="^[A-Za-z0-9_-]{1,64}$" |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["kernel"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[false] |
| `resetMethod` | yes | string; enum=["flr"] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/pci-reset/v1`, `linux/driver-bind/v1`, `linux/link-move/v1`, `linux/link-configure/v1`.

### `sriov-cni-reset`

Kind: `Allocation`; implementation: `sriov-cni`; version: `kernel-reset-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `driver` | yes | string; pattern="^[A-Za-z0-9_-]{1,64}$" |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["kernel"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[false] |
| `resetMethod` | yes | string; enum=["flr"] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/pci-reset/v1`, `linux/driver-bind/v1`, `linux/link-move/v1`, `linux/link-configure/v1`.

### `host-cni-roce`

Kind: `Allocation`; implementation: `host-cni`; version: `roce-single-port-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["nri"] |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["rdma"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[true] |
| `rdmaProfile` | yes | string; enum=["roce-single-port-v1"] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `rdma` | `RDMADevice/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/rdma-namespace/v1`, `runtime/device-access/v1`.

### `sriov-cni-roce`

Kind: `Allocation`; implementation: `sriov-cni`; version: `roce-single-port-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["nri"] |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["rdma"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[true] |
| `rdmaProfile` | yes | string; enum=["roce-single-port-v1"] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `rdma` | `RDMADevice/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/rdma-namespace/v1`, `runtime/device-access/v1`.

### `sriov-cni-switchdev`

Kind: `Allocation`; implementation: `sriov-cni`; version: `switchdev-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["kernel"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[false] |
| `representor` | yes | boolean; enum=[true] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / ethernet | 1–1 | Exclusive |
| outputs | `representor` | `HostRepresentor/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/representor-map/v1`, `linux/link-move/v1`, `linux/link-configure/v1`.

### `sriov-cni-vfio-switchdev`

Kind: `Allocation`; implementation: `sriov-cni`; version: `switchdev-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["cdi"] |
| `driver` | yes | string; enum=["vfio-pci"] |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["vfio"] |
| `representor` | yes | boolean; enum=[true] |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `representor` | `HostRepresentor/v1` / ethernet | 1–1 | Exclusive |
| outputs | `vfio` | `VFIODevice/v1` / — | 1–1 | Exclusive |

Allowed effects: `linux/representor-map/v1`, `linux/driver-bind/v1`, `runtime/device-access/v1`.

### `sriov-cni-roce-switchdev`

Kind: `Allocation`; implementation: `sriov-cni`; version: `roce-single-port-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["nri"] |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["ethernet"] |
| `mode` | yes | string; enum=["rdma"] |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `rdmaAccess` | yes | boolean; enum=[true] |
| `rdmaProfile` | yes | string; enum=["roce-single-port-v1"] |
| `representor` | yes | boolean; enum=[true] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `rdma` | `RDMADevice/v1` / ethernet | 1–1 | Exclusive |
| outputs | `representor` | `HostRepresentor/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/representor-map/v1`, `linux/rdma-namespace/v1`, `runtime/device-access/v1`.

### `accelerated-bridge-cni`

Kind: `Operation`; implementation: `accelerated-bridge-cni`; version: `v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `bridge` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `trunk` | no | array |
| `up` | yes | boolean |
| `vlanId` | yes | integer; minimum=0; maximum=4094 |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| inputs | `representor` | `HostRepresentor/v1` / ethernet | 1–1 | ExclusiveMaster |
| outputs | `representor` | `HostRepresentor/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/representor-bridge/v1`.

### `ovs-cni`

Kind: `Operation`; implementation: `ovs-cni`; version: `v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `bridge` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `trunk` | no | array |
| `up` | yes | boolean |
| `vlanId` | yes | integer; minimum=0; maximum=4094 |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| inputs | `representor` | `HostRepresentor/v1` / ethernet | 1–1 | ExclusiveMaster |
| outputs | `representor` | `HostRepresentor/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `ovs/representor-port/v1`.

### `accelerated-bridge-cni-uplink`

Kind: `Operation`; implementation: `accelerated-bridge-cni`; version: `v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `bridge` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `mtu` | yes | integer; minimum=576; maximum=9000 |
| `trunk` | no | array |
| `up` | yes | boolean |
| `uplink` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `vlanId` | yes | integer; minimum=0; maximum=4094 |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| inputs | `representor` | `HostRepresentor/v1` / ethernet | 1–1 | ExclusiveMaster |
| outputs | `representor` | `HostRepresentor/v1` / ethernet | 1–1 | Exclusive |

Allowed effects: `linux/shared-uplink-vlan/v1`, `linux/representor-bridge/v1`.

### `host-cni-ib-verbs`

Kind: `Allocation`; implementation: `host-cni`; version: `ib-verbs-single-port-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["nri"] |
| `linkType` | yes | string; enum=["infiniband"] |
| `madAccess` | no | boolean |
| `mode` | yes | string; enum=["rdma"] |
| `rdmaAccess` | yes | boolean; enum=[true] |
| `rdmaProfile` | yes | string; enum=["ib-verbs-single-port-v1"] |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `rdma` | `RDMADevice/v1` / infiniband | 1–1 | Exclusive |

Allowed effects: `linux/rdma-namespace/v1`, `runtime/device-access/v1`.

### `host-cni-ipoib`

Kind: `Allocation`; implementation: `host-cni`; version: `ib-ipoib-single-port-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["nri"] |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["infiniband"] |
| `madAccess` | no | boolean |
| `mode` | yes | string; enum=["rdma"] |
| `mtu` | yes | integer; minimum=256; maximum=65520 |
| `rdmaAccess` | yes | boolean; enum=[true] |
| `rdmaProfile` | yes | string; enum=["ib-ipoib-single-port-v1"] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / infiniband | 1–1 | Exclusive |
| outputs | `rdma` | `RDMADevice/v1` / infiniband | 1–1 | Exclusive |

Allowed effects: `linux/rdma-namespace/v1`, `runtime/device-access/v1`.

### `sriov-cni-ib-verbs`

Kind: `Allocation`; implementation: `sriov-cni`; version: `ib-verbs-single-port-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["nri"] |
| `linkType` | yes | string; enum=["infiniband"] |
| `madAccess` | no | boolean |
| `mode` | yes | string; enum=["rdma"] |
| `rdmaAccess` | yes | boolean; enum=[true] |
| `rdmaProfile` | yes | string; enum=["ib-verbs-single-port-v1"] |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `rdma` | `RDMADevice/v1` / infiniband | 1–1 | Exclusive |

Allowed effects: `linux/rdma-namespace/v1`, `runtime/device-access/v1`.

### `sriov-cni-ipoib`

Kind: `Allocation`; implementation: `sriov-cni`; version: `ib-ipoib-single-port-v1`.

| Config key | Required | Type / constraints |
|---|---|---|
| `accessBackend` | yes | string; enum=["nri"] |
| `ifName` | yes | string; pattern="^[A-Za-z0-9_.-]{1,15}$" |
| `linkType` | yes | string; enum=["infiniband"] |
| `madAccess` | no | boolean |
| `mode` | yes | string; enum=["rdma"] |
| `mtu` | yes | integer; minimum=256; maximum=65520 |
| `rdmaAccess` | yes | boolean; enum=[true] |
| `rdmaProfile` | yes | string; enum=["ib-ipoib-single-port-v1"] |
| `up` | yes | boolean |

Ports:

| Direction | Name | Type / link | Cardinality | Access / ownership |
|---|---|---|---|---|
| outputs | `interface` | `KernelInterface/v1` / infiniband | 1–1 | Exclusive |
| outputs | `rdma` | `RDMADevice/v1` / infiniband | 1–1 | Exclusive |

Allowed effects: `linux/rdma-namespace/v1`, `runtime/device-access/v1`.

## Effect and SDK contract

EffectProgram version is `effects.dra-multi-network.schseba.github.io/v1alpha1`.
Effects carry an ID, kind, typed input references, parameters/bindings, explicit
requirements/postconditions and a supported recovery strategy. Cleanup hooks
restore their recorded prepare/connect execution. Custom allocation roots lock
`allocationProfileRef.name` and `digest` to a canonical descriptor.

Supported native effect kinds are the union of each descriptor's allowed effects
above plus policy-managed `linux/eswitch-configure/v1`. Native effect parameters
remain opcode-specific, closed and schema-checked. Runnable complete templates
are in [effect-program](../examples/effect-program/plugin.yaml),
[link-config](../examples/link-config/plugin.yaml) and
[root-effect-program](../examples/root-effect-program/plugin.yaml).
See [NetworkPlugin configuration](admin/networkplugins.md) and the
[SDK](../pkg/pluginapi/README.md) for lifecycle and framework boundaries.

## IPAM and address profiles

`ipam` is an Allocation plugin with config `{network, subnet}` and output
`lease: AddressLease/v2`. It has no Linux effects and sets `allocationDriver` to
`ipam.schseba.github.io`. Each claim request must consume exactly one
`ipam.schseba.github.io/addresses` unit from a shared subnet device.

`address-cni` is an Operation with empty config, scalar inputs
`interface: KernelInterface/v1` (Ethernet) and `lease: AddressLease/v2`, and
output `configured: KernelInterface/v1`. Its effect is
`linux/address-configure/v1`, requiring a private sandbox interface and the
framework's granted lease. Each lease may have one address consumer in a topology.
The [IPAM design](ipam-plan.md) documents pool configuration and backend limits.

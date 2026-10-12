# Operations and troubleshooting

## Trust and access

Keep NetworkPlugin, NetworkTopology and DeviceExposurePolicy writes restricted
to cluster administrators. NetworkTopologyRevision and managed DeviceClass
content are controller-owned. NetworkAttachmentStatus and node capability
reports are agent-owned. Namespace users need Pod and namespaced claim/template
permissions plus observation of their attachment status; they do not need write
access to cluster-scoped configuration. Use ordinary RBAC for these boundaries.
The chart installs service-account roles; it does not create tenant Roles.

Admission validates immutable contracts, ownership identities, full root coverage,
claim bindings and protected runtime/capability metadata. Mutation adds required
NRI enforcement and revision-specific placement. Webhooks fail closed; keep the
controller/admission service available before creating new project workloads.
Node-bound service-account token identity fences capability reporting to the
actual node/Pod. A driver service-account name alone cannot impersonate any node.

## Inspect the full chain

```bash
kubectl -n dra-multi-network get deploy,ds,pods,services
kubectl get networkplugins
kubectl get networktopologies,networktopologyrevisions
kubectl get deviceexposurepolicies -o yaml
kubectl get deviceclasses
kubectl get resourceslices
kubectl -n multi-network-example get pods,resourceclaims,networkattachmentstatuses
kubectl -n multi-network-example describe pod network-mvp
```

Check `observedGeneration` before relying on `Ready` or `Accepted` after edits.
Topology Ready means revision/classes were verified, not hardware availability.
Node readiness and per-revision capability labels gate scheduling. Generic
DeviceExposurePolicy conditions are not a substitute for inspecting actual
published personas; eswitch management has per-node status.

Attachment status objects can remain after a Pod is removed, with phase
`Removed` and a historical `lastError` from an earlier attempt. Inspect the
records for the current Pod UID before treating all historical errors as live
failures. The journal, claim and current phase determine retained ownership.

```bash
pod_uid=$(kubectl -n multi-network-example get pod network-mvp -o jsonpath='{.metadata.uid}')
kubectl -n multi-network-example get networkattachmentstatuses -o json | \
  jq --arg uid "$pod_uid" '.items[] | select(.spec.podUID == $uid) | .status'
```

Use resource names returned by `get deploy,ds` for logs. Default chart names:

```bash
kubectl -n dra-multi-network logs deployment/dra-multi-network-controller --tail=100
kubectl -n dra-multi-network logs daemonset/dra-multi-network-kubeletplugin --tail=100
```

The persistent host journal is under `/var/lib/dra-multi-network`; CDI specs
live under the configured CDI root. Preserve both while diagnosing failures.
Never edit a saved identity/before-image to make cleanup pass.

## Common failures

| Symptom | Check |
|---|---|
| `Ready=False` topology | Condition message; unknown config key, typed ports, cycle, duplicate output name, unsupported profile or missing effect budget |
| Claim/Pod Pending | Actual class selected, matching slices, VF count, shared child capacity, compatibility conflicts, node readiness/revision affinity |
| No advertised devices | Exposure policy validity/selector/priority, protected management state, unsupported IOMMU/RDMA/representor metadata, eswitch drain |
| Prepare fails | Class-origin revision identity, every root present once, current policy grants, hardware identity, retained ownership |
| Sandbox/connect fails | NRI socket/configuration, link name/state/MTU/master, plugin effect binding, policy intersection, existing foreign state |
| Device absent in one container | Its `resources.claims` and request consumption; scoped CDI/NRI access; actual device identity |
| Cleanup/management remains blocked | Attachment/claim still active, missing node acknowledgement, unexpected foreign state or retained before-image ownership |
| New Pods fail when agent absent | Required NRI enforcement should fail; inspect runtime/agent availability rather than adding tenant bypass |

Native macvlan and ipvlan use incompatible RX-handler families on one live parent.
Scheduling compatibility does not remove that Linux constraint. Use separate
parents for simultaneous mixed families; native rejection compensates the
failed child. Exclusive handoff on the same VF is blocked by DRA compatibility
and rechecked at runtime.

## Drain, retirement and uninstall

1. Stop creating consumers and delete/scale down the consuming workloads.
2. Delete manually owned claims after their Pods stop. Wait for attachment and
   DRA unprepare cleanup; inspect retained journal/status errors.
3. Delete unused source topologies/custom plugins. Retirement fences new use;
   claims and node ownership keep revisions/classes/descriptors retained until
   their saved contracts are no longer needed.
4. Wait for management policies to restore owned eswitch/shared state before
   deleting them or withdrawing their permissions.
5. Uninstall the chart only after all project consumers and hardware ownership
   drain. Preserve CRDs and host journals until you have confirmed restoration.

Revision garbage collection waits for node retirement acknowledgement, including
nodes that are unavailable. Force-removing finalizers can discard necessary
cleanup contracts. Driver upgrades/migration and host-reboot acceptance are
outside the current POC review scope; do not treat Helm rolling replacement as
certification of those cases. See [roadmap](roadmap.md).

On an unexpected agent/runtime restart, first verify NRI reconnection, claim
identity, boot ID and journal recovery. Same-boot restart has exercised paths;
complete reboot recovery and broader hardware restoration remain deferred.
If foreign state changed, repair the underlying ownership conflict through an
administrator-reviewed procedure before retrying cleanup.

## IPAM pool operations

Pools are immutable administrator ConfigMaps in the driver namespace, labeled
`ipam.schseba.github.io/pool: "true"`. The controller creates a backend-owned
ledger and pins its API UID in the pool annotation. Lease updates use Kubernetes
resource-version concurrency; the node journal retains attachment-holder intent.
Observe addresses through NetworkAttachmentStatus. Do not edit lease state or
remove ledger finalizers to force reuse. A missing/corrupt ledger or unresolved
orphan use fences new inventory.

Delete consuming Pods and claims, wait for verified cleanup, then delete the pool.
Active reservations and scheduler allocations block pool deletion; the controller
removes drained ledger state. A manually managed claim retains its allocation
after Pod deletion. Recreate a manually managed IPAM claim after unprepare;
the demo fences reuse of a released claim UID. The demo keeps replay tombstones until pool deletion, with a
240 KiB bound; continuous use requires safe GC or a larger transactional backend.
Existing attachments can continue during backend outages, while new connections
and release work wait for durable evidence. See [IPAM design](ipam-plan.md).

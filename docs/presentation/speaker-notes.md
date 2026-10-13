# DRA Multi-Network — Project Walkthrough speaker notes

Suggested timing: 15–20 minutes plus Q&A. Content reflects this checkout; recorded lab results were not rerun for the deck.

## 01. A network is more than an interface.

Opening: this project explores secondary Pod networking using Kubernetes Dynamic Resource Allocation and Node Resource Interface. The current POC owns native execution and recovery in the framework. This presentation also proposes a revised target with external gRPC plugins for both planning and native network execution; that revised execution boundary is not implemented. This is a POC and proposed architecture, not a production-ready release.

[Source](../../README.md)

## 02. Allocate the hardware. Describe the whole network.

A real secondary network can be a composition: two VFs, a bond, several VLAN interfaces, tuning, and optionally addresses. The scheduler needs to allocate scarce resources, while the node must realize the entire graph and clean it up safely. The primary CNI still provides the normal Pod network.

[Source](../../examples/bond-vlan/topology.yaml)

## 03. Two inputs. One allocated contract.

Read the diagram as two independent publications. The controller publishes what workloads request as DeviceClasses. Nodes publish what can be allocated as ResourceSlices. Kubernetes joins them through ResourceClaim allocation. Classes can exist without matching devices; slices can exist before a topology. NRI realizes the allocated contract in the sandbox.

[Source](../admin/resource-translation.md)

## 04. Five CRs. Clear ownership.

These are the five official CRDs in networking.dra-multi-network.schseba.github.io/v1alpha1. The first four are cluster scoped. NetworkAttachmentStatus is namespaced and is an observation of execution. DeviceClass, ResourceSlice, ResourceClaim and ResourceClaimTemplate are native Kubernetes objects, not additional project CRDs. The status object is not the allocation or ownership database.

[Source](../../pkg/api/networking/v1alpha1/types.go)

## 05. Expose personas. Protect physical ownership.

DeviceExposurePolicy supports Expose, Exclude and ManageEswitch. The node applies policy to discovered physical hardware, protects management links, and publishes approved personas. Counters and compatibility encode PF/VF and handoff/shared conflicts; alternate personas are not independent NICs. Effect permissions and profile-specific grants constrain actual mutations. Node-side live ownership checks remain necessary even after scheduler allocation.

[Source](../admin/exposure-policies.md)

## 06. Advertise alternatives. Grant mutations.

The left policy is the host-cni PF exposure from examples/shared/policies.yaml; the right is the sriov-cni VF exposure from examples/bond-vlan/policy.yaml. Both select the dedicated secondary PF family at 0000:29:00.0. PF pciAddresses selects that physical function; VF parentPCIAddresses selects its child VFs. persona kernel, plugin and mode determine the approved advertised profile. effectPermissions authorizes link movement/configuration, independently of descriptor effects and topology budget. These two policies can advertise alternatives at the same time; advertisement does not permit simultaneous exclusive PF/VF handoff. The driver derives compatibility domains, not the administrator YAML. Protected management hardware remains withheld. The PCI value is a lab example, and existing policies must be applied before workload allocations or a full drain as documented. Policies do not create VFs or manage sriov_numvfs.

[PF policy](../../examples/shared/policies.yaml) · [VF policy](../../examples/bond-vlan/policy.yaml)

## 07. A PF and its VFs are mutually exclusive.

Concrete current publication behavior in pkg/multinetwork/inventory.go: every persona consumes an interface-domain counter derived from its own PCI identity and a family-domain counter derived from its parent PF identity. Exclusive host PF handoff uses family group pf-handoff; child VF handoff uses host-retained. Across the shared family counter set those group lists have no intersection, so PF+VF co-allocation is incompatible in either allocation order. VF0 and VF1 share host-retained at family level and have separate interface domains, so they can coexist subject to ordinary ownership, capacity and selectors. This concerns exclusive host PF handoff, not every host use: approved shared children can retain the PF on the host. Node-side identity/ownership checks still fence mutations and uncertain cleanup. DRADeviceCompatibilityGroups is alpha since Kubernetes 1.37 and disabled by default. CompatibilityGroups is on ResourceSlice device.consumesCounters entries; it requires DRAPartitionableDevices. Enable both gates in kube-apiserver and kube-scheduler; follow the project installation guide for all required gates and component flags. The core derives groups from hardware; exposure.compatibility cannot override them in the POC. Official reference: https://kubernetes.io/docs/concepts/resource-management/dynamic-resource-allocation/dra-features/#device-compatibility-groups . The slide shows actual group names; counter-domain display name is explanatory, while real names use i-/f- plus identity digests.

[Publication code](../../pkg/multinetwork/inventory.go) · [Kubernetes compatibility groups](https://kubernetes.io/docs/concepts/resource-management/dynamic-resource-allocation/dra-features/#device-compatibility-groups)

## 08. Six profiles. Two compatibility domains.

The table shows all six standard kernel/shared mappings currently derived in pkg/multinetwork/inventory.go. PF PCI and VF PCI are interface-domain identity inputs, not literal counter-set names; the same physical function maps to one interface domain and all children of one PF map to one family domain. Exclusive host PF has pf-handoff; SR-IOV VF handoff and shared children retain the family with host-retained. Interface groups separate kernel-handoff, macvlan and ipvlan on the same parent. Thus PF handoff conflicts with every listed use in its family; VF handoff conflicts with children on that same VF; macvlan/ipvlan cannot mix on the same parent; same-mode children share within approved capacity. Shared PF children can coexist with VF handoff or VF children, and different VFs have independent interface domains. Compatibility alone does not grant mutations or guarantee native traffic: policy, ownership, capacity and hardware checks still apply. For future third-party gRPC plugins, descriptor declarations specify ownership/access, versioned compatibility mode, capacity requirements and mutation scope; core validates against approved policy/profiles and binds actual discovered hardware identities. Core owns ResourceSlices and authoritative capacity; plugins cannot choose different counter domains or duplicate capacity to evade conflicts. Cross-vendor compatibility requires an agreed approved contract, not arbitrary matching strings. Node plugins execute native operations within grants and record restoration. Generic declaration/dispatch remains future work; this table is the current reference behavior to preserve, not a claim that every combination was live-tested. The alpha DRADeviceCompatibilityGroups feature from the preceding slide provides scheduler group checks; native node ownership checks remain necessary.

[Publication code](../../pkg/multinetwork/inventory.go) · [Third-party compatibility plan](../grpc-plugins.md#compatibility-ownership-for-third-party-plugins)

## 09. A plugin declares a contract.

NetworkPlugin describes Allocation or Operation behavior. Its schema, input/output ports, allowed effects and state contract are accepted and locked into revisions. Current implementation types are Builtin and EffectProgram; there is no GRPC implementation type in the current CRD. EffectProgram is a declarative framework template, not an external service. The plugin is a planner: it returns an effect program; it does not get direct access to host mutations.

The slide shows an excerpt of the canonical bond-cni descriptor in examples/plugins.yaml. Members require two to eight KernelInterface/v1 Ethernet ports with ExclusiveMaster access; its single output is an exclusive KernelInterface/v1. The three allowed effects are create-bond, attach-master and link-configure. The complete closed configSchema and linkType fields are omitted on the slide, so this excerpt is explanatory rather than an apply-ready replacement for the canonical descriptor.

[Source](../admin/networkplugins.md)

## 10. Bounded effects. Recorded cleanup.

Effect opcode suffix /v1 versions the native effect contract. linux/create-bond/v1 creates the bond; linux/attach-master/v1 attaches the granted member interfaces to it; linux/link-configure/v1 applies link settings such as MTU and up/down state. allowedEffects is a permission ceiling, not an execution script or a hardware grant: the returned effect program specifies operations, and the framework also checks topology budget, policy grants and live ownership. stateContract.version v1 versions the saved-state/cleanup contract separately from effect versions. cleanup RecordedEffects means cleanup refers to the original framework execution and restores recorded effects and before-images in reverse order, rather than selecting fresh deletion targets. Restoration still checks physical identity and expected applied state. These are declarative effect permissions, not CNI binaries or shell commands. Cleanup identifies the original framework execution and never supplies fresh deletion targets.

[Source](../../examples/plugins.yaml)

## 11. Compose a network as a graph.

This is the real dual-vf-bond-vlan example. The left and right SR-IOV allocations export interface ports. The bond consumes both; each VLAN consumes the bond interface; each tuning operation configures per-interface IPv4 forwarding. Only the two Allocation roots generate DeviceClasses. Bond, VLAN and tuning operations create no scheduler-allocated devices. Connection follows dependency order; removal restores effects in reverse order.

[Source](../../examples/bond-vlan/topology.yaml)

## 12. The YAML defines the dependency graph.

This is an explanatory excerpt of examples/bond-vlan/topology.yaml. It is not apply-ready: root selectors/configuration, mandatory operation configuration fields, additional allowed effects, VLANs 200/300 and tuning steps are omitted for readability. effectBudget.allow is the topology-wide mutation ceiling; the actual complete example also includes link-move, link-configure and IPv4 forwarding. Allocation roots left and right use sriov-cni, each produces one generated DeviceClass, and the real selectors choose the approved PF. Operation uplink consumes left.interface and right.interface. tenant100 consumes uplink.interface. IDs and port references create DAG dependencies; pluginRef selects the locked plugin descriptor; config is schema-validated, normalized behavior. Compilation checks port types, cardinality, budget and composition before publishing an immutable revision.

[Source](../../examples/bond-vlan/topology.yaml)

## 13. Freeze intent before execution.

The controller normalizes configuration and stores source UID, digest, compiler version, full topology, locked plugin specs/digests and order in the immutable revision. Each root class embeds revision name, UID and digest plus root step and plugin digest. NodePrepare verifies class-origin bindings and complete root coverage. Workloads cannot substitute claim-supplied opaque configuration for this authority. Read generated class names from topology.status.rootClasses.

[Source](../admin/resource-translation.md)

## 14. The claim binds resources to a revision.

The shown direct ResourceClaim expresses the same two requests as the ResourceClaimTemplate in examples/bond-vlan/workload.yaml. LEFT_CLASS and RIGHT_CLASS are placeholders, not real class names: examples/bond-vlan/apply.sh reads status.rootClasses for rootStep left/right and substitutes the generated names. Request names primary and backup are arbitrary claim-local names; their classes determine the locked root identity. Each exactly request selects one device. The topology controller compiles the administrator NetworkTopology into an immutable NetworkTopologyRevision, then creates one DeviceClass per Allocation root. Class-origin opaque configuration contains TopologyRootConfiguration with revision name/UID/digest, root step and plugin contract digest; it is serialized native DeviceClass configuration, not another CRD. Kubernetes selects resources from ResourceSlices and propagates applicable class configuration into claim allocation. NodePrepare verifies class-origin bindings, complete roots from one revision, allocation and policy identities before preparation. NRI resolves the saved locked graph for connection in the Pod sandbox; it does not execute the current mutable topology or a topology name supplied by the workload. A Pod must reference the claim through spec.resourceClaims and consuming containers must reference its local name through resources.claims; the shipped template example handles this per Pod. The primary CNI remains separate.

[Source](../admin/resource-translation.md) · [Workload](../../examples/bond-vlan/workload.yaml)

## 15. Bound class usage per namespace.

Native Kubernetes ResourceQuota limits DRA requests by DeviceClass using <device-class-name>.deviceclass.resource.k8s.io/devices. Substitute real generated class names for LEFT_CLASS and RIGHT_CLASS; they come from topology.status.rootClasses and are the same classes in the claim example. With exactly count 1 per root, team-a quotas of 4 for each class permit at most four complete two-root claims, requesting eight devices total, subject to scheduler capacity/compatibility. This counts requested devices in ResourceClaims, including unallocated claims; it does not reserve or guarantee available hardware. team-b quotas of 0 for both class keys reject creation of new claims requesting those classes. ResourceClaimTemplate objects are not themselves device allocations; their generated ResourceClaims undergo quota admission, so Pod claim generation can fail or wait when quota is exhausted. Admission of other classes remains unrestricted by this example if no corresponding quota key is set. This is a namespace device-use budget, not a DeviceClass RBAC permission or a class visibility restriction. For a strict allowed-namespace list, administrator-managed admission must constrain class references in ResourceClaims and ResourceClaimTemplates, including generated claims and supported request forms. Administrators protect ResourceQuota writes and manage claim/template RBAC. New immutable topology revisions can generate new DeviceClass names: provision matching quota/admission rules before admitting new workloads, retain old rules for old claims until drained, and do not assume setting a new zero quota revokes an existing allocation. The project does not currently implement this namespace class allowlist. These quota examples are explanatory placeholders, not applied or live-tested cluster configuration. Official syntax: https://kubernetes.io/docs/concepts/policy/resource-quotas/ .

[Kubernetes ResourceQuota](https://kubernetes.io/docs/concepts/policy/resource-quotas/)

## 16. Connect before the application starts.

These are the four planner hook names. Kubelet calls the DRA prepare/unprepare lifecycle, and NRI runtime callbacks drive connection/removal in the sandbox. They are not four kubelet RPC methods. At container creation/start the framework checks attachment identity and scoped access. Pod removal and claim release are separate: deleting a Pod does not automatically free a manually managed ResourceClaim. NetworkAttachmentStatus projects phase, outputs and failures.

[Source](../architecture.md)

## 17. Internal planners. A transport-neutral boundary.

pkg/networkplugins.Run accepts pluginapi.Request and returns pluginapi.Result. Requests carry hook, normalized config, opaque allocation/input/prepared references and recorded execution. No Kubernetes client, namespace path or journal crosses the boundary. pkg/topology binds and validates results; pkg/effects owns Linux/OVS mutation and journaling. The planners are internal implementations using the same lifecycle contract intended for gRPC, not internal gRPC servers. The POC does not invoke CNI binaries despite familiar plugin names.

[Source](../../pkg/pluginapi/api.go)

## 18. One contract. Multiple network shapes.

These are all eleven names accepted in networkplugins.Run. VF settings, approved driver rebinding, VFIO, reset, and RDMA profiles are supported within certified allocation contracts rather than being arbitrary new allocation authorities. IPAM and address planners are pure: IPAM exports a lease reference; address-cni plans address configuration. Physical RDMA/InfiniBand acceptance is still outstanding.

[Source](../../pkg/networkplugins/builtin.go)

## 19. Two phases. One plugin boundary.

Revised target requested for this presentation: external gRPC network plugins own both planning/validation and native Linux/OVS mutations. Phase 1 begins when an administrator applies NetworkTopology. The controller checks graph structure and grants, invokes plugin contract/config/plan validation, and publishes an immutable revision plus root DeviceClasses. Validation must not mutate host or Pod interfaces. Current Compile already calls PlanPrepare and PlanConnect to validate generated effect programs, but those checks are ordinary in-process Go calls. Phase 2 begins after DRA allocation and a Pod sandbox is available. The core verifies claim, saved revision and policy scope, then calls the node-local external plugin through NodePrepare, ConnectNetwork, RemoveNetwork and NodeUnprepare. The plugin implements its native interface operations, such as creating a bond, moving/configuring links and restoring its recorded changes. Core responsibilities remain topology coordination, DRA inventory/allocation verification, policy grants, runtime identity and status. Services that mutate interfaces must execute on the relevant node with scoped access to its host/sandbox resources; a generic remote controller service cannot directly manipulate a node namespace. The current serializable planner SDK only returns effect programs or recorded cleanup declarations: it does not implement this delegated-execution protocol. The revised RPC contract still needs execution authorization, durable operation identity, before-image ownership, acknowledgements, idempotent replay and compensation/recovery coordination across service failures. The updated docs/grpc-plugins.md now describes this target, including vendor actions without core opcode handlers, execution grants and durable recovery requirements. No external planner/executor server or protobuf protocol exists today.

The acceptance goal is a vendor-defined supported action that registers, compiles, executes and restores without adding a plugin-name compiler branch or core native handler. New allocation/privilege models still require a versioned generic capability contract; the current POC has not implemented this extensibility.

[Source](../grpc-plugins.md)

## 20. “gRPC” means three different things.

Three boundaries: kubelet to the DRA driver already uses gRPC through kubeletplugin. The current framework-to-network boundary is networkplugins.Run over Go calls, and core pkg/effects performs native changes. The revised presentation target replaces that boundary with external gRPC network plugin services supporting both validation/planning and native execution. The updated docs/grpc-plugins.md documents this target; it requires a new execution protocol beyond the current planning SDK. The IPAM Provider boundary is a separate stateful service contract, currently the local Store. Its gRPC client/server also remain future work. None of these proposed external service examples are apply-ready current CRDs.

[Source](../grpc-plugins.md)

## 21. Allocate capacity. Reserve an address.

The controller publishes subnet ResourceSlices for ipam.schseba.github.io, and the node registers that second DRA driver. One claim can request both interfaces and address capacity. The framework verifies entitlement before Provider.Reserve. Provider has ListPools, GetPool, Inventory, Reserve, Lookup, AcquireHolder, ReleaseHolder and Release. Current Store uses a protected ConfigMap CAS ledger. Attachment holders are acquired before address use, removed after cleanup, and claim release is fenced until holders are gone. Scheduling capacity and concrete address reservation remain separate accounting layers; a future backend must share a transactional ledger.

[Source](../../pkg/ipam/provider.go)

## 22. IPAM is a DRA allocation root.

This is the macvlan-ipam topology from examples/ipam/topology.yaml with the endpoint hardware/persona selectors omitted for readability. The shown configuration and three allowed effects are otherwise the complete three-step shape. endpoint is an Allocation root handled by macvlan-cni. address is an Allocation root referencing the approved ipam NetworkPlugin; that descriptor declares allocationDriver ipam.schseba.github.io in pkg/topology/ipam.go. allocationDriver belongs to the plugin descriptor, not the NetworkTopology step. The controller generates an endpoint DeviceClass and an address DeviceClass, each bound to one saved revision. The IPAM publisher publishes a shared subnet device with usable address capacity in ResourceSlices. A workload ResourceClaim requests both classes and explicit one-unit capacities. Kubernetes performs DRA allocation; the plugin does not directly invoke the scheduler or choose physical devices. Selecting one subnet device with addresses capacity 1 yields one allocated share, not a predetermined IP. The IPAM driver verifies claim/class/node entitlement and calls the Provider to reserve a concrete address during prepare. configure-address is an Operation consuming endpoint.interface and address.lease; address-cni configures the verified lease on the authorized interface before containers start. This Operation has no class of its own. The next two slides show the generated address class and complete two-root claim. No cluster changes were made for this presentation.

[Topology](../../examples/ipam/topology.yaml) · [IPAM plugin contract](../../pkg/topology/ipam.go) · [Subnet publication](../../pkg/ipam/publisher.go)

## 23. The address class selects a subnet.

The controller generates the address DeviceClass for the macvlan-ipam topology, alongside an endpoint class. The IPAM_CLASS name is a placeholder from topology.status.rootClasses[rootStep=address]. This explanatory excerpt is not apply-ready: it omits managed metadata/owner references, the shared-device and allocationReady selector terms, and part of the mandatory framework root binding (apiVersion, revision name/UID/digest, pluginContractDigest). The full controller class in pkg/controller/topology.go selects device.driver ipam.schseba.github.io, allowMultipleAllocations true, network blue, subnet 192.0.2.0/29 and allocationReady true. It has two opaque class-origin configurations: TopologyRootConfiguration addressed to dra-multi-network.schseba.github.io locks the revision/root; IPAMAllocationConfiguration addressed to ipam.schseba.github.io fixes the network, subnet and one-address profile. These are configuration payloads, not additional CRDs. No PCI, link type or interface-mode selection belongs in the address class. The ResourceSlice advertises one shared subnet device with a fixed usable addresses capacity; each claim gets its own share. Concrete addresses are reserved by the provider during prepare, not enumerated as separate DeviceClasses. The current driver requires FromClass configuration; workload-supplied opaque configuration cannot authorize the reservation.

[Class generation](../../pkg/controller/topology.go) · [IPAM binding design](../ipam-plan.md#deviceclasses-and-trusted-bindings)

## 24. One claim requests interface + address.

This direct ResourceClaim has the same device requests as examples/ipam/workload.yaml, which uses a ResourceClaimTemplate to create one claim per Pod. ENDPOINT_CLASS and IPAM_CLASS are generated root classes from the same macvlan-ipam revision. The endpoint request selects one shared macvlan parent and explicitly consumes one dra-multi-network.schseba.github.io/children unit. The address request selects one shared subnet device and explicitly consumes one ipam.schseba.github.io/addresses unit. count 1 means one selected device; it is distinct from the capacity quantity consumed on that device. The address class is pinned to network blue and subnet 192.0.2.0/29; the claim does not choose a concrete IP or provide driver configuration. Both roots must allocate for the topology to execute. If the subnet has exhausted its published capacity, a complete claim remains Pending even if a macvlan child slot remains. During preparation the IPAM driver verifies entitlement and obtains a durable provider reservation. Before native address use, the attachment acquires a holder. NRI connects the graph; address-cni consumes endpoint.interface and address.lease to apply the leased IP before application containers start. No DeviceClass or separate claim request exists for the configure-address Operation. Today core effects execute address configuration; the gRPC target delegates native work to the node plugin while core/provider entitlement and holder coordination remain. Cleanup removes owned native address use before holder and claim release. These examples were checked against controller and workload source, not applied to a cluster.

[Workload example](../../examples/ipam/workload.yaml) · [Topology](../../examples/ipam/topology.yaml)

## 25. Where feedback can help.

Invite feedback on the revised proposed execution boundary, not endorsement. Key open work: external gRPC network services for both validation/planning and native execution, scoped node-local privileges, durable execution identity and cross-process journaling/recovery, stateful IPAM RPC implementation, protocol versioning, authenticated endpoints, upgrade-compatible cleanup, physical hardware acceptance and host reboot recovery. Ask which responsibilities must remain in the core and which native network operations belong in independently deployed node plugins. The current POC still executes native effects inside the framework.

[Source](../roadmap.md)

## 26. The implementation map.

Appendix for Q&A. These links are relative to this deck within the checkout. The slide deck itself is fully offline and self-contained; only these optional code/documentation links require adjacent repository files. The production design guide provides proposed handshake, deployment, authorization and compatibility details. The linked grpc-plugins.md now describes the vendor-native execution target on the gRPC target slide, its protocol requirements and production acceptance.

[Source](../architecture.md)


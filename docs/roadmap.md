# Remaining work after first POC review

The POC includes internal planners, typed effect programs, topology compilation,
five official CRDs, policy-approved DRA publication, NRI connection, recorded
cleanup, eswitch drain/transition logic and shared-uplink VLAN ownership.

Deferred acceptance work:

- Host reboot recovery and testing by rebooting a dedicated node.
- Broader physical hardware restoration/composition and complete live regression.
- Physical RDMA/InfiniBand and switchdev/OVS acceptance on suitable nodes; the
  four-VF switchdev mock lab already exercises both plugin paths.
- Upgrades and migration, including saved-contract compatibility across versions.
- A final manual review of cluster feature gates alongside the reviewed release.

GA architecture work:

- Move **all** plugin-specific validation/planning and native network execution to
  external gRPC services. Vendors must be able to add supported operations without
  plugin-name branches, built-in opcode handlers or native mutations added to core.
  Keep DRA, generic topology/schema checks, allocation ownership, policy grants,
  runtime identity and durable lifecycle coordination in the framework.
- Replace/extend the planning-only SDK with versioned validation and execution
  contracts: scoped node/sandbox delegation, authenticated node endpoint dispatch,
  durable receipts/query/replay, fencing and typed output checks. Plugins persist
  native before-images/applied state; core journals coordination and holds ownership
  until restoration is verified. Specify compensation and cross-process recovery.
- Define generic ownership/compatibility declarations for external descriptors.
  Core derives canonical interface/PF-family domains from physical inventory and
  publishes authoritative shared accounting. Plugins declare access/modes and
  mutation scope, not independent counter domains or duplicate capacity. Preserve
  the six host/VF/macvlan/ipvlan mappings and test cross-vendor sharing/conflicts.
- Migrate each current native family to a node-local plugin executor, keeping old
  recorded POC executions cleanable during rollout. Conformance must prove a new
  vendor action compiles, executes and restores without a vendor-specific core patch,
  including lost acknowledgements, foreign state, restarts and upgrades.
- Implement the IPAM `Provider` RPC client/server using the local interface's
  inventory, reservation, lookup and holder/release call points; preserve global
  uniqueness, entitlement checks, cleanup fencing and fixed subnet capacity.
- Version the protobuf/handshake contract, authenticate and authorize services,
  support remote NetworkPlugin endpoint configuration and retain compatible
  cleanup services across upgrades. See the [owner/deployment guide](grpc-plugins.md)
  for concrete proposed configuration and production acceptance work.
- Define supported release/version/compatibility and deployment guarantees after
  the POC has been reviewed. The POC does not promise production recovery coverage.

Tests that launch containerd/CRI-O binaries remain outside the requested suite.
The POC includes an IPv4 IPAM demo with scheduler-visible subnet capacity and
durable claim/attachment leases. DHCP, DNS, dual-stack, retained workload
identities, migration and production-scale ledger storage remain deferred.

The addressing implementation is described in [IPAM with DRA subnet capacity](ipam-plan.md):
one claim requests interface and IPAM classes, with scheduler-visible subnet
capacity and durable backend address/attachment ownership.

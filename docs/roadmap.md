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

- Move **all** plugin implementations to external gRPC services using the existing
  serializable SDK and keeping DRA, policy grants, native execution and journaling
  in the framework.
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

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
- Define supported release/version/compatibility and deployment guarantees after
  the POC has been reviewed. The POC does not promise production recovery coverage.

Tests that launch containerd/CRI-O binaries remain outside the requested suite.
There is no IPAM implementation in this POC; application/fabric addressing stays
an explicit operator/user step.

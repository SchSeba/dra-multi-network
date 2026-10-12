# Documentation

Read [architecture](architecture.md) for the complete control and runtime flow.
The [API reference](api-reference.md) lists every CRD field and shipped plugin
profile; generated CRD schemas remain authoritative.

## Cluster administrators

1. [Install the controller, admission service and node agent](installation.md).
2. [Define and validate NetworkPlugins](admin/networkplugins.md).
3. [Approve hardware with DeviceExposurePolicy](admin/exposure-policies.md).
4. [Compose a NetworkTopology](admin/topologies.md).
5. [Understand generated DeviceClasses and ResourceSlices](admin/resource-translation.md).
6. [Operate, diagnose and drain the installation](operations.md).

NetworkPlugin, NetworkTopology, DeviceExposurePolicy and NetworkTopologyRevision
are cluster-scoped administrator resources. The controller owns revision content
and generated classes. Users should not have write access to them.

## Application users

[Connect a Pod](user/pods.md) using the DeviceClass names supplied by an
administrator. [Examples](../examples/README.md) separate device/topology setup
from claim and Pod usage. NetworkAttachmentStatus is a namespaced, agent-owned
observation that users can read to inspect their network.

## Contributors and reviewers

[Development](development.md), [plugin SDK](../pkg/pluginapi/README.md),
[validation](validation.md) and [roadmap](roadmap.md) describe the current POC
and its limits. Manual integration checks live in [test/integration](../test/integration/README.md),
separate from installation and from tests that run without real hardware.

[IPAM with DRA subnet capacity](ipam-plan.md) describes the address backend,
second allocation class and runnable macvlan/two-VF examples.

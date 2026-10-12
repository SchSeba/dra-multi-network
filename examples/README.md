# Examples

Install the [driver](../docs/installation.md) first. Run commands from the
repository root with a kubeconfig for that cluster. Each folder has a README
with prerequisites, commands, expected results and cleanup.

`policy*.yaml`, `plugin*.yaml` and `topolog*.yaml` are **cluster administrator**
configuration. `workload.yaml` is **application user** configuration. `apply.sh`
combines those steps for a lab; a normal user receives already approved classes.
Generated DeviceClass names come from topology status. Workload files contain
placeholders and must be rendered by the helper or the README instructions.

## Start here

| Folder | Demonstrates |
|---|---|
| [single-vf](single-vf/README.md) | One exclusive SR-IOV VF as `secondary0` |
| [bond-vlan](bond-vlan/README.md) | Two VFs → active-backup bond → three VLANs |
| [shared](shared/README.md) | PF macvlan with VF ipvlan; shared capacity and exclusive VF conflict |

## Other profiles and customization

| Folder | Demonstrates |
|---|---|
| [vfio](vfio/README.md) | Approved VFIO binding and scoped consuming-container access |
| [vf-config](vf-config/README.md) | Administrator-approved VF settings |
| [kernel-rebind](kernel-rebind/README.md) | Approved kernel-driver binding |
| [reset](reset/README.md) | Function-level reset profile |
| [effect-program](effect-program/README.md) | Declarative operation plugin |
| [link-config](link-config/README.md) | Typed VLAN/MAC/alias configuration |
| [root-effect-program](root-effect-program/README.md) | Declarative allocation planners preserving native authority |
| [roce](roce/README.md) | Dedicated whole-HCA single-port RoCE |
| [ipoib](ipoib/README.md) | Single-port InfiniBand IPoIB and verbs |
| [switchdev](switchdev/README.md) | Two-Pod accelerated bridge and OVS, native/tagged VLAN traffic, eswitch management |

The manifests use the virtual lab's dedicated PF `0000:29:00.0` and selected VFs.
Edit **both policy and topology selectors**, drivers and host identities for
other hardware. Do not apply every scenario at once: some deliberately expose
conflicting choices or use the same VF. Drain earlier workloads before changing
ownership/mode. RDMA/InfiniBand/switchdev require suitable physical hardware;
see [validation](../docs/validation.md).

[`connect.sh`](connect.sh) renders a minimal Pod for an already configured
**single-root** topology. Multi-root examples provide their own workload helper.
It does not approve hardware or publish topology configuration.

## Cleanup

For an isolated examples-only namespace, stop all consumers first:

```bash
kubectl delete namespace multi-network-example --wait=true
```

Then remove only the administrator objects created for your scenario. Wait for
claim/attachment cleanup and retained ownership before uninstalling the driver.
RDMA examples use separate namespaces, listed in their READMEs. The regression
scripts are under [test/integration](../test/integration/README.md), not in the
user example directories or deployment flow. Host runtime fragments are under
[deployments/runtime](../deployments/runtime/README.md).

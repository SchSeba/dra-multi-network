# Examples

For the virtual lab, [bond/VLAN and OVS together](bond-ovs/README.md) runs both scenarios on each workload node with separate native and mock NICs.

Install the [driver](../docs/installation.md) first. Run commands from the
repository root with a kubeconfig for that cluster. Each folder has a README
with prerequisites, commands, expected results and cleanup.

`policy*.yaml`, `plugin*.yaml` and `topolog*.yaml` are **cluster administrator**
configuration. `workload.yaml` is **application user** configuration. `apply.sh`
combines those steps for a lab; a normal user receives already approved classes.

[plugins.yaml](plugins.yaml) lists all builtin NetworkPlugin descriptors: their
implementations, configuration schemas, typed ports, allowed effects and cleanup
contracts. The DRA driver's controller installs these resources automatically
today, so you do not need to apply this file separately. It is included as a
reference to make the examples' plugin configuration visible. Custom plugins
remain in each example's `plugin.yaml` or `device-plugins.yaml` and are applied
as described in that example's README.

Generated DeviceClass names come from topology status. Workload files contain
placeholders and must be rendered by the helper or the README instructions.

## Make targets

Apply an example to the existing cluster, or remove its workloads and configuration:

```bash
make examples-help
make deploy-example-bond-ovs
make delete-example-bond-ovs
# On the remote virtualization host:
rrun "make deploy-example-bond-ovs"
rrun "make delete-example-bond-ovs"
```

Every name below has `deploy-example-NAME` and `delete-example-NAME` targets:

| Name | Example |
|---|---|
| `single-vf` | One kernel VF |
| `bond-vlan` | Native VF bond and VLANs |
| `bond-ovs` | Native bond and OVS switchdev together on every workload node |
| `effect-program` | Declarative operation plugin and bond/VLAN traffic |
| `link-config` | VLAN, MAC and alias configuration |
| `kernel-rebind` | Approved kernel driver binding |
| `reset` | Approved function-level reset |
| `shared` | Shared macvlan and ipvlan |
| `root-effect-program` | Programmable allocation roots |
| `vf-config` | Approved VF settings |
| `vfio` | Scoped VFIO access |
| `ipam` | Macvlan with DRA IPAM |
| `ipam-two-vf` | Two VFs with two IPAM addresses |
| `roce` | RoCE hardware |
| `ipoib` | IPoIB hardware |
| `switchdev-ovs` | Standalone OVS switchdev |
| `switchdev-accelerated-bridge` | Standalone Linux bridge switchdev |

The targets reuse the example helpers and require Python with PyYAML and
kubectl. Set `KUBECONFIG` to select a cluster; otherwise they use the virtual
lab kubeconfig for `CLUSTER_NAME` (default `multi-network`). `bond-ovs` also
requires kcli on the virtualization host.

Hardware prerequisites still apply. Adapt standalone manifests to the intended
PF/VFs before deploying; the combined example discovers both test PFs. RoCE
needs `DRA_RDMA_PCI` and `DRA_RDMA_DRIVER`; IPoIB needs `DRA_IB_PCI` and
`DRA_IB_DRIVER`; standalone OVS needs `OVS_BRIDGE_UUID`. Pass these as environment
variables or Make command-line variables. Drain earlier driver claims before
switching exposure policies; these examples are not an instruction to run every
hardware scenario simultaneously.

Delete targets remove only the named example's Pods, wait for generated claims
to release, and then delete its administrator resources. Shared namespaces,
borrowed policies and templates used by other Pods are preserved. The IPAM pool
is removed only when no remaining topology references its network. Shared host bridges are
preserved; the combined delete target restores recorded eswitch modes after a
full driver drain. No journals or finalizers are forcibly removed.

## Start here

| Folder | Demonstrates |
|---|---|
| [single-vf](single-vf/README.md) | One exclusive SR-IOV VF as `secondary0` |
| [bond-vlan](bond-vlan/README.md) | Two VFs → active-backup bond → three VLANs with IPv4 forwarding |
| [shared](shared/README.md) | PF macvlan with VF ipvlan; shared capacity and exclusive VF conflict |
| [IPAM](ipam/README.md) | Subnet address capacity, one IP per instance and two-VF/two-IP allocation |

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

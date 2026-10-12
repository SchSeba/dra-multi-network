# Manual live integration checks

These scripts require a running cluster, kubeconfig and compatible hardware.
They are separate from deployment and from `go test`. Run from the repository
root; each script locates its fixture manifests under `examples/`.

Start with the README in the corresponding example folder. Adapt both manifests
and hardware-specific assertions if changing the lab PCI addresses. Scripts
may create/delete workloads, restart the agent or mutate/restore device state;
read them before running. Drain earlier consumers of the same resources first.

| Directory | Checks / fixture |
|---|---|
| [ipam](ipam/README.md) | Address capacity, traffic, exhaustion/reuse/restart and two VFs with two IPs |
| `bond-vlan` | Two-Pod traffic and failover; [example](../../examples/bond-vlan/README.md) |
| `shared` | Mixed PF-macvlan/VF-ipvlan traffic and same-VF handoff exclusion |
| `vfio` | VFIO lifecycle and per-container/request access |
| `vf-config`, `kernel-rebind`, `reset` | Approved hardware changes and recorded release |
| `root-effect-program`, `link-config` | Declarative planning and cleanup; operation-only example reuses bond-vlan script |
| `capabilities` | Revision-specific node readiness and topology deletion fencing |
| `retention` | Revision/class/plugin retirement and safe retention |
| [switchdev](switchdev/README.md) | Four-VF Linux/OVS representor traffic, restart, restore and eswitch drain fencing |
| `ipoib` | No-hardware fail-closed behavior, not hardware acceptance |

Example invocation:

```bash
./examples/bond-vlan/apply.sh
./test/integration/bond-vlan/test-traffic.sh
```

Read [validation](../../docs/validation.md) for accepted/deferred scope. There are
no runtime-binary unit tests, automatic reboot checks or migration acceptance
requirements in this suite. Full hardware/reboot/final regression remains
separate POC follow-up work.

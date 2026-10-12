# Development

Use Go 1.27. The module is `github.com/SchSeba/dra-multi-network` and Kubernetes
dependencies are aligned to 1.37. Build and tests do not invoke CNI binaries.

```bash
make generate                 # deepcopy, project CRDs, host mocks
make fmt
make lint
make test                     # includes controller envtest, downloads assets
make build
# Place binaries under an ignored directory:
make cmds PREFIX="$PWD/bin" GOARCH="$(go env GOARCH)"
```

`make test-coverage` is the CI coverage target. Tests do not launch containerd
or CRI-O binaries. With preinstalled envtest assets, run:

```bash
KUBEBUILDER_ASSETS=/absolute/path/to/k8s/1.37.0-linux-ARCH \
  go test ./cmd/... ./pkg/... ./hack/...
```

Generated schemas live in `config/crd/bases` and are copied to the Helm chart
by `make generate`. Edit API Go types, then regenerate both locations. Edit
canonical descriptors in `pkg/topology` only with contract/digest compatibility
in mind; existing revisions retain their saved contracts. Update docs/examples
when changing the public API.

## Source map

| Path | Purpose |
|---|---|
| `cmd/dra-multi-network-controller` | Controller/admission entrypoint |
| `cmd/dra-multi-network-node` | DRA/NRI agent entrypoint |
| `cmd/network-topology-compile` | Offline compilation preview |
| `pkg/api/networking/v1alpha1` | Five project CRD types |
| `pkg/controller`, `pkg/admission` | Compilation, retention, capability placement and API boundaries |
| `pkg/multinetwork` | Inventory/publication, DRA preparation, NRI and administrative management |
| `pkg/pluginapi`, `pkg/networkplugins` | Serializable SDK and pure internal planners |
| `pkg/topology` | Canonical schemas, DAG validation, template binding and plan validation |
| `pkg/effects`, `pkg/journal` | Native effects, physical ownership and durable recovery |
| `pkg/discovery`, `pkg/host`, `pkg/cdi` | Reused discovery/host helpers and scoped CDI |
| `deployments` | Image, Helm and host-runtime configuration |
| `examples`, `test/integration` | Public manifests and manual live regression |

## Plugin changes

Read the [SDK boundary](../pkg/pluginapi/README.md) before implementing a planner.
Do not import Kubernetes, DRA, netlink, OVSDB or journal code into plugin packages.
Use the existing normalized request/result and opaque references. Add config,
port and effect validation at the shared framework boundary. Cleanup requests
restoration of the recorded original hook; it does not plan new deletion targets.
Tests cover dependency isolation, JSON serialization, profile/config/composition
validation and meaningful native recovery/ownership behavior. GA gRPC services
are future work; no transport scaffolding is needed to edit a POC planner.

## Test a live build

On the configured virtualization host, after the cluster already exists:

```bash
make redeploy-dra-driver-virtual-cluster
export KUBECONFIG="$HOME/.kcli/clusters/multi-network/auth/kubeconfig"
./examples/bond-vlan/apply.sh
./test/integration/bond-vlan/test-traffic.sh
```

`rrun 'make redeploy-dra-driver-virtual-cluster'` is an environment-specific
remote wrapper, not a project dependency. SSH/kubeconfig/hardware access is
required for live checks. Manual integration scripts can change device settings,
restart the agent and create/delete resources; read each script and its fixture
README before running it. They do not run during Helm deployment.

## First-push checks

Run generation, format, lint, tests and build. Lint/render the chart; preview all
example topologies with their custom plugin descriptors. Check Markdown links,
example commands and cleanup ordering. CI runs software checks on push/PR;
virtual-cluster smoke testing is an explicit manually dispatched workflow.
No upgrade, reboot, runtime-binary or unsupported hardware acceptance test is
implicitly required by that software suite.

The project starts from dra-driver-sriov and references host-device, SR-IOV,
bond, VLAN, macvlan/ipvlan, accelerated bridge and OVS CNI behavior. Keep upstream
copyright/license notices when reusing code; [NOTICE](../NOTICE) records origins.

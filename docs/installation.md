# Installation

## Prerequisites

- Kubernetes **1.37** with `resource.k8s.io/v1` and the feature gates below.
- Linux nodes, a working primary CNI, and dedicated secondary PCI devices.
- NRI enabled in CRI-O or a suitably configured containerd. The agent uses
  `/var/run/nri/nri.sock`, name `dra-multi-network`, index `42`.
- For SR-IOV, create the intended VFs before exposing them. The driver does not
  manage `sriov_numvfs` or replace the primary CNI.
- Go **1.27**, a container build tool and Helm for building/installing from source.

Enable these gates on the API server, scheduler and kubelets that support them:

```text
DynamicResourceAllocation=true,DRAPartitionableDevices=true,DRAConsumableCapacity=true,DRADeviceCompatibilityGroups=true
```

Compatibility groups are critical: alternative personas for the same function
and PF/VF family must participate in the same physical ownership domains. Check
actual component flags manually after deployment; installing the Helm chart
does not configure Kubernetes feature gates.

## Configure the runtime

Follow [runtime enforcement](../deployments/runtime/README.md) before running
workloads. Its CRI-O/containerd fragments are host configuration, not ConfigMaps.
The admission service adds and protects the required NRI plugin annotation.
The runtime must enforce it. The provided containerd patch addresses the tested
first-request-after-disconnect gap; do not assume stock containerd provides the
same guarantee. Runtime replacement remains an administrator action on a
drained node, separate from this chart.

Bootstrap toleration is restricted to operator-controlled system Pods. The
chart annotates its own controller and node agent so they can start before NRI
connects. Do not give application Pods this bypass or write access to the driver
namespace. See [operations](operations.md) for trust and troubleshooting.

## Build and install

Until a release is published, build an image and make it available to every
node. Substitute your registry and tag; these commands do not assume a published
image exists.

```bash
git clone https://github.com/SchSeba/dra-multi-network.git
cd dra-multi-network
make generate
# Uses the project container Makefile and CentOS Stream 9 runtime image.
make -C deployments/container centos9
```

The container Makefile prints the produced image name. Push/tag it in your
registry, then install using that repository and tag:

```bash
helm upgrade --install dra-multi-network deployments/helm/dra-multi-network \
  --namespace dra-multi-network --create-namespace \
  --set image.repository=YOUR_REGISTRY/dra-multi-network \
  --set image.tag=YOUR_TAG
kubectl -n dra-multi-network get deployments,daemonsets,pods
```

The chart installs five project CRDs, RBAC, a topology-controller Deployment,
an NRI/DRA node DaemonSet and fail-closed admission webhooks. Admission uses
controller-managed serving certificates and a Kubernetes Secret; cert-manager
is not required. The controller defaults to control-plane placement; override
`controller.nodeSelector`/tolerations when your cluster needs different placement.
Use `kubeletPlugin.nodeSelector` to limit the node agents to intended nodes, and
set its tolerations if those nodes are tainted. Kubelet registration paths are
configurable. The agent is privileged and mounts the native resources needed by
its approved effects, including network namespaces, VFIO, CDI and the journal.

For OVS only, configure OVS on the participating nodes and enable its socket mount:

```bash
helm upgrade dra-multi-network deployments/helm/dra-multi-network \
  --namespace dra-multi-network --reuse-values --set kubeletPlugin.ovs.enabled=true
```

The chart does not create bridges, configure shared uplinks or start OVS. Those
are administrator prerequisites for [switchdev examples](../examples/switchdev/README.md).
See the [chart README](../deployments/helm/dra-multi-network/README.md) for values.

## Manual verification

```bash
kubectl get crds | rg 'networking.dra-multi-network.schseba.github.io'
kubectl get networkplugins
kubectl get nodes -L dra-multi-network.schseba.github.io/ready
kubectl -n dra-multi-network logs daemonset/dra-multi-network-kubeletplugin --tail=100
```

Check deployment resource names with `kubectl get deploy,ds` if using a custom
Helm release/fullname. Verify the runtime socket and node process flags on each
node. On kubeadm-style control planes, inspect `/etc/kubernetes/manifests/` for
API server/scheduler feature gates; inspect the node kubelet configuration or
running process flags. Confirm all four gates above rather than relying on the
chart or lab input file.

Then apply an [example](../examples/README.md) after adapting its PCI selectors.
ResourceSlices are empty until an exposure policy approves eligible hardware.
A topology can compile successfully while no device matches it.

## Virtual lab

On a dedicated Linux virtualization host with kcli, libvirt/KVM and the required
network/image access:

```bash
make deploy-single-node-virtual-cluster-standalone
export KUBECONFIG="$HOME/.kcli/clusters/multi-network/auth/kubeconfig"
make redeploy-dra-driver-virtual-cluster
make e2e-workloads
```

The `standalone` suffix is a compatibility target alias; the project has one
internal MVP mode. The lab uses CRI-O, Flannel and Kubernetes 1.37. Defaults are
cluster `multi-network`, management network `multi-net` (`192.168.130.0/24`),
secondary network `multi-sriov` (`192.168.210.0/24`), Pod CIDR `10.246.0.0/16` and
service CIDR `10.112.0.0/16`. Environment overrides are listed by
`hack/deploy-virtual-k8s-cluster.sh --help`. Provisioning creates/updates lab VMs;
use it only on the intended virtualization host. Verification remains a separate
manual step. `test/integration` is never run as a Helm installation hook.

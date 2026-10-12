# dra-multi-network Helm chart

Installs the five project CRDs, controller/admission Deployment, DRA/NRI node
DaemonSet, service accounts/RBAC and admission services/webhooks. Kubernetes
1.37 and a configured NRI runtime are prerequisites. See
[installation](../../../docs/installation.md) before installing.

```bash
helm upgrade --install dra-multi-network . \
  --namespace dra-multi-network --create-namespace \
  --set image.repository=YOUR_REGISTRY/dra-multi-network --set image.tag=YOUR_TAG
```

Run from this chart directory, or replace `.` with its repository path. Build
and publish/load your image first; a default repository does not prove that a
POC tag is published. Project configuration is CRDs, not ConfigMaps. There is
one internal MVP mode. The chart does not install CNI binaries, manage the primary
CNI, create VFs/bridges or change Kubernetes feature gates.

## Values

| Key | Default / purpose |
|---|---|
| `image.repository`, `image.tag`, `image.pullPolicy` | `ghcr.io/schseba/dra-multi-network`, chart appVersion, `Always` |
| `imagePullSecrets` | Registry credential Secret references |
| `nameOverride`, `fullnameOverride`, `namespaceOverride` | Resource naming / target namespace overrides |
| `allowDefaultNamespace` | `false`; deployment should have a dedicated namespace |
| `selectorLabelsOverride` | Optional selector labels |
| `serviceAccount.create`, `.name`, `.annotations` | Node service account creation and metadata |
| `controller.nodeSelector` | Control-plane nodes by default |
| `controller.tolerations`, `.affinity`, `.priorityClassName` | Placement / priority |
| `controller.podAnnotations`, `.podSecurityContext` | Pod metadata/security |
| `controller.containers.controller.securityContext`, `.resources` | Controller container settings |
| `kubeletPlugin.nodeSelector`, `.tolerations`, `.affinity` | Node agent placement |
| `kubeletPlugin.updateStrategy`, `.priorityClassName` | RollingUpdate / system-node-critical |
| `kubeletPlugin.podAnnotations`, `.podSecurityContext` | Pod metadata/security |
| `kubeletPlugin.containers.plugin.securityContext`, `.resources` | Privileged native executor / resources |
| `kubeletPlugin.kubeletRegistrarDirectoryPath` | `/var/lib/kubelet/plugins_registry` |
| `kubeletPlugin.kubeletPluginsDirectoryPath` | `/var/lib/kubelet/plugins` |
| `kubeletPlugin.nriPluginName`, `.nriPluginIndex` | `dra-multi-network`, `42`; must agree with runtime |
| `kubeletPlugin.ovs.enabled` | `false`; expose existing host OVSDB socket when enabled |
| `logging.level`, `.format`, `.alsologtostderr`, `.logFile` | `3`, `text`, `true`, empty file path |

Admission certificates are managed by the controller using a Secret. No
cert-manager installation is needed. Verify resource readiness, runtime socket,
feature gates and example behavior manually after deployment.

## Validate and remove

```bash
helm lint .
helm template dra-multi-network . --namespace dra-multi-network >/tmp/dra-multi-network.yaml
```

Before uninstalling, drain Pods/claims and restore all owned hardware/host state
as described in [operations](../../../docs/operations.md). Helm uninstall is not
a substitute for claim cleanup. Preserve journals and retained CRD contracts
until restoration completes.

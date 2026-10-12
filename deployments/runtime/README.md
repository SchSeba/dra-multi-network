# Runtime enforcement

The node agent connects to `/var/run/nri/nri.sock` as plugin
`dra-multi-network`, index `42`. NRI must remain enabled and accept external
connections. Both runtime fragments set the callback timeout to ten seconds.
These are operator configuration files, not ConfigMaps or deployment hooks.

For CRI-O 1.37.2, install `crio.conf` in `/etc/crio/crio.conf.d/` and restart
the runtime on a drained node. Project Pods must carry
`required-plugins.noderesource.dev: '["dra-multi-network"]'`; admission adds and
protects this annotation, including container overrides. Its absence/recovery
test passed after runtime disconnection was recognized. Immediate-disconnect
behavior remains unverified on CRI-O; that earlier check does not cover the
first-request failure reproduced below on containerd.

For containerd `2.4.1-dra-nri1`, import the absolute path of `containerd.toml` from a
version-3 or version-4 `/etc/containerd/config.toml`, preserving the rest of the node's
configuration. This fragment requires the agent globally. Before enabling it,
add `dra-multi-network.schseba.github.io/bootstrap-nri-toleration: "true"` to
operator-controlled bootstrap Pods and required static/system Pods that must
start before the agent. The Helm chart sets it on the controller and
node agent. Keep its namespace writable only by operators. Admission rejects
tenant tolerations and rejects tolerations on project workloads even for a
trusted agent/controller. Do not grant this bypass to application Pods.

The runtime and the agent must use the same socket. See the upstream
[containerd NRI configuration](https://github.com/containerd/containerd/blob/v2.4.1/docs/NRI.md).

Stock containerd 2.4.1 with NRI 0.12.3 failed the disconnect check: the first
Create request after a transport failure counted the closed plugin as present.
The upstream [adaptation implementation](https://github.com/containerd/nri/blob/v0.12.3/pkg/adaptation/adaptation.go)
adds participation before invoking the callback, and Start has no mandatory
participation recheck. The shipped two-file patch adds participation only for
plugins still open after Create and reuses the default validator's required
plugin check after Start callbacks. It preserves optional-plugin behavior and
the existing annotation/toleration syntax. Do not certify the stock binary by
waiting for its disconnected plugin to disappear before testing.

Build the patch against the pinned upstream commit and vendor lock:

```bash
GOOS=linux GOARCH=amd64 ./deployments/runtime/build-containerd.sh
```

The script verifies the source archive digest and applies the exact patch
without fuzzy matching. It builds only `containerd`; matching upstream 2.4.1
shim binaries and runc must also be on PATH. This is a manual build, not a
runtime replacement or deployment hook. The live cluster remains on CRI-O.

The Go test suite does not launch containerd or CRI-O binaries. Current POC
networking and device-access examples were exercised on CRI-O. See
[validation](../../docs/validation.md) for the tested scope and limits.

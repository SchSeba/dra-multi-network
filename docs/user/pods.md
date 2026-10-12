# Connect a Pod to a secondary network

Ask the cluster administrator for a Ready topology and its root DeviceClasses.
Your namespace needs normal Pod and ResourceClaim/ResourceClaimTemplate access;
you do not configure NetworkPlugins, exposure policies or topology revisions.

A ResourceClaim selects every allocation root of one immutable topology revision.
Use `Exactly`, `allocationMode: ExactCount`, `count: 1` for each root. This POC
supports one project topology claim per Pod. It rejects incomplete roots, roots
from different revisions, claim-supplied replacement driver config and unsupported
request forms. Additional unrelated non-project claims are handled separately.

## Single-root Pod

The [single-VF example](../../examples/single-vf/README.md) resolves the current
class and substitutes it into this pattern:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: secondary-network
  namespace: my-app
spec:
  spec:
    devices:
      requests:
      - name: endpoint
        exactly:
          deviceClassName: ADMIN_SUPPLIED_DEVICECLASS
          allocationMode: ExactCount
          count: 1
---
apiVersion: v1
kind: Pod
metadata:
  name: app
  namespace: my-app
spec:
  resourceClaims:
  - name: network
    resourceClaimTemplateName: secondary-network
  containers:
  - name: app
    image: quay.io/schseba/toolbox:latest
    command: [sleep, infinity]
    resources:
      claims:
      - name: network
```

Replace the class and create the namespace before applying. The template creates
a per-Pod claim. You can instead create a ResourceClaim with the same devices
spec and reference it using `resourceClaimName`; manage its deletion explicitly.
Application users should receive/read existing classes rather than edit them.

The fail-closed admission service adds required NRI plugin enforcement and node
placement for the exact revision. Do not remove those fields or add bootstrap
bypass annotations. You do not need a Multus attachment annotation or a CNI JSON
configuration. The primary interface remains available from the primary CNI.

## Two-root bond/VLAN topology

For [two VFs → bond → VLANs](../../examples/bond-vlan/README.md), the claim has two
requests, each with its corresponding generated class:

```yaml
requests:
- name: primary
  exactly:
    deviceClassName: ADMIN_LEFT_CLASS
    allocationMode: ExactCount
    count: 1
- name: backup
  exactly:
    deviceClassName: ADMIN_RIGHT_CLASS
    allocationMode: ExactCount
    count: 1
```

Requesting only one root does not produce half the network. Both roots must fit
on one eligible node. Operation outputs such as `bond0` and VLAN interfaces are
created by NRI, not additional claim requests. Use a new template name for a new
revision; ResourceClaimTemplate content is immutable. The example names templates
with the revision digest to avoid mutating an old template.

## Containers, device access and IP addresses

Containers that consume device access must list the Pod claim in
`resources.claims`. This applies to init containers too. For request-specific
consumption use the supported request selector under the claim reference;
access is scoped to the allocated request/function. A sibling container that
does not consume a VFIO/RDMA request does not receive those device nodes. VFIO
uses scoped CDI, and RDMA uses NRI runtime adjustments. These profiles do not
turn the whole Pod into a privileged workload.

Network interfaces live in the Pod network namespace and are consequently
visible to containers that share it. `resources.claims` scopes device access;
it is not per-container network namespace isolation. Changing link state or
assigning IPs inside the Pod requires `NET_ADMIN`; ping may require `NET_RAW`.
The starter Pod includes these capabilities only for experimentation.

The optional [IPAM example](../../examples/ipam/README.md) adds IPv4 addresses
from a scheduler-accounted subnet. The POC does not include new default routes, DNS or
NetworkPolicy enforcement for secondary interfaces. For an isolated lab:

```bash
kubectl -n multi-network-example exec single-vf -- \
  ip address add 192.168.211.10/24 dev secondary0
```

Choose site-appropriate addresses. This command does not establish connectivity
unless the other endpoint/fabric is also configured.

## Deployments

Put the same `resourceClaims` and container `resources.claims` fields in a
Deployment's `spec.template.spec`, using a ResourceClaimTemplate. Each replica
then receives its own claim/devices. Ensure the cluster has enough eligible VFs
or shared capacity for the replica count. Do not point several independently
running Pods at one exclusive manually created claim.

## Observe and clean up

```bash
kubectl -n my-app get pods,resourceclaims
kubectl -n my-app describe pod app
kubectl -n my-app get networkattachmentstatuses -o yaml
kubectl -n my-app exec app -- ip -d link show
```

Start with Pod scheduling events and claim allocation if Pending. If sandbox
creation fails, inspect the attachment error and ask the administrator to check
node runtime/driver logs. [Operations](../operations.md) maps common failures.

Delete consuming Pods first, wait for attachment cleanup, then delete manually
owned claims/templates. Administrator policies/topologies can be removed after
all their consumers drain. Never force-remove finalizers or node journals to
free a device. Every [example README](../../examples/README.md) includes its
cleanup commands and notes whether it shares administrator objects.

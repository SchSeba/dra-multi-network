#!/usr/bin/env bash
# Manual lab acceptance: only the spare secondary VF, no policy changes.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/link-config"
namespace=multi-network-link-config-test
for resource in "namespace/$namespace" networktopology/labeled-vlan networkplugin/labeled-vlan; do
  if kubectl get "$resource" >/dev/null 2>&1; then echo "ERROR: test resource exists: $resource" >&2; exit 1; fi
done
kubectl get resourceclaims -A -o json | python3 -c '
import json,sys
assert not any(r["driver"]=="dra-multi-network.schseba.github.io" and r["device"].startswith("0000-29-11-0-") for c in json.load(sys.stdin)["items"] for r in c.get("status",{}).get("allocation",{}).get("devices",{}).get("results",[])),"Spare VF 0000:29:11.0 is already allocated"
'
cleanup() {
  kubectl delete namespace "$namespace" --ignore-not-found --wait=true --timeout=180s
  kubectl delete networktopology labeled-vlan --ignore-not-found --wait=true
  kubectl delete networkplugin labeled-vlan --ignore-not-found --wait=false
}
trap cleanup EXIT
kubectl apply -f plugin.yaml -f topology.yaml
kubectl wait --for=condition=Ready networktopology/labeled-vlan --timeout=120s
class=$(kubectl get networktopology labeled-vlan -o jsonpath='{.status.rootClasses[0].className}')
revision=$(kubectl get networktopology labeled-vlan -o jsonpath='{.status.revisionRef.name}')
for mac in 00:00:00:00:00:00 01:42:ac:11:00:02; do
  denial=$(mktemp)
  if kubectl get networktopology labeled-vlan -o json | python3 -c '
import json,sys
t=json.load(sys.stdin); t={"apiVersion":t["apiVersion"],"kind":t["kind"],"metadata":{"name":"denied-link-config"},"spec":t["spec"]}
t["spec"]["steps"][1]["config"]["mac"]=sys.argv[1]; print(json.dumps(t))
' "$mac" | kubectl create --dry-run=server -f - > "$denial" 2>&1; then
    cat "$denial"; rm -f "$denial"; echo 'ERROR: unsafe MAC admitted' >&2; exit 1
  fi
  cat "$denial"; grep -q E_LINK_MAC "$denial"; rm -f "$denial"
done
echo 'PASS: admission rejects zero and multicast Ethernet MACs'
kubectl create namespace "$namespace"
cat <<YAML | kubectl apply -f -
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata: {name: labeled-vlan, namespace: $namespace}
spec:
  spec:
    devices:
      requests:
      - name: secondary
        exactly: {deviceClassName: $class, allocationMode: ExactCount, count: 1}
---
apiVersion: v1
kind: Pod
metadata:
  name: labeled-vlan
  namespace: $namespace
  annotations: {required-plugins.noderesource.dev: '["dra-multi-network"]'}
spec:
  terminationGracePeriodSeconds: 1
  nodeSelector: {dra-multi-network.schseba.github.io/ready: "true"}
  resourceClaims: [{name: secondary, resourceClaimTemplateName: labeled-vlan}]
  initContainers:
  - name: verify-network
    image: quay.io/schseba/toolbox:latest
    securityContext: {capabilities: {add: [NET_ADMIN, NET_RAW]}}
    command: [/bin/bash, -ec, 'test "\$(cat /sys/class/net/tenant100/address)" = 02:42:ac:11:00:02; grep -Eq "^dra-mn:[^;]+;tenant100/app:blue$" /sys/class/net/tenant100/ifalias; ip -d link show tenant100']
    resources: {claims: [{name: secondary}]}
  containers:
  - name: app
    image: quay.io/schseba/toolbox:latest
    securityContext: {capabilities: {add: [NET_ADMIN, NET_RAW]}}
    command: [sleep, infinity]
    resources: {claims: [{name: secondary}]}
YAML
kubectl -n "$namespace" wait --for=condition=Ready pod/labeled-vlan --timeout=180s
kubectl -n "$namespace" exec labeled-vlan -c app -- ip -j -d link show tenant100 | python3 -c '
import json,sys
l=json.load(sys.stdin)[0]
assert l["address"]=="02:42:ac:11:00:02" and l["mtu"]==1500 and "UP" in l["flags"],l
assert l["ifalias"].startswith("dra-mn:") and l["ifalias"].endswith(";tenant100/app:blue"),l
assert l["linkinfo"]["info_kind"]=="vlan" and l["linkinfo"]["info_data"]["id"]==100,l
print("PASS: allocated VF exports configured VLAN with approved MAC/label and preserved ownership identity")
'
kubectl -n "$namespace" get pod labeled-vlan -o json | python3 -c 'import json,sys; p=json.load(sys.stdin); assert p["status"]["initContainerStatuses"][0]["state"]["terminated"]["exitCode"]==0; print("PASS: MAC/alias were ready before the init container started")'
kubectl -n "$namespace" get networkattachmentstatuses -o json | python3 -c 'import json,sys; a=json.load(sys.stdin)["items"]; assert len(a)==1 and a[0]["status"]["phase"]=="Ready" and {o["ifName"] for o in a[0]["status"]["outputs"]}=={"secondary0","tenant100"},a; print("PASS: exact attachment outputs Ready")'
cleanup
trap - EXIT
kubectl wait --for=delete "networkplugin/labeled-vlan" "networktopologyrevision/$revision" "deviceclass/$class" --timeout=180s
echo 'PASS: verified drain collected the descriptor, immutable revision and class; verify original VF state separately on the node'

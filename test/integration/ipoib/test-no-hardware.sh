#!/usr/bin/env bash
# Manual negative acceptance on the development VM, never a deployment hook.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/ipoib"
namespace=multi-network-ipoib-negative
if kubectl get namespace "$namespace" >/dev/null 2>&1; then
  echo "Test namespace already exists: $namespace" >&2; exit 1
fi
for topology in single-port-ipoib single-port-ib-verbs; do
  if kubectl get networktopology "$topology" >/dev/null 2>&1; then
    echo "Test topology already exists: $topology" >&2; exit 1
  fi
done
kubectl get resourceslices -o json | python3 -c '
import json,sys
driver="dra-multi-network.schseba.github.io"
for s in json.load(sys.stdin)["items"]:
 if s["spec"]["driver"]!=driver:continue
 for d in s["spec"].get("devices",[]):
  assert d.get("attributes",{}).get(driver+"/linkType",{}).get("string")!="infiniband",d
print("PASS: no published IB hardware; this is a negative acceptance test")
'
cleanup() {
  kubectl delete namespace "$namespace" --ignore-not-found --wait=true --timeout=180s
  kubectl delete networktopology single-port-ipoib single-port-ib-verbs --ignore-not-found --wait=true --timeout=180s
}
trap cleanup EXIT
kubectl apply -f topology.yaml -f verbs-topology.yaml
kubectl create namespace "$namespace"
for topology in single-port-ipoib single-port-ib-verbs; do
  kubectl wait --for=condition=Ready "networktopology/$topology" --timeout=120s
  class=$(kubectl get networktopology "$topology" -o jsonpath='{.status.rootClasses[0].className}')
  kubectl get deviceclass "$class" -o json | python3 -c '
import json,sys
c=json.load(sys.stdin)
assert ".linkType == \"infiniband\"" in c["spec"]["selectors"][0]["cel"]["expression"],c
print("PASS: official controller generated typed IB class",c["metadata"]["name"])
'
  kubectl apply -f - <<EOF
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata: {name: $topology, namespace: $namespace}
spec:
  spec:
    devices:
      requests:
      - name: rdma
        exactly: {deviceClassName: $class, count: 1}
---
apiVersion: v1
kind: Pod
metadata: {name: $topology, namespace: $namespace}
spec:
  terminationGracePeriodSeconds: 1
  resourceClaims:
  - name: secondary
    resourceClaimTemplateName: $topology
  containers:
  - name: app
    image: quay.io/schseba/toolbox:latest
    command: [sleep, infinity]
    resources:
      claims: [{name: secondary, request: rdma}]
EOF
  kubectl -n "$namespace" wait --for='jsonpath={.status.conditions[?(@.type=="PodScheduled")].reason}=Unschedulable' "pod/$topology" --timeout=120s
  kubectl -n "$namespace" get pod "$topology" -o json | python3 -c '
import json,sys
p=json.load(sys.stdin)
assert p["status"]["phase"]=="Pending" and not p["spec"].get("nodeName") and not p["status"].get("containerStatuses"),p
assert p["spec"]["affinity"]["nodeAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"],p
print("PASS: missing certified IB hardware blocks workload before sandbox/container creation")
'
done
kubectl -n "$namespace" get resourceclaims -o json | python3 -c '
import json,sys
claims=json.load(sys.stdin)["items"]
assert len(claims)==2 and all(not c.get("status",{}).get("allocation") for c in claims),claims
print("PASS: neither IB profile acquired an allocation on the Ethernet-only VM")
'

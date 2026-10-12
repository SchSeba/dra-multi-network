#!/usr/bin/env bash
# Manual acceptance test; deliberately separate from deployment.
# Requires free secondary VFs and the development VM's PCI addresses.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/shared"
namespace=multi-network-compatibility-test
kubectl apply -f ../bond-vlan/policy.yaml -f policies.yaml -f vf-policies.yaml -f topologies.yaml -f vf-topologies.yaml
if [[ -n ${DRA_SHARED_PROFILE_MANIFEST:-} ]]; then
  kubectl apply -f "$DRA_SHARED_PROFILE_MANIFEST"
fi
for topology in vf-macvlan vf-ipvlan vf-handoff shared-macvlan secondary-pf-handoff; do
  generation=$(kubectl get networktopology "$topology" -o jsonpath='{.metadata.generation}')
  kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" "networktopology/$topology" --timeout=120s
  kubectl wait --for=condition=Ready "networktopology/$topology" --timeout=120s
done
kubectl create namespace "$namespace"
trap 'kubectl delete namespace "$namespace" --wait=true --timeout=180s' EXIT
create() {
  local topology=$1 pod=$2 class revision
  class=$(kubectl get networktopology "$topology" -o jsonpath='{.status.rootClasses[0].className}')
  revision=$(kubectl get networktopology "$topology" -o jsonpath='{.status.revisionRef.digest}')
  sed -e "s/ROOT_CLASS/$class/" -e "s/CLAIM_TEMPLATE/$topology-${revision:0:16}/g" \
      -e "s/POD_NAME/$pod/" -e "s/multi-network-example/$namespace/g" workload.yaml | kubectl apply -f -
}
ready() { kubectl -n "$namespace" wait --for=condition=Ready "pod/$1" --timeout=180s; }
exec_app() { local pod=$1; shift; kubectl -n "$namespace" exec "$pod" -c app -- "$@"; }
allocation() {
  local claim
  claim=$(kubectl -n "$namespace" get pod "$1" -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
  kubectl -n "$namespace" get resourceclaim "$claim" -o json
}
pending() {
  kubectl -n "$namespace" wait --for='jsonpath={.status.conditions[?(@.type=="PodScheduled")].reason}=Unschedulable' "pod/$1" --timeout=120s
  allocation "$1" | python3 -c 'import json,sys; c=json.load(sys.stdin); assert not c.get("status",{}).get("allocation"),c'
}

create shared-macvlan macvlan
create vf-ipvlan ipvlan
ready macvlan
ready ipvlan
allocation macvlan | python3 -c 'import json,sys; r=json.load(sys.stdin)["status"]["allocation"]["devices"]["results"]; assert len(r)==1 and r[0]["device"]=="0000-29-00-0-macvlan" and r[0].get("shareID"),r'
allocation ipvlan | python3 -c 'import json,sys; r=json.load(sys.stdin)["status"]["allocation"]["devices"]["results"]; assert len(r)==1 and r[0]["device"]=="0000-29-10-2-ipvlan" and r[0].get("shareID"),r'
exec_app macvlan ip -d link show secondary0
exec_app ipvlan ip -d link show secondary0
exec_app macvlan ip address replace 192.168.214.10/24 dev secondary0
exec_app ipvlan ip address replace 192.168.214.11/24 dev secondary0
exec_app macvlan ping -I secondary0 -c 3 -W 2 192.168.214.11
exec_app ipvlan ping -I secondary0 -c 3 -W 2 192.168.214.10
echo 'PASS: PF macvlan and VF ipvlan coexist and exchange traffic on different parents in one PF family'
kubectl -n "$namespace" delete pods macvlan ipvlan --wait=true
kubectl -n "$namespace" wait --for=delete resourceclaims --all --timeout=120s

create vf-handoff exclusive-vf
ready exclusive-vf
allocation exclusive-vf | python3 -c 'import json,sys; r=json.load(sys.stdin)["status"]["allocation"]["devices"]["results"]; assert len(r)==1 and r[0]["device"]=="0000-29-10-0-kernel" and not r[0].get("shareID"),r'
create vf-macvlan blocked-macvlan
pending blocked-macvlan
echo 'PASS: exclusive SR-IOV VF blocks macvlan on that same VF before allocation'
create secondary-pf-handoff blocked-pf
pending blocked-pf
echo 'PASS: allocated VF blocks handoff of its PF'
kubectl -n "$namespace" delete pod exclusive-vf --wait=true
ready blocked-macvlan
allocation blocked-macvlan | python3 -c 'import json,sys; r=json.load(sys.stdin)["status"]["allocation"]["devices"]["results"]; assert r[0]["device"]=="0000-29-10-0-macvlan" and r[0].get("shareID"),r'
echo 'PASS: releasing the VF allows the blocked macvlan allocation to become Ready'
kubectl -n "$namespace" delete pod blocked-macvlan --wait=true
ready blocked-pf
create vf-handoff blocked-vf
pending blocked-vf
echo 'PASS: PF handoff blocks its child VF'
kubectl -n "$namespace" delete pod blocked-pf --wait=true
ready blocked-vf
echo 'PASS: releasing the PF restores VF allocation'

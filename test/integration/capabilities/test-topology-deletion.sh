#!/usr/bin/env bash
# Manual controller/admission acceptance. A separate scheduler name keeps the
# temporary Pod unbound; this test never allocates or mutates hardware.
set -euo pipefail
name=capability-deletion-test
namespace=multi-network-retirement-test
node=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
if kubectl get networktopology "$name" >/dev/null 2>&1; then
  echo 'ERROR: topology test already exists' >&2; exit 1
fi
if kubectl get namespace "$namespace" >/dev/null 2>&1; then
  echo 'ERROR: retirement test namespace already exists' >&2; exit 1
fi
before=$(mktemp -d)
cleanup() {
  kubectl delete networktopology "$name" --ignore-not-found --wait=true
  kubectl delete namespace "$namespace" --ignore-not-found --wait=true --timeout=120s
  rm -rf "$before"
}
trap cleanup EXIT
kubectl get networktopology kernel-rebind -o json | python3 -c 'import json,sys; t=json.load(sys.stdin); print(json.dumps({"apiVersion":t["apiVersion"],"kind":t["kind"],"metadata":{"name":sys.argv[1]},"spec":t["spec"]}))' "$name" | kubectl apply -f -
kubectl wait --for=condition=Ready "networktopology/$name" --timeout=120s
revision=$(kubectl get networktopology "$name" -o jsonpath='{.status.revisionRef.name}')
digest=$(kubectl get networktopology "$name" -o jsonpath='{.status.revisionRef.digest}')
class=$(kubectl get networktopology "$name" -o jsonpath='{.status.rootClasses[0].className}')
label="dra-multi-network.schseba.github.io/revision-$(python3 -c 'import hashlib,json,sys; print(hashlib.sha256(json.dumps(sys.argv[1]).encode()).hexdigest()[:32])' "$digest")"
eligible() {
  kubectl get node "$node" -o json | python3 -c 'import json,sys; assert (json.load(sys.stdin)["metadata"].get("labels",{}).get(sys.argv[1])=="true")== (sys.argv[2]=="true")' "$label" "$1"
}
check_eligibility() {
  local expected=$1 found=false attempt
  for attempt in $(seq 1 45); do
    if eligible "$expected" >/dev/null 2>&1; then found=true; break; fi
    sleep 2
  done
  test "$found" = true
}
check_eligibility true
kubectl create namespace "$namespace"
cat > "$before/template.json" <<JSON
{"apiVersion":"resource.k8s.io/v1","kind":"ResourceClaimTemplate","metadata":{"name":"retained-template","namespace":"$namespace"},"spec":{"spec":{"devices":{"requests":[{"name":"endpoint","exactly":{"deviceClassName":"$class","count":1}}]}}}}
JSON
kubectl create -f "$before/template.json"
cat > "$before/pod.json" <<JSON
{"apiVersion":"v1","kind":"Pod","metadata":{"name":"pending","namespace":"$namespace"},"spec":{"schedulerName":"retirement-test","resourceClaims":[{"name":"network","resourceClaimTemplateName":"retained-template"}],"containers":[{"name":"app","image":"registry.k8s.io/pause:3.10","resources":{"claims":[{"name":"network"}]}}]}}
JSON
kubectl create -f "$before/pod.json"
uid=$(kubectl -n "$namespace" get pod pending -o jsonpath='{.metadata.uid}')
kubectl -n "$namespace" get pods,resourceclaims -o json | python3 -c 'import json,sys; items=json.load(sys.stdin)["items"]; assert all(not x.get("spec",{}).get("nodeName") if x["kind"]=="Pod" else not x.get("status",{}).get("allocation") for x in items),items'
kubectl get networktopologyrevision "$revision" -o json > "$before/revision.json"
kubectl get deviceclass "$class" -o json > "$before/class.json"
kubectl delete networktopology "$name" --wait=true
# Test admission immediately, before waiting for eventual label withdrawal.
python3 - "$before" "$namespace" "$node" "$uid" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
template=json.loads((root/'template.json').read_text())
claim={'apiVersion':'resource.k8s.io/v1','kind':'ResourceClaim','metadata':{'name':'denied','namespace':sys.argv[2]},'spec':template['spec']['spec']}
template['metadata']['name']='denied'
pod=json.loads((root/'pod.json').read_text()); pod['metadata']['name']='denied'
binding={'apiVersion':'v1','kind':'Binding','metadata':{'name':'pending','namespace':sys.argv[2],'uid':sys.argv[4]},'target':{'apiVersion':'v1','kind':'Node','name':sys.argv[3]}}
for name,obj in [('claim',claim),('template-denied',template),('pod-denied',pod),('binding',binding)]:
 (root/(name+'.json')).write_text(json.dumps(obj))
PY
for kind in claim template-denied pod-denied; do
  if kubectl create --dry-run=server -f "$before/$kind.json" > "$before/denial" 2>&1; then
    echo "ERROR: $kind admitted after topology deletion" >&2; exit 1
  fi
  cat "$before/denial"
  grep -Eq 'E_(TOPOLOGY|REVISION)_RETIRED' "$before/denial"
done
if kubectl create --raw "/api/v1/namespaces/$namespace/pods/pending/binding" -f "$before/binding.json" > "$before/denial" 2>&1; then
  echo 'ERROR: binding admitted after topology deletion' >&2; exit 1
fi
cat "$before/denial"
grep -Eq 'E_(TOPOLOGY|REVISION)_RETIRED' "$before/denial"
# Metadata updates and deletion remain available to existing consumers.
kubectl -n "$namespace" annotate pod pending retirement-tested=true
kubectl -n "$namespace" annotate resourceclaimtemplate retained-template retirement-tested=true
echo 'PASS: API denied new claims, templates, Pods and direct binding for the retired topology; existing metadata updates remained available'
check_eligibility false
kubectl get networktopologyrevision "$revision" -o json > "$before/revision-after.json"
kubectl get deviceclass "$class" -o json > "$before/class-after.json"
python3 - "$before" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
for kind in ('revision','class'):
 before,after=[json.loads((root/name).read_text()) for name in (kind+'.json',kind+'-after.json')]
 assert before['metadata']['uid']==after['metadata']['uid'] and before['spec']==after['spec'],(before,after)
print('PASS: topology deletion withdrew revision eligibility while retaining the original immutable revision and class for cleanup authority')
PY

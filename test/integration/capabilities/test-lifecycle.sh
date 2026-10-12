#!/usr/bin/env bash
# Manual acceptance on the development VM. Stops only the exact node-agent
# process temporarily; existing Linux networking and its journal stay intact.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$(dirname "$0")"
namespace=multi-network-capability-test
system_namespace=dra-multi-network
vm=multi-network-ctlplane-0
node=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
class=$(kubectl get networktopology kernel-rebind -o jsonpath='{.status.rootClasses[0].className}')
digest=$(kubectl get networktopology kernel-rebind -o jsonpath='{.status.revisionRef.digest}')
test -n "$class"
label="dra-multi-network.schseba.github.io/revision-$(python3 -c 'import hashlib,json,sys; print(hashlib.sha256(json.dumps(sys.argv[1]).encode()).hexdigest()[:32])' "$digest")"
agent=$(kubectl -n "$system_namespace" get pods -l app.kubernetes.io/component=kubeletplugin -o jsonpath='{.items[0].metadata.name}')
container_id=$(kubectl -n "$system_namespace" get pod "$agent" -o jsonpath='{.status.containerStatuses[?(@.name=="plugin")].containerID}')
container_id=${container_id#cri-o://}
[[ "$container_id" =~ ^[a-f0-9]{64}$ ]]
if kubectl get namespace "$namespace" >/dev/null 2>&1; then
  echo 'ERROR: capability test namespace already exists' >&2; exit 1
fi
before=$(mktemp -d)
paused=false
cleanup() {
  if test "$paused" = true; then
    kcli ssh "$vm" "sudo /usr/libexec/crio/crun --root /run/crun --systemd-cgroup kill $container_id CONT" </dev/null
  fi
  kubectl delete namespace "$namespace" --ignore-not-found --wait=true --timeout=120s
  rm -rf "$before"
}
trap cleanup EXIT
eligible() {
  kubectl get node "$node" -o json | python3 -c 'import json,sys; assert (json.load(sys.stdin)["metadata"].get("labels",{}).get(sys.argv[1])=="true")== (sys.argv[2]=="true")' "$label" "$1"
}
available=false
for attempt in $(seq 1 45); do
  if eligible true >/dev/null 2>&1; then available=true; break; fi
  sleep 2
done
test "$available" = true
kubectl get node "$node" -o json | python3 -c '
import json,sys
node=json.load(sys.stdin)
report=json.loads(node["metadata"]["annotations"]["dra-multi-network.schseba.github.io/capabilities"])
assert report["nodeUID"]==node["metadata"]["uid"] and report["bootID"]==node["status"]["nodeInfo"]["bootID"],report
assert report["ready"] and report["imageID"] and report["containerID"] and report["executor"] and report["pluginDigests"] and report["profiles"],report
print("PASS: bounded capability report includes actual node/boot/container/build identity and supported plugin/executor/hardware profiles")
'
kubectl create namespace "$namespace"
paused=true
kcli ssh "$vm" "sudo /usr/libexec/crio/crun --root /run/crun --systemd-cgroup kill $container_id STOP" </dev/null
expired=false
for attempt in $(seq 1 45); do
  if eligible false >/dev/null 2>&1; then expired=true; break; fi
  sleep 2
done
test "$expired" = true
echo 'PASS: expired agent report removed revision eligibility'
python3 - "$namespace" "$class" <<'PY' | kubectl apply -f -
import json,sys
namespace,cls=sys.argv[1:]
print(json.dumps({"apiVersion":"resource.k8s.io/v1","kind":"ResourceClaimTemplate","metadata":{"name":"capability","namespace":namespace},"spec":{"spec":{"devices":{"requests":[{"name":"endpoint","exactly":{"deviceClassName":cls,"allocationMode":"ExactCount","count":1}}]}}}}))
print('---')
print(json.dumps({"apiVersion":"v1","kind":"Pod","metadata":{"name":"capability","namespace":namespace},"spec":{"terminationGracePeriodSeconds":1,"resourceClaims":[{"name":"network","resourceClaimTemplateName":"capability"}],"containers":[{"name":"app","image":"quay.io/schseba/toolbox:latest","command":["sleep","infinity"],"resources":{"claims":[{"name":"network"}]}}]}}))
PY
kubectl -n "$namespace" wait --for='jsonpath={.status.conditions[?(@.type=="PodScheduled")].reason}=Unschedulable' pod/capability --timeout=120s
kubectl -n "$namespace" get pod capability -o json | python3 -c '
import json,sys
pod=json.load(sys.stdin)
assert not pod["spec"].get("nodeName"),pod
assert json.loads(pod["metadata"]["annotations"]["required-plugins.noderesource.dev"])==["dra-multi-network"],pod
terms=pod["spec"]["affinity"]["nodeAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"]["nodeSelectorTerms"]
assert terms and all(any(e["key"]==sys.argv[1] and e["operator"]=="In" and e["values"]==["true"] for e in t["matchExpressions"]) for t in terms),terms
print("PASS: admission injected required NRI participation and revision affinity; unavailable agent blocked scheduling")
' "$label"
if kubectl label node "$node" "$label=true" --overwrite > "$before/label-error" 2>&1; then
  echo 'ERROR: tenant wrote protected revision label' >&2; exit 1
fi
grep 'E_NODE_CAPABILITY_AUTHORITY' "$before/label-error"
if kubectl annotate node "$node" dra-multi-network.schseba.github.io/capabilities- > "$before/report-error" 2>&1; then
  echo 'ERROR: tenant removed protected capability report' >&2; exit 1
fi
grep 'E_NODE_CAPABILITY_AUTHORITY' "$before/report-error"
uid=$(kubectl -n "$namespace" get pod capability -o jsonpath='{.metadata.uid}')
python3 - "$namespace" "$node" "$uid" <<'PY' > "$before/binding.json"
import json,sys
namespace,node,uid=sys.argv[1:]
print(json.dumps({"apiVersion":"v1","kind":"Binding","metadata":{"name":"capability","namespace":namespace,"uid":uid},"target":{"kind":"Node","name":node}}))
PY
if kubectl create --raw "/api/v1/namespaces/$namespace/pods/capability/binding" -f "$before/binding.json" > "$before/binding-error" 2>&1; then
  echo 'ERROR: direct binding bypassed revision eligibility' >&2; exit 1
fi
grep 'E_NODE_CAPABILITY' "$before/binding-error"
echo 'PASS: tenant metadata writes and direct binding could not bypass unavailable revision capability'
kcli ssh "$vm" "sudo /usr/libexec/crio/crun --root /run/crun --systemd-cgroup kill $container_id CONT" </dev/null
paused=false
refreshed=false
for attempt in $(seq 1 45); do
  if eligible true >/dev/null 2>&1; then refreshed=true; break; fi
  sleep 2
done
test "$refreshed" = true
kubectl -n "$namespace" wait --for=condition=Ready pod/capability --timeout=180s
kubectl -n "$namespace" exec capability -c app -- test -d /sys/class/net/secondary0
echo 'PASS: refreshed report restored eligibility and the blocked workload became Ready'
annotation_denied() {
  local key=$1 value=$2 reason=$3
  python3 - "$before/binding.json" "$key" "$value" <<'PY' > "$before/annotation-binding.json"
import json,sys
binding=json.load(open(sys.argv[1]))
binding['metadata']['annotations']={sys.argv[2]:sys.argv[3]}
print(json.dumps(binding))
PY
  if kubectl create --raw "/api/v1/namespaces/$namespace/pods/capability/binding" -f "$before/annotation-binding.json" > "$before/annotation-error" 2>&1; then
    echo 'ERROR: binding changed protected runtime annotations' >&2; exit 1
  fi
  if ! grep "$reason" "$before/annotation-error"; then cat "$before/annotation-error" >&2; exit 1; fi
}
annotation_denied required-plugins.noderesource.dev '[]' E_REQUIRED_PLUGIN
annotation_denied cdi.k8s.io/devices 'dra-multi-network.schseba.github.io/vf=unclaimed' E_DEVICE_ACCESS_ORIGIN
annotation_denied dra-multi-network.schseba.github.io/bootstrap-nri-toleration true E_BOOTSTRAP_AUTHORITY
echo 'PASS: binding annotations cannot remove NRI requirements, inject project CDI devices or grant bootstrap toleration'
if kubectl api-resources --api-group='' -o name | grep -qx bindings; then
  # Kubernetes 1.37's legacy collection endpoint clears the Pod UID. It
  # cannot supply the identity precondition required for project workloads.
  if kubectl create --raw "/api/v1/namespaces/$namespace/bindings" -f "$before/binding.json" > "$before/legacy-error" 2>&1; then
    echo 'ERROR: legacy binding bypassed exact Pod identity' >&2; exit 1
  fi
  if ! grep E_BINDING_POD_IDENTITY "$before/legacy-error"; then cat "$before/legacy-error" >&2; exit 1; fi
  echo 'PASS: legacy binding without preserved Pod UID was rejected'
fi
claim=$(kubectl -n "$namespace" get pod capability -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n "$namespace" delete pod capability --wait=true
kubectl -n "$namespace" wait --for=delete "resourceclaim/$claim" --timeout=120s
driver=$(kcli ssh "$vm" 'readlink /sys/bus/pci/devices/0000:29:11.0/driver' </dev/null)
test "${driver##*/}" = igbvf
test "$(kcli ssh "$vm" 'cat /sys/bus/pci/devices/0000:29:11.0/driver_override' </dev/null)" = '(null)'
kubectl -n "$system_namespace" get pod "$agent" -o json | python3 -c 'import json,sys; p=json.load(sys.stdin); assert all(s["ready"] and s["restartCount"]==0 for s in p["status"]["containerStatuses"]); print("PASS: agent resumed with zero restarts; test allocation restored original VF binding/override")'

#!/usr/bin/env bash
# Manual single-node lab acceptance; uses only spare secondary VF 29:11.0.
# The agent pause is temporary and targets its exact verified container ID.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$(dirname "$0")"
namespace=multi-network-gc-test
name=gc-spare-vf
plugin=gc-programmable-vf
vm=multi-network-ctlplane-0
system_namespace=dra-multi-network
node=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
for resource in "namespace/$namespace" "networktopology/$name" "deviceexposurepolicy/$name" "networkplugin/$plugin"; do
  if kubectl get "$resource" >/dev/null 2>&1; then echo "ERROR: test resource already exists: $resource" >&2; exit 1; fi
done
before=$(mktemp -d)
paused=false
container_id=''
cleanup() {
  if test "$paused" = true; then
    kcli ssh "$vm" "sudo /usr/libexec/crio/crun --root /run/crun --systemd-cgroup kill $container_id CONT" </dev/null
    paused=false
  fi
  kubectl delete namespace "$namespace" --ignore-not-found --wait=true --timeout=180s
  kubectl delete networktopology "$name" --ignore-not-found --wait=true
  kubectl delete deviceexposurepolicy "$name" --ignore-not-found --wait=true
  kubectl delete networkplugin "$plugin" --ignore-not-found --wait=false
  "$repo_dir/examples/bond-vlan/apply.sh"
  rm -rf "$before"
}
trap cleanup EXIT
kubectl -n multi-network-example delete pods network-mvp network-mvp-peer --ignore-not-found --wait=true --timeout=120s
kubectl -n multi-network-example wait --for=delete resourceclaims --all --timeout=180s
kubectl get resourceclaims -A -o json | python3 -c 'import json,sys; assert not any(r["driver"]=="dra-multi-network.schseba.github.io" for c in json.load(sys.stdin)["items"] for r in c.get("status",{}).get("allocation",{}).get("devices",{}).get("results",[])),"Drain other project claims before changing publication"'
kubectl apply -f "$repo_dir/examples/root-effect-program/plugin.yaml"
kubectl get networkplugin programmable-sriov -o json | python3 -c '
import json,sys
p=json.load(sys.stdin)
print(json.dumps({"apiVersion":p["apiVersion"],"kind":p["kind"],"metadata":{"name":sys.argv[1]},"spec":p["spec"]}))
' "$plugin" | kubectl apply -f -
kubectl wait --for='jsonpath={.metadata.finalizers[0]}=networking.dra-multi-network.schseba.github.io/retain-contract' "networkplugin/$plugin" --timeout=120s
cat <<YAML | kubectl apply -f -
apiVersion: networking.dra-multi-network.schseba.github.io/v1alpha1
kind: DeviceExposurePolicy
metadata: {name: $name}
spec:
  priority: 100
  selector: {kind: VF, pciAddresses: ["0000:29:11.0"]}
  action: Expose
  exposure:
    persona: gc
    plugin: $plugin
    mode: kernel
    allowedDrivers: [igbvf]
    effectPermissions: [linux/driver-bind/v1, linux/link-move/v1, linux/link-configure/v1]
---
apiVersion: networking.dra-multi-network.schseba.github.io/v1alpha1
kind: NetworkTopology
metadata: {name: $name}
spec:
  effectBudget: {allow: [linux/driver-bind/v1, linux/link-move/v1, linux/link-configure/v1]}
  steps:
  - id: endpoint
    kind: Allocation
    pluginRef: {name: $plugin}
    selectors: ['device.attributes["dra-multi-network.schseba.github.io"].pciAddress == "0000:29:11.0"']
    config: {driver: igbvf, selectedDriver: igbvf, mode: kernel, linkType: ethernet, ifName: secondary0, mtu: 1500, up: true, rdmaAccess: false}
YAML
kubectl wait --for=condition=Ready "networktopology/$name" --timeout=120s
revision=$(kubectl get networktopology "$name" -o jsonpath='{.status.revisionRef.name}')
revision_uid=$(kubectl get networktopology "$name" -o jsonpath='{.status.revisionRef.uid}')
class=$(kubectl get networktopology "$name" -o jsonpath='{.status.rootClasses[0].className}')
kubectl get networktopologyrevision "$revision" -o json > "$before/revision.json"
kubectl get deviceclass "$class" -o json > "$before/class.json"
kubectl create namespace "$namespace"
sed -e "s/ROOT_CLASS/$class/" -e 's/CLAIM_TEMPLATE/gc-endpoint/g' -e 's/POD_NAME/gc-vf/g' -e "s/multi-network-example/$namespace/g" "$repo_dir/examples/shared/workload.yaml" | kubectl apply -f -
kubectl -n "$namespace" wait --for=condition=Ready pod/gc-vf --timeout=180s
claim=$(kubectl -n "$namespace" get pod gc-vf -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n "$namespace" get resourceclaim "$claim" -o json | python3 -c '
import json,sys
r=json.load(sys.stdin)["status"]["allocation"]["devices"]["results"]
assert len(r)==1 and r[0]["device"]=="0000-29-11-0-gc" and not r[0].get("shareID"),r
'
kubectl delete networktopology "$name" --wait=true
kubectl delete deviceexposurepolicy "$name" --wait=true
kubectl delete networkplugin "$plugin" --wait=false
for attempt in $(seq 1 45); do
  if kubectl get networktopologyrevision "$revision" -o json | python3 -c 'import json,sys; assert json.load(sys.stdin)["metadata"].get("annotations",{}).get("dra-multi-network.schseba.github.io/retirement")' >/dev/null 2>&1; then break; fi
  sleep 2
done
kubectl get networktopologyrevision "$revision" -o json > "$before/retained-revision.json"
kubectl get deviceclass "$class" -o json > "$before/retained-class.json"
python3 - "$before" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
for kind in ('revision','class'):
 a=json.loads((root/f'{kind}.json').read_text()); b=json.loads((root/f'retained-{kind}.json').read_text())
 assert a['spec']==b['spec'] and a['metadata']['uid']==b['metadata']['uid']
 assert not b['metadata'].get('deletionTimestamp'),b
print('PASS: live VF allocation retains its immutable revision/class after source and policy deletion')
PY
kubectl get networkplugin "$plugin" -o json | python3 -c 'import json,sys; p=json.load(sys.stdin); assert p["metadata"].get("deletionTimestamp") and p["metadata"].get("finalizers"),p'
kubectl -n "$namespace" exec gc-vf -c app -- ip -d link show secondary0
kubectl get node "$node" -o json | python3 -c '
import json,sys
r=json.loads(json.load(sys.stdin)["metadata"]["annotations"]["dra-multi-network.schseba.github.io/capabilities"])
assert r["reservationsChecked"] and any(x["uid"]==sys.argv[1] for x in r.get("retainedRevisions",[])),r
assert sys.argv[2] in r.get("retainedPlugins",[]),r
print("PASS: node reports retained cleanup authority and descriptor; existing workload still has its interface")
' "$revision_uid" "$plugin"
kubectl -n "$namespace" delete pod gc-vf --wait=true
kubectl -n "$namespace" wait --for=delete "resourceclaim/$claim" --timeout=180s
drained=false
for attempt in $(seq 1 60); do
  if kubectl get node "$node" -o json | python3 -c 'import json,sys; r=json.loads(json.load(sys.stdin)["metadata"]["annotations"]["dra-multi-network.schseba.github.io/capabilities"]); assert any(x["uid"]==sys.argv[1] for x in r.get("drainedRevisions",[]))' "$revision_uid" >/dev/null 2>&1; then drained=true; break; fi
  sleep 2
done
test "$drained" = true
kubectl get networktopologyrevision "$revision" >/dev/null
kubectl get deviceclass "$class" >/dev/null
echo 'PASS: released VF has a durable node drain acknowledgement; the remaining claim template still retains its class'
agent=$(kubectl -n "$system_namespace" get pods -l app.kubernetes.io/component=kubeletplugin -o jsonpath='{.items[0].metadata.name}')
container_id=$(kubectl -n "$system_namespace" get pod "$agent" -o jsonpath='{.status.containerStatuses[?(@.name=="plugin")].containerID}')
container_id=${container_id#cri-o://}
[[ "$container_id" =~ ^[a-f0-9]{64}$ ]]
paused=true
kcli ssh "$vm" "sudo /usr/libexec/crio/crun --root /run/crun --systemd-cgroup kill $container_id STOP" </dev/null
# Settle an already-sent API patch, then wait beyond the report's 45s TTL.
# Compare elapsed time and an unchanged report; host and VM clocks may differ.
sleep 2
updated=$(kubectl get node "$node" -o json | python3 -c 'import json,sys; print(json.loads(json.load(sys.stdin)["metadata"]["annotations"]["dra-multi-network.schseba.github.io/capabilities"])["updated"])')
deadline=$((SECONDS+50))
while ((SECONDS < deadline)); do
  sleep 2
done
kubectl get node "$node" -o json | python3 -c 'import json,sys; assert json.loads(json.load(sys.stdin)["metadata"]["annotations"]["dra-multi-network.schseba.github.io/capabilities"])["updated"]==sys.argv[1]' "$updated"
kubectl -n "$namespace" delete resourceclaimtemplate gc-endpoint --wait=true
# Allow more than one GC reconcile with the now-unreferenced, expired report.
for attempt in $(seq 1 6); do
  kubectl get networktopologyrevision "$revision" >/dev/null
  kubectl get deviceclass "$class" >/dev/null
  kubectl get networkplugin "$plugin" >/dev/null
  sleep 2
done
echo 'PASS: unavailable node agent retains unreferenced revision, class and descriptor'
kcli ssh "$vm" "sudo /usr/libexec/crio/crun --root /run/crun --systemd-cgroup kill $container_id CONT" </dev/null
paused=false
kubectl -n "$system_namespace" wait --for=condition=Ready "pod/$agent" --timeout=180s
kubectl wait --for=delete "networktopologyrevision/$revision" "deviceclass/$class" "networkplugin/$plugin" --timeout=180s
echo 'PASS: healthy agent reconciliation allows exact generated class, revision and descriptor collection'
cleanup
trap - EXIT
"$repo_dir/test/integration/bond-vlan/test-traffic.sh"

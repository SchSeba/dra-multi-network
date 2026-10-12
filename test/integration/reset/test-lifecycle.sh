#!/usr/bin/env bash
# Manual acceptance; adding a new persona requires an allocation drain.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/reset"
namespace=multi-network-example
vm=multi-network-ctlplane-0
kubectl get resourceclaims -A -o json | python3 -c '
import json,sys
for claim in json.load(sys.stdin)["items"]:
 for result in claim.get("status",{}).get("allocation",{}).get("devices",{}).get("results",[]):
  assert result["driver"] != "dra-multi-network.schseba.github.io", "Drain project claims before this test"
'
before=$(mktemp -d)
cleanup() {
  kubectl -n "$namespace" delete pod kernel-reset-mvp --ignore-not-found --wait=true --timeout=120s
  rm -rf "$before"
}
trap cleanup EXIT
kcli ssh "$vm" 'ip -j link show eth11' </dev/null > "$before/link.json"
kcli ssh "$vm" 'cat /sys/bus/pci/devices/0000:29:11.0/driver_override' </dev/null > "$before/override"
kcli ssh "$vm" 'cat /sys/bus/pci/devices/0000:29:11.0/reset_method' </dev/null > "$before/methods"
kubectl apply -f policy.yaml -f topology.yaml
generation=$(kubectl get networktopology kernel-reset -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/kernel-reset --timeout=120s
kubectl wait --for=condition=Ready networktopology/kernel-reset --timeout=120s
class=$(kubectl get networktopology kernel-reset -o jsonpath='{.status.rootClasses[0].className}')
revision=$(kubectl get networktopology kernel-reset -o jsonpath='{.status.revisionRef.digest}')
kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/ROOT_CLASS/$class/" -e "s/CLAIM_TEMPLATE/kernel-reset-${revision:0:16}/g" -e 's/POD_NAME/kernel-reset-mvp/' ../shared/workload.yaml | kubectl apply -f -
kubectl -n "$namespace" wait --for=condition=Ready pod/kernel-reset-mvp --timeout=180s
driver=$(kcli ssh "$vm" 'readlink /sys/bus/pci/devices/0000:29:11.0/driver' </dev/null)
test "${driver##*/}" = igbvf
test "$(kcli ssh "$vm" 'cat /sys/bus/pci/devices/0000:29:11.0/driver_override' </dev/null)" = igbvf
kubectl -n "$namespace" exec kernel-reset-mvp -c app -- test -d /sys/class/net/secondary0
echo 'PASS: approved FLR and kernel binding prepared; rediscovered interface attached to the sandbox'
claim=$(kubectl -n "$namespace" get pod kernel-reset-mvp -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n "$namespace" delete pod kernel-reset-mvp --wait=true
kubectl -n "$namespace" wait --for=delete "resourceclaim/$claim" --timeout=120s
kcli ssh "$vm" 'cat /sys/bus/pci/devices/0000:29:11.0/driver_override' </dev/null > "$before/override-after"
cmp "$before/override" "$before/override-after"
kcli ssh "$vm" 'cat /sys/bus/pci/devices/0000:29:11.0/reset_method' </dev/null > "$before/methods-after"
cmp "$before/methods" "$before/methods-after"
kcli ssh "$vm" 'ip -j link show eth11' </dev/null > "$before/link-after.json"
python3 - "$before" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
original,restored=[json.loads((root/name).read_text())[0] for name in ("link.json","link-after.json")]
for field in ("ifname","address","mtu"):
 assert original[field]==restored[field],(field,original,restored)
assert ("UP" in original["flags"])==("UP" in restored["flags"])
assert not restored.get("ifalias"),restored
print("PASS: original reset methods, kernel override and interface state restored after attachment cleanup")
PY

kubectl delete deviceexposurepolicy secondary-vf-reset --wait=true

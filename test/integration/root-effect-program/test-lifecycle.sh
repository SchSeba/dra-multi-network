#!/usr/bin/env bash
# Manual acceptance on the example VM; drain existing project claims first.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/root-effect-program"
namespace=multi-network-example
vm=multi-network-ctlplane-0
kubectl get resourceclaims -A -o json | python3 -c '
import json,sys
for claim in json.load(sys.stdin)["items"]:
 for result in claim.get("status",{}).get("allocation",{}).get("devices",{}).get("results",[]):
  assert result["driver"] != "dra-multi-network.schseba.github.io", "Drain project claims before this test"
'
before=$(mktemp -d)
vfs=(0000:29:10.0 0000:29:10.2 0000:29:10.4 0000:29:10.6 0000:29:11.0)
cleanup() {
  kubectl -n "$namespace" delete pod network-mvp network-mvp-peer --ignore-not-found --wait=true --timeout=120s
  rm -rf "$before"
}
trap cleanup EXIT
for pci in "${vfs[@]}"; do
  kcli ssh "$vm" "cat /sys/bus/pci/devices/$pci/driver_override" </dev/null > "$before/$pci-override"
  kcli ssh "$vm" "readlink /sys/bus/pci/devices/$pci/driver" </dev/null > "$before/$pci-driver"
done
kcli ssh "$vm" 'ip -j link show' </dev/null > "$before/links.json"
./apply.sh
DRA_EXAMPLE_TOPOLOGY=programmable-vf-bond-vlan "$repo_dir/test/integration/bond-vlan/test-traffic.sh"
kubectl -n "$namespace" get resourceclaims -o json > "$before/claims.json"
python3 -c '
import json,sys
results=[r for c in json.load(sys.stdin)["items"] for r in c["status"]["allocation"]["devices"]["results"]]
assert len(results)==4 and len({r["device"] for r in results})==4,results
for r in results:
 assert r["device"].startswith("0000-29-") and r["device"].endswith("-programmed"),r
print("PASS: four distinct VFs selected using the programmable root contract")
' < "$before/claims.json"
mapfile -t allocated < <(python3 - "$before/claims.json" <<'PY_ALLOC'
import json,sys
for c in json.load(open(sys.argv[1]))["items"]:
 for r in c["status"]["allocation"]["devices"]["results"]:
  p=r["device"].split("-")
  print(f"{p[0]}:{p[1]}:{p[2]}.{p[3]}")
PY_ALLOC
)
for pci in "${allocated[@]}"; do
  test "$(kcli ssh "$vm" "cat /sys/bus/pci/devices/$pci/driver_override" </dev/null)" = igbvf
  driver=$(kcli ssh "$vm" "readlink /sys/bus/pci/devices/$pci/driver" </dev/null)
  test "${driver##*/}" = igbvf
done
echo "PASS: template NodePrepare installed the approved driver override on all four allocated VFs"
if [[ ${DRA_TEST_RESTART_AGENT:-0} == 1 ]]; then
  kubectl -n "$namespace" get pods network-mvp network-mvp-peer -o json > "$before/pods.json"
  agent=$(kubectl -n dra-multi-network get pod -l app.kubernetes.io/component=kubeletplugin -o jsonpath='{.items[0].metadata.name}')
  test -n "$agent"
  kubectl -n dra-multi-network delete pod "$agent" --wait=true
  kubectl -n dra-multi-network rollout status daemonset/dra-multi-network-kubeletplugin --timeout=180s
  kubectl -n "$namespace" get pods network-mvp network-mvp-peer -o json > "$before/pods-after.json"
  python3 - "$before" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
def identities(name):
 return sorted((p["metadata"]["uid"],c["name"],c["containerID"],c["restartCount"],c["ready"]) for p in json.loads((root/name).read_text())["items"] for c in p["status"]["containerStatuses"])
assert identities("pods.json")==identities("pods-after.json")
print("PASS: programmable allocation survives agent recovery without restarting either workload")
PY
  DRA_EXAMPLE_TOPOLOGY=programmable-vf-bond-vlan "$repo_dir/test/integration/bond-vlan/test-traffic.sh"
fi
mapfile -t claims < <(kubectl -n "$namespace" get pods network-mvp network-mvp-peer -o jsonpath='{range .items[*]}{.status.resourceClaimStatuses[0].resourceClaimName}{"\n"}{end}')
test "${#claims[@]}" = 2
kubectl -n "$namespace" delete pod network-mvp network-mvp-peer --wait=true
for claim in "${claims[@]}"; do
  kubectl -n "$namespace" wait --for=delete "resourceclaim/$claim" --timeout=120s
done
for pci in "${vfs[@]}"; do
  kcli ssh "$vm" "cat /sys/bus/pci/devices/$pci/driver_override" </dev/null > "$before/$pci-override-after"
  kcli ssh "$vm" "readlink /sys/bus/pci/devices/$pci/driver" </dev/null > "$before/$pci-driver-after"
  cmp "$before/$pci-override" "$before/$pci-override-after"
  cmp "$before/$pci-driver" "$before/$pci-driver-after"
done
kcli ssh "$vm" 'ip -j link show' </dev/null > "$before/links-after.json"
python3 - "$before" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
def links(name):
 return {x["ifname"]:x for x in json.loads((root/name).read_text()) if x["ifname"] in ("eth0","eth7","eth8","eth9","eth10","eth11")}
a,b=links("links.json"),links("links-after.json")
assert a.keys()==b.keys(),(a,b)
for name in a:
 for field in ("ifname","address","mtu"):
  assert a[name][field]==b[name][field],(name,field,a[name],b[name])
 assert ("UP" in a[name]["flags"])==("UP" in b[name]["flags"]),(name,a[name],b[name])
 assert a[name].get("ifalias","")==b[name].get("ifalias",""),(name,a[name],b[name])
print("PASS: recorded cleanup restored every secondary VF driver/override and original PF/VF link state")
PY
kubectl delete deviceexposurepolicy secondary-programmable-vfs --wait=true
trap - EXIT
rm -rf "$before"

#!/usr/bin/env bash
# Manual acceptance on the example VM; administrator must first drain and disable
# the secondary PF's VFs. This test does not reconfigure sriov_numvfs.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/reset"
namespace=multi-network-pf-reset-test
vm=multi-network-ctlplane-0
pci=0000:29:00.0
kubectl get resourceclaims -A -o json | python3 -c '
import json,sys
for claim in json.load(sys.stdin)["items"]:
 for result in claim.get("status",{}).get("allocation",{}).get("devices",{}).get("results",[]):
  assert result["driver"] != "dra-multi-network.schseba.github.io", "Drain project claims before this test"
'
test "$(kcli ssh "$vm" "cat /sys/bus/pci/devices/$pci/sriov_numvfs" </dev/null)" = 0
before=$(mktemp -d)
cleanup() {
  kubectl -n "$namespace" delete pod pf-reset-mvp --ignore-not-found --wait=true --timeout=120s
  rm -rf "$before"
}
trap cleanup EXIT
kcli ssh "$vm" 'ip -j link show eth0' </dev/null > "$before/link.json"
for attribute in driver_override reset_method; do
  kcli ssh "$vm" "cat /sys/bus/pci/devices/$pci/$attribute" </dev/null > "$before/$attribute"
done
kubectl apply -f - <<'YAML'
apiVersion: networking.dra-multi-network.schseba.github.io/v1alpha1
kind: DeviceExposurePolicy
metadata: {name: secondary-pf-reset}
spec:
  priority: 100
  action: Expose
  selector: {kind: PF, pciAddresses: ["0000:29:00.0"]}
  exposure:
    persona: reset
    plugin: host-cni-reset
    mode: kernel
    allowedDrivers: [igb]
    allowedResetMethods: [flr]
    effectPermissions: [linux/pci-reset/v1, linux/driver-bind/v1, linux/link-move/v1, linux/link-configure/v1]
---
apiVersion: networking.dra-multi-network.schseba.github.io/v1alpha1
kind: NetworkTopology
metadata: {name: pf-reset}
spec:
  effectBudget:
    allow: [linux/pci-reset/v1, linux/driver-bind/v1, linux/link-move/v1, linux/link-configure/v1]
  steps:
  - id: endpoint
    kind: Allocation
    pluginRef: {name: host-cni-reset}
    selectors:
    - 'device.attributes["dra-multi-network.schseba.github.io"].pciAddress == "0000:29:00.0"'
    config: {mode: kernel, linkType: ethernet, rdmaAccess: false, driver: igb, resetMethod: flr, ifName: secondary0, mtu: 1500, up: true}
YAML
kubectl wait --for=condition=Ready networktopology/pf-reset --timeout=120s
class=$(kubectl get networktopology pf-reset -o jsonpath='{.status.rootClasses[0].className}')
revision=$(kubectl get networktopology pf-reset -o jsonpath='{.status.revisionRef.digest}')
kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/ROOT_CLASS/$class/" -e "s/CLAIM_TEMPLATE/pf-reset-${revision:0:16}/g" -e 's/POD_NAME/pf-reset-mvp/' -e "s/multi-network-example/$namespace/g" ../shared/workload.yaml | kubectl apply -f -
kubectl -n "$namespace" wait --for=condition=Ready pod/pf-reset-mvp --timeout=180s
kubectl -n "$namespace" exec pf-reset-mvp -c app -- ip -d link show secondary0
test "$(kcli ssh "$vm" "cat /sys/bus/pci/devices/$pci/driver_override" </dev/null)" = igb
echo 'PASS: zero-VF PF remains a PF; approved FLR, kernel binding and namespace handoff reached Ready'
claim=$(kubectl -n "$namespace" get pod pf-reset-mvp -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n "$namespace" delete pod pf-reset-mvp --wait=true
kubectl -n "$namespace" wait --for=delete "resourceclaim/$claim" --timeout=120s
for attribute in driver_override reset_method; do
  kcli ssh "$vm" "cat /sys/bus/pci/devices/$pci/$attribute" </dev/null > "$before/$attribute-after"
  cmp "$before/$attribute" "$before/$attribute-after"
done
kcli ssh "$vm" 'ip -j link show eth0' </dev/null > "$before/link-after.json"
python3 - "$before" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
original,restored=[json.loads((root/name).read_text())[0] for name in ("link.json","link-after.json")]
for field in ("ifname","address","mtu"):
 assert original[field]==restored[field],(field,original,restored)
assert ("UP" in original["flags"])==("UP" in restored["flags"])
assert not restored.get("ifalias"),restored
print("PASS: original PF reset methods, override, name, MAC, MTU and up state restored")
PY
test "$(kcli ssh "$vm" "cat /sys/bus/pci/devices/$pci/sriov_numvfs" </dev/null)" = 0
kubectl delete deviceexposurepolicy secondary-pf-reset --wait=true
kubectl delete networktopology pf-reset --wait=true
kubectl delete namespace "$namespace" --wait=true --timeout=120s

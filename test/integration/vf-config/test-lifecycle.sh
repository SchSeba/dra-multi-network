#!/usr/bin/env bash
# Manual acceptance on the development VM. Drain project claims before adding
# the new exposure policy: accounting changes require an allocation drain.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/vf-config"
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
  kubectl -n "$namespace" delete pod vf-config-mvp blocked-configured-vf --ignore-not-found --wait=true --timeout=120s
  rm -rf "$before"
}
trap cleanup EXIT
kcli ssh "$vm" 'ip -j -d link show eth0' </dev/null > "$before/pf.json"
kcli ssh "$vm" 'ip -j link show eth11' </dev/null > "$before/vf.json"
kubectl apply -f ../bond-vlan/policy.yaml
./apply.sh
kcli ssh "$vm" 'ip -j -d link show eth0' </dev/null | python3 -c '
import json,sys
vf=next(v for v in json.load(sys.stdin)[0]["vfinfo_list"] if v["vf"]==4)
assert vf["address"]=="02:00:00:00:00:14" and vf["trust"] and not vf["spoofchk"],vf
assert any(v.get("vlan")==37 and v.get("qos")==2 for v in vf["vlan_list"]),vf
print("PASS: allocated VF has the requested administrative MAC, trust, spoof check, VLAN and QoS")
'
kubectl apply -f - <<'YAML'
apiVersion: networking.dra-multi-network.schseba.github.io/v1alpha1
kind: NetworkTopology
metadata: {name: configured-vf-kernel-handoff}
spec:
  effectBudget: {allow: [linux/link-move/v1, linux/link-configure/v1]}
  steps:
    - id: endpoint
      kind: Allocation
      pluginRef: {name: sriov-cni}
      selectors: ['device.attributes["dra-multi-network.schseba.github.io"].pciAddress == "0000:29:11.0"']
      config: {mode: kernel, linkType: ethernet, rdmaAccess: false, ifName: secondary0, mtu: 1500, up: true}
YAML
generation=$(kubectl get networktopology configured-vf-kernel-handoff -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/configured-vf-kernel-handoff --timeout=120s
kubectl wait --for=condition=Ready networktopology/configured-vf-kernel-handoff --timeout=120s
class=$(kubectl get networktopology configured-vf-kernel-handoff -o jsonpath='{.status.rootClasses[0].className}')
revision=$(kubectl get networktopology configured-vf-kernel-handoff -o jsonpath='{.status.revisionRef.digest}')
kubectl get resourceslices -o json | python3 -c '
import json,sys
devices={d["name"]:d for s in json.load(sys.stdin)["items"] if s["spec"]["driver"]=="dra-multi-network.schseba.github.io" for d in s["spec"].get("devices",[])}
endpoints=[devices[n] for n in ("0000-29-11-0-kernel","0000-29-11-0-configured")]
assert all(d["attributes"]["dra-multi-network.schseba.github.io/allocationReady"]["bool"] for d in endpoints),endpoints
groups=[next(c for c in d["consumesCounters"] if c["counterSet"].startswith("i-")) for d in endpoints]
assert groups[0]["counterSet"]==groups[1]["counterSet"] and groups[0]["compatibilityGroups"]!=groups[1]["compatibilityGroups"],groups
'
sed -e "s/ROOT_CLASS/$class/" -e "s/CLAIM_TEMPLATE/configured-vf-handoff-${revision:0:16}/g" -e 's/POD_NAME/blocked-configured-vf/' ../shared/workload.yaml | kubectl apply -f -
kubectl -n "$namespace" wait --for='jsonpath={.status.conditions[?(@.type=="PodScheduled")].reason}=Unschedulable' pod/blocked-configured-vf --timeout=120s
claim=$(kubectl -n "$namespace" get pod blocked-configured-vf -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n "$namespace" get resourceclaim "$claim" -o json | python3 -c 'import json,sys; assert not json.load(sys.stdin).get("status",{}).get("allocation")'
echo 'PASS: configured and unconfigured personas for the same VF cannot be allocated together'
claim=$(kubectl -n "$namespace" get pod vf-config-mvp -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n "$namespace" delete pod vf-config-mvp --wait=true
kubectl -n "$namespace" wait --for=delete "resourceclaim/$claim" --timeout=120s
kubectl -n "$namespace" wait --for=condition=Ready pod/blocked-configured-vf --timeout=180s
claim=$(kubectl -n "$namespace" get pod blocked-configured-vf -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n "$namespace" delete pod blocked-configured-vf --wait=true
kubectl -n "$namespace" wait --for=delete "resourceclaim/$claim" --timeout=120s
kcli ssh "$vm" 'ip -j -d link show eth0' </dev/null > "$before/pf-after.json"
kcli ssh "$vm" 'ip -j link show eth11' </dev/null > "$before/vf-after.json"
python3 - "$before" <<'PY'
import json,sys,pathlib
root=pathlib.Path(sys.argv[1])
def load(name): return json.loads((root/name).read_text())[0]
assert load("pf.json")["vfinfo_list"]==load("pf-after.json")["vfinfo_list"], "VF settings were not restored"
original,restored=load("vf.json"),load("vf-after.json")
for field in ("ifname","address","mtu"):
 assert original[field]==restored[field],(field,original,restored)
assert ("UP" in original["flags"])==("UP" in restored["flags"])
assert not restored.get("ifalias"),restored
print("PASS: every VF's original PF settings and the configured VF's effective MAC/name/MTU/up state were restored")
PY

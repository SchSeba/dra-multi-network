#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
namespace=ipam-example

# The lab PF must be dedicated and earlier consumers fully drained.
./examples/ipam/apply.sh
kubectl -n "$namespace" exec ipam-demo-1 -- ip -o link show dev secondary0 | grep -q LOWER_UP
revision=$(kubectl get networktopology macvlan-ipam -o jsonpath='{.status.revisionRef.digest}')
endpoint=$(kubectl get networktopology macvlan-ipam -o jsonpath='{.status.rootClasses[?(@.rootStep=="endpoint")].className}')
address=$(kubectl get networktopology macvlan-ipam -o jsonpath='{.status.rootClasses[?(@.rootStep=="address")].className}')
for i in 2 3 4 5; do
  sed -e "s/ENDPOINT_CLASS/$endpoint/g" -e "s/IPAM_CLASS/$address/g" -e "s/CLAIM_TEMPLATE/macvlan-ipam-${revision:0:16}/g" -e "s/POD_NAME/ipam-demo-$i/g" examples/ipam/workload.yaml | kubectl apply -f -
done
for i in 1 2 3 4 5; do
  kubectl -n "$namespace" wait --for=condition=Ready "pod/ipam-demo-$i" --timeout=180s
done
sed -e "s/ENDPOINT_CLASS/$endpoint/g" -e "s/IPAM_CLASS/$address/g" -e "s/CLAIM_TEMPLATE/macvlan-ipam-${revision:0:16}/g" -e "s/POD_NAME/ipam-demo-6/g" examples/ipam/workload.yaml | kubectl apply -f -
addresses=()
for i in 1 2 3 4 5; do
  ip=$(kubectl -n "$namespace" exec "ipam-demo-$i" -- sh -ec "ip -o -4 addr show dev secondary0 scope global | awk '{print \$4}'")
  [[ "$ip" =~ ^192\.0\.2\.[2-6]/29$ ]]
  addresses+=("$ip")
done
[[ $(printf '%s\n' "${addresses[@]}" | sort -u | wc -l) -eq 5 ]]
peer=${addresses[1]%/*}
kubectl -n "$namespace" exec ipam-demo-1 -- ping -I secondary0 -c 3 -W 2 "$peer"
# Observe scheduler rejection, rather than treating a briefly Pending Pod as proof.
for attempt in $(seq 1 60); do
  events=$(kubectl -n "$namespace" get events --field-selector involvedObject.name=ipam-demo-6 -o json)
  if python3 -c 'import json,sys; events=json.load(sys.stdin)["items"]; sys.exit(not any(e.get("reason")=="FailedScheduling" and "claim" in e.get("message","").lower() for e in events))' <<<"$events"; then break; fi
  [[ "$attempt" -lt 60 ]]
  sleep 2
done
[[ $(kubectl -n "$namespace" get pod ipam-demo-6 -o jsonpath='{.status.phase}') == Pending ]]
claim=$(kubectl -n "$namespace" get pod ipam-demo-6 -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
[[ -z $(kubectl -n "$namespace" get resourceclaim "$claim" -o jsonpath='{.status.allocation}') ]]
# Verify both backend and scheduler capacity are returned after attachment cleanup.
kubectl -n "$namespace" delete pod ipam-demo-1 --wait=true --timeout=120s
kubectl -n "$namespace" wait --for=condition=Ready pod/ipam-demo-6 --timeout=180s
# Same-boot agent restart must preserve address identity and native verification.
before=$(kubectl -n "$namespace" exec ipam-demo-6 -- ip -o -4 addr show dev secondary0 scope global)
kubectl -n dra-multi-network rollout restart daemonset/dra-multi-network-kubeletplugin
kubectl -n dra-multi-network rollout status daemonset/dra-multi-network-kubeletplugin --timeout=180s
after=$(kubectl -n "$namespace" exec ipam-demo-6 -- ip -o -4 addr show dev secondary0 scope global)
[[ "$before" == "$after" ]]
kubectl delete namespace "$namespace" --wait=true --timeout=180s
printf 'PASS: five unique IPs, traffic, exhausted sixth claim, reuse and same-boot restart\n'

./examples/ipam/two-vf/apply.sh
kubectl -n "$namespace" get resourceclaims -o json | python3 -c '
import json,sys
claims=json.load(sys.stdin)["items"]
claim=next(c for c in claims if c.get("status",{}).get("allocation"))
results=claim["status"]["allocation"]["devices"]["results"]
nics=[r for r in results if r["driver"]=="dra-multi-network.schseba.github.io"]
ips=[r for r in results if r["driver"]=="ipam.schseba.github.io"]
assert len(nics)==len(ips)==2,results
assert len({r["device"] for r in nics})==2,nics
assert len({r["shareID"] for r in ips})==2,ips
assert all(r["consumedCapacity"]["ipam.schseba.github.io/addresses"]=="1" for r in ips),ips
'
for interface in secondary0 secondary1; do
  kubectl -n "$namespace" exec two-vf-ipam -- ip -d link show "$interface"
done
ips=$(kubectl -n "$namespace" exec two-vf-ipam -- sh -ec "ip -o -4 addr show dev secondary0 scope global; ip -o -4 addr show dev secondary1 scope global")
python3 -c 'import sys; lines=sys.stdin.read().splitlines(); assert len(lines)==2; ips=[line.split()[3] for line in lines]; assert len(set(ips))==2; assert all(ip.startswith("192.0.2.") and ip.endswith("/29") for ip in ips); print("Assigned:",*ips)' <<<"$ips"
kubectl delete namespace "$namespace" --wait=true --timeout=180s
# The ledger must retain tombstones while no address/holder remains live.
kubectl -n dra-multi-network get configmap -l ipam.schseba.github.io/ledger=true -o json | python3 -c '
import json,sys
for cm in json.load(sys.stdin)["items"]:
 for lease in json.loads(cm["data"]["leases"]).values():
  assert lease["state"]=="Released" and not lease.get("holders"),lease
'
printf 'PASS: two distinct VFs, two IPAM shares/IPs, and complete lease cleanup\n'

#!/usr/bin/env bash
# Manual test of an already deployed example; does not redeploy the driver.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/bond-vlan"
topology_name=${DRA_EXAMPLE_TOPOLOGY:-dual-vf-bond-vlan}
namespace=${DRA_EXAMPLE_NAMESPACE:-multi-network-example}
# shortcut: /24 lab addresses fit 122 node pairs, expand addressing before larger runs.
address_offset=${DRA_EXAMPLE_HOST_OFFSET:-0}
[[ $address_offset =~ ^[0-9]+$ && $address_offset -le 242 ]]
left_address=$((10 + address_offset))
right_address=$((11 + address_offset))
generation=$(kubectl get networktopology "$topology_name" -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" "networktopology/$topology_name" --timeout=120s
kubectl wait --for=condition=Ready "networktopology/$topology_name" --timeout=120s
revision=$(kubectl get networktopology "$topology_name" -o jsonpath='{.status.revisionRef.digest}')
left=$(kubectl get networktopology "$topology_name" -o jsonpath='{.status.rootClasses[?(@.rootStep=="left")].className}')
right=$(kubectl get networktopology "$topology_name" -o jsonpath='{.status.rootClasses[?(@.rootStep=="right")].className}')
kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
for pod in network-mvp network-mvp-peer; do
  sed -e "s/LEFT_CLASS/$left/" -e "s/RIGHT_CLASS/$right/" -e "s/CLAIM_TEMPLATE/dual-vf-network-${revision:0:16}/g" \
      -e "s/name: network-mvp$/name: $pod/" -e "s/multi-network-example/$namespace/g" workload.yaml | kubectl apply -f -
done
kubectl -n "$namespace" wait --for=condition=Ready pod/network-mvp pod/network-mvp-peer --timeout=180s
exec_app() { local pod=$1; shift; kubectl -n "$namespace" exec "$pod" -c app -- "$@"; }
traffic() {
  local tag subnet
  for tag in 100 200 300; do
    subnet=$((210 + tag / 100))
    exec_app network-mvp ping -I "tenant$tag" -c 3 -W 2 "192.168.$subnet.$right_address"
    exec_app network-mvp-peer ping -I "tenant$tag" -c 3 -W 2 "192.168.$subnet.$left_address"
  done
}
for tag in 100 200 300; do
  subnet=$((210 + tag / 100))
  exec_app network-mvp ip address replace "192.168.$subnet.$left_address/24" dev "tenant$tag"
  exec_app network-mvp-peer ip address replace "192.168.$subnet.$right_address/24" dev "tenant$tag"
done
traffic
trap 'exec_app network-mvp ip link set vfleft up' EXIT
exec_app network-mvp ip link set vfleft down
sleep 1
test "$(exec_app network-mvp cat /sys/class/net/bond0/bonding/active_slave)" = vfright
traffic
exec_app network-mvp ip link set vfleft up
trap - EXIT
echo 'PASS: both Pods exchange traffic on VLANs 100/200/300, including VF failover'

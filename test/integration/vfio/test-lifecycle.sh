#!/usr/bin/env bash
# Manual test of an existing vfio-mvp Pod; does not redeploy the driver.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/vfio"
kubectl -n multi-network-example wait --for=condition=Ready pod/vfio-mvp --timeout=120s
kubectl -n multi-network-example logs vfio-mvp -c verify-vfio | grep 'PASS: one allocated, viable VFIO group'
kubectl apply -f - <<'YAML'
apiVersion: networking.dra-multi-network.schseba.github.io/v1alpha1
kind: NetworkTopology
metadata: {name: vfio-kernel-handoff}
spec:
  effectBudget: {allow: [linux/link-move/v1, linux/link-configure/v1]}
  steps:
  - id: secondary
    kind: Allocation
    pluginRef: {name: sriov-cni}
    selectors: ['device.attributes["dra-multi-network.schseba.github.io"].pciAddress == "0000:29:11.0"']
    config: {mode: kernel, linkType: ethernet, rdmaAccess: false, ifName: secondary0, mtu: 1500, up: true}
YAML
kubectl wait --for=condition=Ready networktopology/vfio-kernel-handoff --timeout=120s
class=$(kubectl get networktopology vfio-kernel-handoff -o jsonpath='{.status.rootClasses[0].className}')
revision=$(kubectl get networktopology vfio-kernel-handoff -o jsonpath='{.status.revisionRef.digest}')
sed -e "s/ROOT_CLASS/$class/" -e "s/CLAIM_TEMPLATE/vfio-kernel-${revision:0:16}/g" -e 's/POD_NAME/blocked-vfio-kernel/' ../shared/workload.yaml | kubectl apply -f -
kubectl -n multi-network-example wait --for='jsonpath={.status.conditions[?(@.type=="PodScheduled")].reason}=Unschedulable' pod/blocked-vfio-kernel --timeout=120s
claim=$(kubectl -n multi-network-example get pod blocked-vfio-kernel -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n multi-network-example get resourceclaim "$claim" -o json | python3 -c 'import json,sys;c=json.load(sys.stdin);assert not c.get("status",{}).get("allocation"),c'
echo 'PASS: VFIO blocks kernel handoff of the same VF'
vfio_claim=$(kubectl -n multi-network-example get pod vfio-mvp -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n multi-network-example delete pod vfio-mvp --wait=true
kubectl -n multi-network-example wait --for=delete "resourceclaim/$vfio_claim" --timeout=120s
kubectl -n multi-network-example wait --for=condition=Ready pod/blocked-vfio-kernel --timeout=180s
driver=$(kubectl -n multi-network-example exec blocked-vfio-kernel -c app -- readlink /sys/class/net/secondary0/device/driver)
test "${driver##*/}" = igbvf
echo 'PASS: releasing VFIO restored igbvf and admitted kernel handoff of the same VF'
kubectl -n multi-network-example delete pod blocked-vfio-kernel --wait=true
kubectl -n multi-network-example wait --for=delete "resourceclaim/$claim" --timeout=120s

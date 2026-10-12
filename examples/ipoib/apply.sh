#!/usr/bin/env bash
# Manual hardware example; separate from deployment and feature-gate checks.
set -euo pipefail
cd "$(dirname "$0")"
: "${DRA_IB_PCI:?set the drained dedicated PCI function}"
: "${DRA_IB_DRIVER:?set its current kernel driver}"
kind=${DRA_IB_KIND:-PCI}
[[ $DRA_IB_PCI =~ ^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$ ]]
[[ $DRA_IB_DRIVER =~ ^[A-Za-z0-9_-]{1,64}$ ]]
[[ $kind == PCI || $kind == PF || $kind == VF ]]
plugin=host-cni-ipoib
if [[ $kind == VF ]]; then plugin=sriov-cni-ipoib; fi
sed -e "s/IB_PCI/$DRA_IB_PCI/g" -e "s/IB_DRIVER/$DRA_IB_DRIVER/g" \
    -e "s/kind: PCI/kind: $kind/" -e "s/host-cni-ipoib/$plugin/g" policy.yaml | kubectl apply -f -
sed "s/host-cni-ipoib/$plugin/g" topology.yaml | kubectl apply -f -
generation=$(kubectl get networktopology single-port-ipoib -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/single-port-ipoib --timeout=120s
kubectl wait --for=condition=Ready networktopology/single-port-ipoib --timeout=120s
revision=$(kubectl get networktopology single-port-ipoib -o jsonpath='{.status.revisionRef.digest}')
class=$(kubectl get networktopology single-port-ipoib -o jsonpath='{.status.rootClasses[0].className}')
kubectl create namespace multi-network-ipoib-example --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/ROOT_CLASS/$class/g" -e "s/CLAIM_TEMPLATE/ipoib-${revision:0:16}/g" workload.yaml | kubectl apply -f -
kubectl -n multi-network-ipoib-example wait --for=condition=Ready pod/ipoib-mvp --timeout=180s

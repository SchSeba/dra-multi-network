#!/usr/bin/env bash
# Manual hardware example; separate from deployment and feature-gate checks.
set -euo pipefail
cd "$(dirname "$0")"
: "${DRA_RDMA_PCI:?set the drained dedicated PCI function}"
: "${DRA_RDMA_DRIVER:?set its current kernel driver}"
kind=${DRA_RDMA_KIND:-PCI}
[[ $DRA_RDMA_PCI =~ ^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$ ]]
[[ $DRA_RDMA_DRIVER =~ ^[A-Za-z0-9_-]{1,64}$ ]]
[[ $kind == PCI || $kind == PF || $kind == VF ]]
plugin=host-cni-roce
if [[ $kind == VF ]]; then plugin=sriov-cni-roce; fi
sed -e "s/RDMA_PCI/$DRA_RDMA_PCI/g" -e "s/RDMA_DRIVER/$DRA_RDMA_DRIVER/g" \
    -e "s/kind: PCI/kind: $kind/" -e "s/host-cni-roce/$plugin/g" policy.yaml | kubectl apply -f -
sed "s/host-cni-roce/$plugin/g" topology.yaml | kubectl apply -f -
generation=$(kubectl get networktopology single-port-roce -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/single-port-roce --timeout=120s
kubectl wait --for=condition=Ready networktopology/single-port-roce --timeout=120s
revision=$(kubectl get networktopology single-port-roce -o jsonpath='{.status.revisionRef.digest}')
class=$(kubectl get networktopology single-port-roce -o jsonpath='{.status.rootClasses[0].className}')
kubectl create namespace multi-network-roce-example --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/ROOT_CLASS/$class/g" -e "s/CLAIM_TEMPLATE/roce-${revision:0:16}/g" workload.yaml | kubectl apply -f -
kubectl -n multi-network-roce-example wait --for=condition=Ready pod/roce-mvp --timeout=180s

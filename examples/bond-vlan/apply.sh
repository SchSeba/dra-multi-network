#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
kubectl apply -f policy.yaml -f topology.yaml
generation=$(kubectl get networktopology dual-vf-bond-vlan -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/dual-vf-bond-vlan --timeout=120s
kubectl wait --for=condition=Ready networktopology/dual-vf-bond-vlan --timeout=120s
revision=$(kubectl get networktopology dual-vf-bond-vlan -o jsonpath='{.status.revisionRef.digest}')
template="dual-vf-network-${revision:0:16}"
left=$(kubectl get networktopology dual-vf-bond-vlan -o jsonpath='{.status.rootClasses[?(@.rootStep=="left")].className}')
right=$(kubectl get networktopology dual-vf-bond-vlan -o jsonpath='{.status.rootClasses[?(@.rootStep=="right")].className}')
kubectl create namespace multi-network-example --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/LEFT_CLASS/$left/" -e "s/RIGHT_CLASS/$right/" -e "s/CLAIM_TEMPLATE/$template/g" workload.yaml | kubectl apply -f -
kubectl -n multi-network-example wait --for=condition=Ready pod/network-mvp --timeout=180s

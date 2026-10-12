#!/usr/bin/env bash
# Drain existing project claims before introducing this exposure contract.
set -euo pipefail
cd "$(dirname "$0")"
kubectl apply -f plugin.yaml
kubectl apply -f policy.yaml -f topology.yaml
name=programmable-vf-bond-vlan
generation=$(kubectl get networktopology "$name" -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" "networktopology/$name" --timeout=120s
kubectl wait --for=condition=Ready "networktopology/$name" --timeout=120s
revision=$(kubectl get networktopology "$name" -o jsonpath='{.status.revisionRef.digest}')
left=$(kubectl get networktopology "$name" -o jsonpath='{.status.rootClasses[?(@.rootStep=="left")].className}')
right=$(kubectl get networktopology "$name" -o jsonpath='{.status.rootClasses[?(@.rootStep=="right")].className}')
kubectl create namespace multi-network-example --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/LEFT_CLASS/$left/" -e "s/RIGHT_CLASS/$right/" -e "s/CLAIM_TEMPLATE/dual-vf-network-${revision:0:16}/g" ../bond-vlan/workload.yaml | kubectl apply -f -
kubectl -n multi-network-example wait --for=condition=Ready pod/network-mvp --timeout=180s

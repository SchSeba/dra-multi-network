#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
kubectl create namespace multi-network-example --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f policy.yaml -f topology.yaml
generation=$(kubectl get networktopology configured-vf -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/configured-vf --timeout=120s
kubectl wait --for=condition=Ready networktopology/configured-vf --timeout=120s
class=$(kubectl get networktopology configured-vf -o jsonpath='{.status.rootClasses[0].className}')
revision=$(kubectl get networktopology configured-vf -o jsonpath='{.status.revisionRef.digest}')
sed -e "s/ROOT_CLASS/$class/" -e "s/CLAIM_TEMPLATE/configured-vf-${revision:0:16}/g" workload.yaml | kubectl apply -f -
kubectl -n multi-network-example wait --for=condition=Ready pod/vf-config-mvp --timeout=180s

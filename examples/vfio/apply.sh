#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
kubectl apply -f policy.yaml -f topology.yaml
generation=$(kubectl get networktopology vfio-endpoint -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/vfio-endpoint --timeout=120s
kubectl wait --for=condition=Ready networktopology/vfio-endpoint --timeout=120s
revision=$(kubectl get networktopology vfio-endpoint -o jsonpath='{.status.revisionRef.digest}')
class=$(kubectl get networktopology vfio-endpoint -o jsonpath='{.status.rootClasses[0].className}')
kubectl create namespace multi-network-example --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/ROOT_CLASS/$class/" -e "s/CLAIM_TEMPLATE/vfio-${revision:0:16}/g" workload.yaml | kubectl apply -f -
kubectl -n multi-network-example wait --for=condition=Ready pod/vfio-mvp --timeout=180s

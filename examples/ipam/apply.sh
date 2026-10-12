#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
kubectl apply -f pool.yaml -f policy.yaml -f topology.yaml
generation=$(kubectl get networktopology macvlan-ipam -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/macvlan-ipam --timeout=120s
kubectl wait --for=condition=Ready networktopology/macvlan-ipam --timeout=120s
revision=$(kubectl get networktopology macvlan-ipam -o jsonpath='{.status.revisionRef.digest}')
endpoint=$(kubectl get networktopology macvlan-ipam -o jsonpath='{.status.rootClasses[?(@.rootStep=="endpoint")].className}')
address=$(kubectl get networktopology macvlan-ipam -o jsonpath='{.status.rootClasses[?(@.rootStep=="address")].className}')
kubectl create namespace ipam-example --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/ENDPOINT_CLASS/$endpoint/g" -e "s/IPAM_CLASS/$address/g" -e "s/CLAIM_TEMPLATE/macvlan-ipam-${revision:0:16}/g" -e 's/POD_NAME/ipam-demo-1/g' workload.yaml | kubectl apply -f -
kubectl -n ipam-example wait --for=condition=Ready pod/ipam-demo-1 --timeout=180s

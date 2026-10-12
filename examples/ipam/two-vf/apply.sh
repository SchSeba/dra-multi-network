#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
kubectl apply -f ../pool.yaml -f policy.yaml -f topology.yaml
generation=$(kubectl get networktopology two-vf-ipam -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/two-vf-ipam --timeout=120s
kubectl wait --for=condition=Ready networktopology/two-vf-ipam --timeout=120s
revision=$(kubectl get networktopology two-vf-ipam -o jsonpath='{.status.revisionRef.digest}')
rendered=$(cat workload.yaml)
for root in vf0 vf1 address0 address1; do
  class=$(kubectl get networktopology two-vf-ipam -o "jsonpath={.status.rootClasses[?(@.rootStep==\"$root\")].className}")
  marker="${root^^}_CLASS"
  rendered=${rendered//"$marker"/"$class"}
done
kubectl create namespace ipam-example --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/CLAIM_TEMPLATE/two-vf-ipam-${revision:0:16}/g" -e 's/POD_NAME/two-vf-ipam/g' <<<"$rendered" | kubectl apply -f -
kubectl -n ipam-example wait --for=condition=Ready pod/two-vf-ipam --timeout=180s

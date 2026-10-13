#!/usr/bin/env bash
# Connect a single-root topology that an administrator has already configured.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/.." && pwd)
if [[ $# != 2 ]]; then
  echo "usage: $0 TOPOLOGY POD_NAME" >&2
  exit 1
fi
topology=$1
pod=$2
namespace=${DRA_EXAMPLE_NAMESPACE:-multi-network-example}
generation=$(kubectl get networktopology "$topology" -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" "networktopology/$topology" --timeout=120s
kubectl wait --for=condition=Ready "networktopology/$topology" --timeout=120s
roots=$(kubectl get networktopology "$topology" -o jsonpath='{range .status.rootClasses[*]}{.rootStep}{"\n"}{end}')
if [[ $(wc -l <<< "$roots") != 1 ]]; then
  echo "use the topology's multi-root workload; connect.sh supports one root" >&2
  exit 1
fi
class=$(kubectl get networktopology "$topology" -o jsonpath='{.status.rootClasses[0].className}')
revision=$(kubectl get networktopology "$topology" -o jsonpath='{.status.revisionRef.digest}')
kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/ROOT_CLASS/$class/" -e "s/CLAIM_TEMPLATE/${topology}-${revision:0:16}/g" \
    -e "s/POD_NAME/$pod/" -e "s/multi-network-example/$namespace/g" "$repo_dir/examples/single-vf/workload.yaml" | kubectl apply -f -
kubectl -n "$namespace" wait --for=condition=Ready "pod/$pod" --timeout=180s

#!/usr/bin/env bash
set -euo pipefail
source hack/common.sh

controller_ip=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
DRIVER_IMAGE="$controller_ip:5000/dra-multi-network"
CONTAINER_TOOL=podman IMAGE_NAME="$DRIVER_IMAGE" make -C deployments/container/
podman push --tls-verify=false "$DRIVER_IMAGE"
export PATH="${root}/bin:$PATH"
make helm

helm upgrade -i dra-multi-network deployments/helm/dra-multi-network/ \
    --namespace "$NAMESPACE" --create-namespace --reuse-values \
    --set image.repository="$DRIVER_IMAGE" --wait --timeout 5m
kubectl -n "$NAMESPACE" rollout restart daemonset/dra-multi-network-kubeletplugin deployment/dra-multi-network-controller
kubectl -n "$NAMESPACE" rollout status daemonset/dra-multi-network-kubeletplugin --timeout=180s
kubectl -n "$NAMESPACE" rollout status deployment/dra-multi-network-controller --timeout=180s

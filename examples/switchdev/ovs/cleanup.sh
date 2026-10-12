#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
namespace=multi-network-example
# Save generated identities before removing the consuming Pods.
for suffix in a b; do
  pod=switchdev-ovs-$suffix
  claim=$(kubectl -n "$namespace" get pod "$pod" -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}' --ignore-not-found)
  template=$(kubectl -n "$namespace" get pod "$pod" -o jsonpath='{.spec.resourceClaims[0].resourceClaimTemplateName}' --ignore-not-found)
  kubectl -n "$namespace" delete pod "$pod" --ignore-not-found --wait=true --timeout=120s
  if [[ -n $claim ]]; then kubectl -n "$namespace" wait --for=delete "resourceclaim/$claim" --timeout=120s; fi
  if [[ -n $template ]]; then kubectl -n "$namespace" delete resourceclaimtemplate "$template" --ignore-not-found; fi
done
kubectl delete networktopology switchdev-ovs --ignore-not-found
kubectl delete deviceexposurepolicy switchdev-ovs-vfs --ignore-not-found

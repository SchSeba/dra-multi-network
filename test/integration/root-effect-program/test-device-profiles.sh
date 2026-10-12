#!/usr/bin/env bash
# Manual lab acceptance; uses the existing scoped-access and sharing tests.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/root-effect-program"
namespace=multi-network-example
kubectl -n "$namespace" delete pods network-mvp network-mvp-peer --ignore-not-found --wait=true --timeout=120s
kubectl -n "$namespace" wait --for=delete resourceclaims --all --timeout=180s
kubectl get resourceclaims -A -o json | python3 -c '
import json,sys
for claim in json.load(sys.stdin)["items"]:
 for result in claim.get("status",{}).get("allocation",{}).get("devices",{}).get("results",[]):
  assert result["driver"] != "dra-multi-network.schseba.github.io", "Drain other project claims before this test"
'
restore() {
  kubectl -n "$namespace" delete pod vfio-container-scope --ignore-not-found --wait=true --timeout=120s
  kubectl -n "$namespace" wait --for=delete resourceclaims --all --timeout=180s
  kubectl delete namespace multi-network-compatibility-test --ignore-not-found --wait=true --timeout=180s
  kubectl apply -f ../vfio/container-scope.yaml -f ../shared/policies.yaml -f ../shared/vf-policies.yaml -f ../shared/topologies.yaml -f ../shared/vf-topologies.yaml
  "$repo_dir/examples/bond-vlan/apply.sh"
}
trap restore EXIT
kubectl apply -f device-plugins.yaml
DRA_VFIO_SCOPE_MANIFEST="$PWD/device-vfio.yaml" DRA_TEST_RESTART_AGENT=1 "$repo_dir/test/integration/vfio/test-container-scope.sh"
echo 'PASS: programmable VFIO preparation, per-container grants, agent recovery and recorded cleanup'
kubectl apply -f ../vfio/container-scope.yaml
DRA_SHARED_PROFILE_MANIFEST="$PWD/device-shared.yaml" "$repo_dir/test/integration/shared/test-vf-compatibility.sh"
echo 'PASS: programmable macvlan/ipvlan traffic and shared/exclusive compatibility with recorded cleanup'
restore
trap - EXIT
"$repo_dir/test/integration/bond-vlan/test-traffic.sh"

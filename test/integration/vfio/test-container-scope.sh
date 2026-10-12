#!/usr/bin/env bash
# Manual two-function acceptance on the development VM. Drain project claims
# before adding its exposure policy; no deployment-time verification hook.
set -euo pipefail
repo_dir=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo_dir/examples/vfio"
namespace=multi-network-example
kubectl get resourceclaims -A -o json | python3 -c '
import json,sys
for claim in json.load(sys.stdin)["items"]:
 for result in claim.get("status",{}).get("allocation",{}).get("devices",{}).get("results",[]):
  assert result["driver"] != "dra-multi-network.schseba.github.io", "Drain project claims before this test"
'
cleanup() { kubectl -n "$namespace" delete pod vfio-container-scope --ignore-not-found --wait=true --timeout=120s; }
trap cleanup EXIT
kubectl apply -f "${DRA_VFIO_SCOPE_MANIFEST:-container-scope.yaml}"
generation=$(kubectl get networktopology vfio-container-scope -o jsonpath='{.metadata.generation}')
kubectl wait --for="jsonpath={.status.observedGeneration}=$generation" networktopology/vfio-container-scope --timeout=120s
kubectl wait --for=condition=Ready networktopology/vfio-container-scope --timeout=120s
revision=$(kubectl get networktopology vfio-container-scope -o jsonpath='{.status.revisionRef.digest}')
left=$(kubectl get networktopology vfio-container-scope -o jsonpath='{.status.rootClasses[?(@.rootStep=="left")].className}')
right=$(kubectl get networktopology vfio-container-scope -o jsonpath='{.status.rootClasses[?(@.rootStep=="right")].className}')
kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
sed -e "s/LEFT_CLASS/$left/" -e "s/RIGHT_CLASS/$right/" -e "s/CLAIM_TEMPLATE/vfio-scope-${revision:0:16}/g" container-scope-workload.yaml | kubectl apply -f -
kubectl -n "$namespace" wait --for=condition=Ready pod/vfio-container-scope --timeout=180s
if [[ ${DRA_TEST_RESTART_AGENT:-0} == 1 ]]; then
  before=$(kubectl -n "$namespace" get pod vfio-container-scope -o json | python3 -c 'import json,sys;p=json.load(sys.stdin);print(json.dumps([p["metadata"]["uid"],[(s["name"],s["containerID"],s["restartCount"]) for s in p["status"]["containerStatuses"]]]))')
  kubectl -n dra-multi-network delete pod -l app.kubernetes.io/component=kubeletplugin --wait=true
  kubectl -n dra-multi-network rollout status daemonset/dra-multi-network-kubeletplugin --timeout=180s
  after=$(kubectl -n "$namespace" get pod vfio-container-scope -o json | python3 -c 'import json,sys;p=json.load(sys.stdin);assert all(s["ready"] for s in p["status"]["containerStatuses"]),p;print(json.dumps([p["metadata"]["uid"],[(s["name"],s["containerID"],s["restartCount"]) for s in p["status"]["containerStatuses"]]]))')
  test "$before" = "$after"
  echo 'PASS: agent recovered the running two-request VFIO workload without changing its Pod or application containers'
fi
network_namespace=''
for container in verify-both left right observer; do
  logs=$(kubectl -n "$namespace" logs vfio-container-scope -c "$container")
  grep 'PASS:' <<< "$logs"
  identity=$(sed -n 's/^NETNS: //p' <<< "$logs")
  test -n "$identity"
  if test -z "$network_namespace"; then network_namespace=$identity; fi
  test "$identity" = "$network_namespace"
done
kubectl -n "$namespace" debug pod/vfio-container-scope --container=scope-debug --image=quay.io/schseba/toolbox:latest --profile=general -- python3 -c 'import glob,os; assert not glob.glob("/dev/vfio/*"); print("NETNS:",os.readlink("/proc/self/ns/net")); print("PASS: ephemeral container has no unrequested VFIO devices")'
kubectl -n "$namespace" wait --for='jsonpath={.status.ephemeralContainerStatuses[?(@.name=="scope-debug")].state.terminated.exitCode}=0' pod/vfio-container-scope --timeout=120s
logs=$(kubectl -n "$namespace" logs vfio-container-scope -c scope-debug)
grep 'PASS:' <<< "$logs"
test "$(sed -n 's/^NETNS: //p' <<< "$logs")" = "$network_namespace"
echo 'PASS: init, application, sidecar and ephemeral containers all joined the same verified sandbox network namespace'
kubectl -n "$namespace" debug pod/vfio-container-scope --container=scope-denied --image=quay.io/schseba/toolbox:latest --profile=sysadmin -- sleep infinity
kubectl -n "$namespace" wait --for='jsonpath={.status.ephemeralContainerStatuses[?(@.name=="scope-denied")].state.waiting.reason}=CreateContainerError' pod/vfio-container-scope --timeout=120s
kubectl -n "$namespace" get pod vfio-container-scope -o json | python3 -c '
import json,sys
status=next(s for s in json.load(sys.stdin)["status"]["ephemeralContainerStatuses"] if s["name"]=="scope-denied")
assert "E_DEVICE_ACCESS_SCOPE" in status["state"]["waiting"]["message"],status
assert "running" not in status["state"],status
print("PASS: NRI rejected a privileged ephemeral container that would bypass the per-container device grant")
'
claim=$(kubectl -n "$namespace" get pod vfio-container-scope -o jsonpath='{.status.resourceClaimStatuses[0].resourceClaimName}')
kubectl -n "$namespace" delete pod vfio-container-scope --wait=true
kubectl -n "$namespace" wait --for=delete "resourceclaim/$claim" --timeout=120s
for pci in 0000:29:10.0 0000:29:11.0; do
  driver=$(kcli ssh multi-network-ctlplane-0 "readlink /sys/bus/pci/devices/$pci/driver" </dev/null)
  test "${driver##*/}" = igbvf
  override=$(kcli ssh multi-network-ctlplane-0 "cat /sys/bus/pci/devices/$pci/driver_override" </dev/null)
  test "$override" = '(null)'
done
echo 'PASS: both original igbvf bindings and driver overrides restored after scoped access cleanup'

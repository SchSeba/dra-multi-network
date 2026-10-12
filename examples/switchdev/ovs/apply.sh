#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
mode=${1:---all}
case "$mode" in
  --all|--configure-only|--connect-only) ;;
  *) echo "usage: $0 [--configure-only|--connect-only]" >&2; exit 1 ;;
esac
if [[ $mode != --connect-only ]]; then
  # Obtain this UUID on the selected node: ovs-vsctl get Bridge ovsbr0 _uuid
  : "${OVS_BRIDGE_UUID:?set the existing approved ovsbr0 UUID}"
  [[ $OVS_BRIDGE_UUID =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]
  sed "s/OVS_BRIDGE_UUID/$OVS_BRIDGE_UUID/" policy.yaml | kubectl apply -f -
  kubectl apply -f topology.yaml
fi
if [[ $mode != --configure-only ]]; then
  for suffix in a b; do
    ../../connect.sh switchdev-ovs switchdev-ovs-$suffix
  done
fi

#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
mode=${1:---all}
case "$mode" in
  --all|--configure-only|--connect-only) ;;
  *) echo "usage: $0 [--configure-only|--connect-only]" >&2; exit 1 ;;
esac
if [[ $mode != --connect-only ]]; then
  kubectl apply -f policy.yaml -f topology.yaml
fi
if [[ $mode != --configure-only ]]; then
  for suffix in a b; do
    ../../connect.sh switchdev-bridge switchdev-bridge-$suffix
  done
fi

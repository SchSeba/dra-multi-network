#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
kubectl apply -f policy.yaml -f topology.yaml
../connect.sh single-vf single-vf

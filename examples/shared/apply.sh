#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
kubectl apply -f policies.yaml -f vf-policies.yaml -f topologies.yaml -f vf-topologies.yaml
../connect.sh shared-macvlan macvlan
../connect.sh vf-ipvlan ipvlan

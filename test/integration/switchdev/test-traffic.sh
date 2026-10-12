#!/usr/bin/env bash
set -euo pipefail
case ${1:-} in
  bridge) name=switchdev-bridge; subnet=215 ;;
  ovs) name=switchdev-ovs; subnet=216 ;;
  *) echo "usage: $0 bridge|ovs" >&2; exit 1 ;;
esac
namespace=multi-network-example
for suffix in a b; do
  pod=$name-$suffix
  kubectl -n "$namespace" wait --for=condition=Ready "pod/$pod" --timeout=180s
  last=10; if [[ $suffix == b ]]; then last=11; fi
  for pair in "vf0:$subnet" "tenant200:$((subnet+2))" "tenant300:$((subnet+4))"; do
    link=${pair%%:*}; network=${pair##*:}
    kubectl -n "$namespace" exec "$pod" -- ip address replace "192.168.$network.$last/24" dev "$link"
  done
done
for pair in "vf0:$subnet" "tenant200:$((subnet+2))" "tenant300:$((subnet+4))"; do
  link=${pair%%:*}; network=${pair##*:}
  kubectl -n "$namespace" exec "$name-a" -- ping -I "$link" -c 3 -W 2 "192.168.$network.11"
  kubectl -n "$namespace" exec "$name-b" -- ping -I "$link" -c 3 -W 2 "192.168.$network.10"
done
echo "PASS: $name bidirectional traffic on native VLAN 100 and tagged VLANs 200/300"

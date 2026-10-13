#!/usr/bin/env bash
# Run on the virtualization host after deploying either cluster layout.
set -euo pipefail
cd "$(dirname "$0")/../.."
source hack/common.sh
command -v kcli >/dev/null
python3 -c 'import yaml'
# New publication policies must be configured before any driver claim is active.
claims=$(kubectl get resourceclaims -A -o json)
python3 -c 'import json,sys; claims=json.load(sys.stdin)["items"]; assert not any("dra-multi-network.schseba.github.io" in json.dumps(c.get("status",{}).get("allocation",{})) for c in claims), "Drain existing driver claims before configuring the combined example"' <<< "$claims"
mapfile -t nodes < <(kubectl get nodes -l feature.node.kubernetes.io/network-sriov.capable=true -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
((${#nodes[@]} > 0))
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
# Configure every node before creating Pods: policy changes fence publication.
for index in "${!nodes[@]}"; do
  node=${nodes[$index]}
  vm=${node%.$domain_name}
  kubectl cordon "$node"
  kcli ssh "$vm" 'sudo bash -s' <<'NODE' > "$dir/hardware.json"
set -euo pipefail
ovs-vsctl --may-exist add-br ovsbr0
ovs-vsctl set Bridge ovsbr0 external_ids:dra-multi-network.schseba.github.io/bridge-id=site/ovsbr0 other_config:hwaddr=02:42:00:00:00:20
ovs-vsctl set Interface ovsbr0 mtu_request=1500
ip link set ovsbr0 mtu 1500 up
python3 - <<'PY'
import json
from pathlib import Path
import subprocess
native = Path('/sys/class/net/sriovtest0/device').resolve()
mock = Path('/sys/bus/pci/devices') / Path('/sys/module/mock_smartnic/parameters/target_pf').read_text().strip()
assert native != mock
assert (native/'driver').resolve().name == 'igb'
assert (mock/'driver').resolve().name == 'mock_smartnic_pf'
assert (native/'sriov_numvfs').read_text().strip() == '4'
assert (mock/'sriov_numvfs').read_text().strip() == '4'
name = next((mock/'net').iterdir()).name
subprocess.run(['ip','link','set',name,'down'],check=True)
print(json.dumps({'native':native.name,'mock':mock.name,'vfs':[(mock/f'virtfn{i}').resolve().name for i in [2,3]],'bridgeUUID':subprocess.check_output(['ovs-vsctl','get','Bridge','ovsbr0','_uuid'],text=True).strip()}))
PY
NODE
  python3 - "$node" "$index" "$dir/hardware.json" <<'PY'
import json,sys
path=sys.argv[3]
obj=json.load(open(path)); obj.update(node=sys.argv[1],suffix=sys.argv[2])
json.dump(obj,open(path,'w'))
PY
  python3 examples/bond-ovs/render.py < "$dir/hardware.json" > "$dir/node-$index.yaml"
  kubectl apply -f "$dir/node-$index.yaml"
done
for index in "${!nodes[@]}"; do
  node=${nodes[$index]}
  generation=$(kubectl get "deviceexposurepolicy/eswitch-$index" -o jsonpath='{.metadata.generation}')
  kubectl wait --for="jsonpath={.status.eswitchNodes[0].observedGeneration}=$generation" "deviceexposurepolicy/eswitch-$index" --timeout=180s
  kubectl wait --for='jsonpath={.status.eswitchNodes[0].phase}=Ready' "deviceexposurepolicy/eswitch-$index" --timeout=180s
  kubectl wait --for='jsonpath={.status.eswitchNodes[0].mode}=switchdev' "deviceexposurepolicy/eswitch-$index" --timeout=180s
  kubectl uncordon "$node"
done
for index in "${!nodes[@]}"; do
  export DRA_EXAMPLE_NAMESPACE="bond-ovs-$index"
  export DRA_EXAMPLE_TOPOLOGY="bond-vlan-$index"
  export DRA_EXAMPLE_HOST_OFFSET=$((index * 2))
  ./examples/connect.sh "switchdev-ovs-$index" switchdev-ovs-a
  ./examples/connect.sh "switchdev-ovs-$index" switchdev-ovs-b
  ./test/integration/bond-vlan/test-traffic.sh
  ./test/integration/switchdev/test-traffic.sh ovs
  for pod in network-mvp network-mvp-peer switchdev-ovs-a switchdev-ovs-b; do
    actual=$(kubectl -n "$DRA_EXAMPLE_NAMESPACE" get pod "$pod" -o jsonpath='{.spec.nodeName}')
    [[ $actual == "${nodes[$index]}" ]]
  done
  echo "PASS: native VF bond/VLAN and OVS switchdev run together on ${nodes[$index]}"
done
# Native VFs share the lab fabric; verify traffic between workers as well.
if ((${#nodes[@]} > 1)); then
  for tag in 100 200 300; do
    subnet=$((210 + tag / 100))
    kubectl -n bond-ovs-0 exec network-mvp -c app -- ping -I "tenant$tag" -c 3 -W 2 "192.168.$subnet.12"
    kubectl -n bond-ovs-1 exec network-mvp -c app -- ping -I "tenant$tag" -c 3 -W 2 "192.168.$subnet.10"
  done
  echo 'PASS: native bond/VLAN traffic also crosses between the first two workers'
fi

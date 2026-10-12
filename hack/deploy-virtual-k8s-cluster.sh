#!/usr/bin/env bash
set -xeo pipefail

source hack/common.sh

NUM_OF_WORKERS=${NUM_OF_WORKERS:-2}
export OPERATOR_EXEC=kubectl
# Optional mock switchdev PF; only the dedicated secondary NIC is bound.
MOCK_SMARTNIC_REPO=${MOCK_SMARTNIC_REPO:-}
MOCK_SMARTNIC_REF=${MOCK_SMARTNIC_REF:-724d8984696f9124f777776e45d2108cec60c214}
MOCK_SMARTNIC_KERNEL=${MOCK_SMARTNIC_KERNEL:-5.14.0-427.el9.x86_64}
declare -A MOCK_SMARTNIC_BOOTS=()
export CNI_PLUGINS_URL="https://github.com/containernetworking/plugins/releases/download/v1.9.1/cni-plugins-linux-amd64-v1.9.1.tgz"

cleanup_only=false
render_only=false
for arg in "$@"; do
  case "$arg" in
    --cleanup)
      cleanup_only=true
      ;;
    --single-node)
      NUM_OF_WORKERS=0
      ;;
    --render)
      render_only=true
      ;;
    -h|--help)
      cat <<EOF
Usage: $0 [--cleanup] [--single-node] [--render]

Deploy a kcli-based virtual Kubernetes cluster with SR-IOV VFs and the DRA driver.

  (default)      Tear down any existing cluster, then deploy a new multi-node cluster
                 (1 control plane + NUM_OF_WORKERS workers, default 2).
  --single-node  Deploy a single-node cluster (control plane only, acts as worker).
  --cleanup      Tear down the cluster and exit.
  --render       Write and print the cluster plan without deploying or deleting resources.

Environment:
  NUM_OF_WORKERS     Worker count (default: 2; use 0 or --single-node for single-node).
  CLUSTER_NAME       Cluster name (default: multi-network).
  CLUSTER_VERSION    Kubernetes version (default: 1.37.0).
  NETWORK_NAME      Management network (default: multi-net; at most 15 characters).
  SRIOV_NETWORK_NAME Secondary network (default: multi-sriov; at most 15 characters).
  NETWORK_CIDR      Management subnet (default: 192.168.130.0/24).
  SRIOV_NETWORK_CIDR Secondary subnet (default: 192.168.210.0/24).
  POD_CIDR          Pod subnet (default: 10.246.0.0/16).
  SERVICE_CIDR      Service subnet (default: 10.112.0.0/16).
  API_IP            API address (default: 192.168.130.250).
  VIRTUAL_ROUTER_ID  Router ID (default: 210).
  MOCK_SMARTNIC_REPO Optional HTTPS mock repo; enable with
                    https://github.com/SchSeba/switchdev-mock-driver.git.
  MOCK_SMARTNIC_REF  Full commit SHA (default: 724d8984696f9124f777776e45d2108cec60c214).
  MOCK_SMARTNIC_KERNEL Running guest kernel pin (default: 5.14.0-427.el9.x86_64).

Mock mode installs OVS/module on SR-IOV hosts and creates five VFs only on the
XML-attested secondary PF. It leaves legacy mode for DRA eswitch policies,
enables the DRA OVS socket mount, and does not deploy the SR-IOV operator.
Workers must already boot the selected kernel; this script does not install it.
EOF
      exit 0
      ;;
    *)
      echo "Unknown argument: $arg" >&2
      echo "Usage: $0 [--cleanup] [--single-node] [--render]" >&2
      exit 1
      ;;
  esac
done

if [[ -n $MOCK_SMARTNIC_REPO ]]; then
  [[ $MOCK_SMARTNIC_REPO =~ ^https://[a-zA-Z0-9.-]+(:[0-9]+)?/[a-zA-Z0-9._/-]+$ && $MOCK_SMARTNIC_REF =~ ^[0-9a-f]{40}$ && $MOCK_SMARTNIC_KERNEL =~ ^[a-zA-Z0-9_.+-]+$ ]] || {
    echo 'Mock driver requires an HTTPS repository, full commit SHA and pinned running kernel.' >&2
    exit 1
  }
fi

if [[ "$NUM_OF_WORKERS" -eq 0 ]]; then
  SINGLE_NODE=true
  total_number_of_nodes=1
  registry_storage=20Gi
else
  SINGLE_NODE=false
  total_number_of_nodes=$((1 + NUM_OF_WORKERS))
  registry_storage=60Gi
fi

if [[ ${#network_name} -gt 15 || ${#sriov_network_name} -gt 15 ]]; then
  echo "NETWORK_NAME and SRIOV_NETWORK_NAME must be at most 15 characters for libvirt bridges" >&2
  exit 1
fi

check_requirements() {
  local -a cmds=(kcli virsh podman make go)
  [[ -z $MOCK_SMARTNIC_REPO ]] || cmds+=(python3 timeout)
  for cmd in "${cmds[@]}"; do
    if ! command -v "$cmd" &> /dev/null; then
      echo "$cmd is not available"
      exit 1
    fi
  done
}

# kcli delete wrapper; ignore failure only if output confirms the resource is absent.
kcli_delete() {
  local out rc=0
  out=$(kcli delete "$@" -y 2>&1) || rc=$?
  [[ -n "$out" ]] && printf '%s\n' "$out"
  if [[ $rc -ne 0 ]] && ! grep -qi 'not found' <<<"$out"; then
    return "$rc"
  fi
  return 0
}

cleanup() {
  echo "## cleaning up cluster $cluster_name"
  kcli_delete cluster "$cluster_name"
  kcli_delete network "$cluster_name"
  kcli_delete network "${sriov_network_name}"
  kcli_delete network "${network_name}"
  sudo rm -f "/etc/containers/registries.conf.d/003-${cluster_name}.conf"
}

create_networks() {
  kcli create network -c "${network_cidr}" "${network_name}"
  kcli create network -c "${sriov_network_cidr}" --nodhcp -i "${sriov_network_name}"
  virsh net-info "${network_name}" >/dev/null
  virsh net-info "${sriov_network_name}" >/dev/null
}

write_cluster_plan() {
  local sriov_vmrule
  sriov_vmrule=$(cat <<EOF
      nets:
        - name: ${network_name}
          type: igb
          vfio: true
          noconf: true
          numa: 0
        - name: ${sriov_network_name}
          type: igb
          vfio: true
          noconf: true
          numa: 1
      numcpus: 6
      numa:
        - id: 0
          vcpus: 0,2,4
          memory: 2048
        - id: 1
          vcpus: 1,3,5
          memory: 2048
EOF
)

  cat <<EOF > "./${cluster_name}-plan.yaml"
version: $cluster_version
ctlplane_memory: 4096
worker_memory: 4096
pool: default
disk_size: 30
network: ${network_name}
api_ip: $api_ip
virtual_router_id: $virtual_router_id
domain: $domain_name
ctlplanes: 1
workers: $NUM_OF_WORKERS
ingress: false
machine: q35
engine: crio
sdn: flannel
cluster_network_ipv4: ${pod_cidr}
service_network_ipv4: ${service_cidr}
feature_gates:
  - DynamicResourceAllocation
  - DRAPartitionableDevices
  - DRAConsumableCapacity
  - DRADeviceCompatibilityGroups
autolabeller: false
vmrules:
$(if $SINGLE_NODE; then
  printf '  - %s-ctlplane-.*:\n%s\n' "$cluster_name" "$sriov_vmrule"
else
  cat <<VMRULES
  - $cluster_name-ctlplane-.*:
      nets:
        - name: ${network_name}
          type: igb
          vfio: true
          noconf: true
  - $cluster_name-worker-.*:
$sriov_vmrule
VMRULES
fi)
EOF
}

wait_for_cluster_ready() {
  local attempts=0
  local max_attempts=72
  local ready=false
  local sleep_time=10

  until $ready || [[ $attempts -eq $max_attempts ]]; do
    if [[ $(kubectl get nodes --no-headers | grep -c " Ready ") -eq $total_number_of_nodes ]]; then
      ready=true
    fi
    if $ready; then
      echo "Cluster is ready"
    else
      echo "Cluster is not ready yet"
      sleep "$sleep_time"
    fi
    attempts=$((attempts + 1))
  done

  if ! $ready; then
    echo "Timed out waiting for cluster to be ready"
    kubectl get nodes
    exit 1
  fi
}

label_nodes() {
  if $SINGLE_NODE; then
    echo "## untaint control plane to allow scheduling workloads"
    kubectl taint nodes --all node-role.kubernetes.io/control-plane- || true

    echo "## label control plane as sriov capable and as worker"
    kubectl label node "${cluster_name}-ctlplane-0.${domain_name}" \
      feature.node.kubernetes.io/network-sriov.capable=true --overwrite
    kubectl label node "${cluster_name}-ctlplane-0.${domain_name}" \
      node-role.kubernetes.io/worker= --overwrite
    return
  fi

  echo "## label cluster workers as sriov capable"
  for ((num=0; num<NUM_OF_WORKERS; num++)); do
    kubectl label node "${cluster_name}-worker-${num}.${domain_name}" \
      feature.node.kubernetes.io/network-sriov.capable=true --overwrite
  done

  echo "## label cluster workers as worker"
  for ((num=0; num<NUM_OF_WORKERS; num++)); do
    kubectl label node "${cluster_name}-worker-${num}.${domain_name}" \
      node-role.kubernetes.io/worker= --overwrite
  done
}

get_controller_ip() {
  controller_ip=$(kubectl get node "${cluster_name}-ctlplane-0.${domain_name}" \
    -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
  if [[ -z "$controller_ip" ]]; then
    echo "## ERROR: Failed to get controller IP"
    kubectl get nodes -o wide
    exit 1
  fi
}

configure_host_registry() {
  insecure_registry="[[registry]]
location = \"$controller_ip:5000\"
insecure = true

[aliases]
\"golang\" = \"docker.io/library/golang\"
"

  sudo bash -c "cat << EOF > /etc/containers/registries.conf.d/003-${cluster_name}.conf
$insecure_registry
EOF"
}

update_host() {
  local node_name=$1
  kcli ssh "$node_name" << EOF
sudo su
set -e
echo '$insecure_registry' > /etc/containers/registries.conf.d/003-internal.conf
systemctl restart crio

mkdir -p /opt/cni/bin
if command -v curl >/dev/null 2>&1; then
  curl -fL --retry 3 --retry-delay 2 --output /tmp/cni-plugins-linux-amd64-v1.9.1.tgz ${CNI_PLUGINS_URL}
elif command -v wget >/dev/null 2>&1; then
  wget -qO /tmp/cni-plugins-linux-amd64-v1.9.1.tgz ${CNI_PLUGINS_URL}
else
  echo "Neither curl nor wget is installed on the node" >&2
  exit 1
fi
# Unlink old executables so active CNI processes can finish on their existing inode.
tar --unlink-first -xzf /tmp/cni-plugins-linux-amd64-v1.9.1.tgz -C /opt/cni/bin
rm -f /tmp/cni-plugins-linux-amd64-v1.9.1.tgz

cat > /etc/NetworkManager/system-connections/multi.nmconnection << 'NMEOF'
[connection]
id=multi
type=ethernet
[ethernet]
[match]
driver=igbvf;
[ipv4]
method=disabled
[ipv6]
addr-gen-mode=default
method=disabled
[proxy]
NMEOF
chmod 600 /etc/NetworkManager/system-connections/multi.nmconnection

cat > /etc/systemd/system/disable-offload.service << 'OFFLOADEOF'
[Unit]
Description=disable checksum offload to avoid vf bug
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/bin/bash -ec "if command -v ethtool >/dev/null 2>&1; then for nic in eth0 eth1; do [ -d \"/sys/class/net/\$nic\" ] || continue; ethtool --offload \"\$nic\" rx off tx off || true; ethtool -K \"\$nic\" gso off || true; done; fi"
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=default.target
OFFLOADEOF

systemctl daemon-reload
systemctl enable disable-offload.service
systemctl start disable-offload.service || true

cat > /etc/systemd/system/load-br-netfilter.service << 'BRNFEOF'
[Unit]
Description=load br_netfilter
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/bin/bash -c "modprobe br_netfilter"
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=default.target
BRNFEOF

systemctl daemon-reload
systemctl enable --now load-br-netfilter

# The mock installer needs zero VFs; it must never take over a management PF.
if [[ -z '${MOCK_SMARTNIC_REPO}' ]]; then
cat > /usr/local/bin/create-sriov-vfs.sh << 'VFSCRIPT'
#!/usr/bin/bash
set -euo pipefail
created=0
for driver_link in /sys/bus/pci/devices/*/driver; do
  [[ -e "\$driver_link" ]] || continue
  readlink -f "\$driver_link" | grep -q '/igb\$' || continue
  pf="\$(dirname "\$driver_link")"
  addr="\$(basename "\$pf")"
  [[ -w "\$pf/sriov_numvfs" ]] || continue
  [[ -r "\$pf/sriov_totalvfs" ]] || continue
  totalvfs="\$(cat "\$pf/sriov_totalvfs")"
  [[ "\$totalvfs" -ge 5 ]] || continue
  echo 0 > "\$pf/sriov_numvfs" || true
  echo 5 > "\$pf/sriov_numvfs"
  echo "Created VFs on \$addr"
  created=\$((created + 1))
done
if [[ "\$created" -eq 0 ]]; then
  echo "error: no igb PF with writable sriov_numvfs and sriov_totalvfs >= 5" >&2
  exit 1
fi
VFSCRIPT
chmod +x /usr/local/bin/create-sriov-vfs.sh

cat > /etc/systemd/system/create-sriov-vfs.service << 'SRIOVSVCEOF'
[Unit]
Description=create sriov vfs
After=systemd-udevd.service
Before=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
# Needed on SELinux enforcing hosts where init_t may be blocked from sysfs writes.
SELinuxContext=system_u:system_r:unconfined_service_t:s0
ExecStart=/usr/local/bin/create-sriov-vfs.sh
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=network-pre.target
SRIOVSVCEOF

restorecon /etc/systemd/system/create-sriov-vfs.service || true
test -s /etc/systemd/system/create-sriov-vfs.service
systemctl daemon-reload
systemctl reenable create-sriov-vfs.service
systemctl start create-sriov-vfs.service || true
fi

systemctl restart NetworkManager

grubby --update-kernel=DEFAULT --args=pci=realloc
grubby --update-kernel=DEFAULT --args=iommu=pt
grubby --update-kernel=DEFAULT --args=intel_iommu=on

if [[ -z '${MOCK_SMARTNIC_REPO}' ]]; then
cat > /etc/systemd/system/load-vfio.service << 'VFIOEOF'
[Unit]
Description=load VFIO modules for DRA VFIO demos
After=network.target

[Service]
Type=oneshot
ExecStart=/usr/bin/bash -c "modprobe vfio && modprobe vfio_iommu_type1 && modprobe vfio-pci"
RemainAfterExit=yes
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=multi-user.target
VFIOEOF

systemctl daemon-reload
systemctl enable --now load-vfio
fi

EOF
}

restart_sriov_host() {
  if [[ -n $MOCK_SMARTNIC_REPO ]]; then
    local boot
    boot=$(timeout 20 kcli ssh "$1" cat /proc/sys/kernel/random/boot_id)
    [[ $boot =~ ^[0-9a-f-]{36}$ ]] || { echo 'Cannot record host boot ID' >&2; return 1; }
    MOCK_SMARTNIC_BOOTS[$1]=$boot
  fi
  kcli restart vm "$1"
}

configure_hosts_and_reboot() {
  update_host "${cluster_name}-ctlplane-0"

  # Single-node reboots the control plane VM
  if $SINGLE_NODE; then
    restart_sriov_host "${cluster_name}-ctlplane-0"
    return
  fi

  for ((num=0; num<NUM_OF_WORKERS; num++)); do
    update_host "${cluster_name}-worker-${num}"
    restart_sriov_host "${cluster_name}-worker-${num}"
  done
}

# Select the attested secondary NIC; management may also use igb.
setup_mock_smartnic() {
  local vm=$1 mac args attempt boot ready=false
  mac=$(python3 - "$vm" "$sriov_network_name" <<'PYXML'
import subprocess, sys, xml.etree.ElementTree as ET
vm, network = sys.argv[1:]
xml = ET.fromstring(subprocess.check_output(["virsh", "dumpxml", vm]))
net = ET.fromstring(subprocess.check_output(["virsh", "net-dumpxml", network]))
if xml.findall("./devices/hostdev") or xml.findall("./devices/interface[@type='hostdev']"):
    raise SystemExit("Mock workers must not have PCI passthrough")
bridge = net.find("bridge")
nics = [nic for nic in xml.findall("./devices/interface")
        if nic.find("model") is not None and nic.find("model").get("type") == "igb"
        and nic.find("source") is not None
        and (nic.find("source").get("network") == network or
             (bridge is not None and nic.find("source").get("bridge") == bridge.get("name")))]
if len(nics) != 1:
    raise SystemExit("Need exactly one emulated igb NIC on the dedicated test network")
print(nics[0].find("mac").get("address"))
PYXML
  ) || return 1
  for ((attempt=0; attempt<60; attempt++)); do
    boot=$(timeout 10 kcli ssh "$vm" cat /proc/sys/kernel/random/boot_id) || boot=
    if [[ $boot =~ ^[0-9a-f-]{36}$ && $boot != "${MOCK_SMARTNIC_BOOTS[$vm]}" ]]; then ready=true; break; fi
    sleep 5
  done
  $ready || { echo "Timed out waiting for $vm SSH" >&2; return 1; }
  printf -v args '%q ' "$MOCK_SMARTNIC_REPO" "$MOCK_SMARTNIC_REF" "$MOCK_SMARTNIC_KERNEL" "$mac"
  timeout 900 kcli ssh "$vm" "sudo --preserve-env=SSH_CONNECTION bash -s -- $args" <<'WORKER'
set -Eeuo pipefail
repo=$1 ref=$2 kernel=$3 mac=$4
[[ $(uname -r) == "$kernel" ]] || { echo 'Provision a worker with the pinned kernel before mock setup.' >&2; exit 1; }
# libvirt MAC attestation maps to exactly one PCI PF; the installer checks usage.
names=()
for path in /sys/class/net/*; do
  [[ $(cat "$path/address") == "$mac" ]] && names+=("$path")
done
((${#names[@]} == 1))
pf=$(basename "$(readlink -f "${names[0]}/device")")
checkout=/var/tmp/switchdev-mock-driver
[[ ! -e $checkout ]] || { echo "Refusing to overwrite $checkout" >&2; exit 1; }
command -v git >/dev/null || dnf install -y git
git clone -- "$repo" "$checkout"
git -C "$checkout" checkout --detach "$ref"
[[ $(git -C "$checkout" rev-parse HEAD) == "$ref" ]]
# Refuse foreign provisioning state before installing anything.
[[ ! -e /usr/local/bin/create-sriov-vfs.sh && ! -e /etc/systemd/system/create-sriov-vfs.service && ! -e /etc/systemd/system/kubelet.service.d/31-mock-smartnic-vfs.conf ]]
"$checkout/scripts/setup-worker.sh" --pf "$pf" --kernel "$kernel" --emulated-pf
# DRA discovers existing VFs; unlike the SR-IOV operator it does not create them.
# Keep VF creation after the persisted exact-PF binding, including at boot.
cat > /usr/local/bin/create-sriov-vfs.sh <<'VFSCRIPT'
#!/usr/bin/env bash
set -Eeuo pipefail
pf=$1
[[ $pf =~ ^[0-9a-f]{4}:[0-9a-f]{2}:[01][0-9a-f]\.[0-7]$ ]]
p=/sys/bus/pci/devices/$pf
[[ $(cat /sys/module/mock_smartnic/parameters/target_pf) == "$pf" ]]
[[ $(basename "$(readlink -f "$p/driver")") == mock_smartnic_pf ]]
[[ $(cat "$p/sriov_totalvfs") -ge 5 && $(cat "$p/sriov_drivers_autoprobe") == 1 ]]
num=$(cat "$p/sriov_numvfs")
if [[ $num == 0 ]]; then
  printf '5\n' > "$p/sriov_numvfs"
else
  [[ $num == 5 ]] || { echo 'Unexpected VF count; refusing to destroy allocations.' >&2; exit 1; }
fi
for index in {0..4}; do
  vf=$(readlink -f "$p/virtfn$index")
  [[ $(readlink -f "$vf/physfn") == "$p" ]]
  [[ $(basename "$(readlink -f "$vf/driver")") == mock_smartnic_vf ]]
done
VFSCRIPT
chmod 755 /usr/local/bin/create-sriov-vfs.sh
cat > /etc/systemd/system/create-sriov-vfs.service <<VFUNIT
[Unit]
Description=Create VFs only on the dedicated mock SmartNIC PF
Requires=mock-smartnic-lab.service
After=mock-smartnic-lab.service
Before=kubelet.service
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/bin/create-sriov-vfs.sh $pf
[Install]
WantedBy=multi-user.target
VFUNIT
cat > /etc/systemd/system/kubelet.service.d/31-mock-smartnic-vfs.conf <<'KUBELET'
[Unit]
Requires=create-sriov-vfs.service
After=create-sriov-vfs.service
KUBELET
if command -v restorecon >/dev/null; then
  restorecon /usr/local/bin/create-sriov-vfs.sh /etc/systemd/system/create-sriov-vfs.service
fi
systemctl daemon-reload
systemctl enable --now create-sriov-vfs.service
devlink dev eswitch show "pci/$pf"
echo "Prepared five VFs on $pf; DRA policies manage the eswitch mode."
WORKER
}

collect_node_diagnostics() {
  kubectl get nodes -o wide || true
  kubectl get events -A --sort-by=.lastTimestamp || true
  kubectl describe node "${cluster_name}-ctlplane-0.${domain_name}" || true
  for ((num=0; num<NUM_OF_WORKERS; num++)); do
    kubectl describe node "${cluster_name}-worker-${num}.${domain_name}" || true
  done
}

wait_for_nodes_after_reboot() {
  # Single-node reboots the control plane VM, so the API server is down until
  # the VM finishes booting. Multi-node only reboots workers; the API stays up.
  if $SINGLE_NODE; then
    sleep 60
  fi

  local attempts=0
  local max_attempts=40
  local ready=false
  local sleep_time=15

  until $ready || [[ $attempts -eq $max_attempts ]]; do
    echo "waiting for API server to be reachable after reboot (attempt $attempts)"
    if kubectl get nodes &>/dev/null; then
      echo "API server is reachable, waiting for node ready..."
      if kubectl wait --for=condition=ready node --all --timeout=60s 2>/dev/null; then
        echo "node(s) ready"
        ready=true
      fi
    else
      sleep "$sleep_time"
    fi
    attempts=$((attempts + 1))
  done

  if ! $ready; then
    echo "## node readiness wait failed; collecting diagnostics"
    collect_node_diagnostics
    exit 1
  fi
}

fix_cni_bridge() {
  local node_pod_cidr
  node_pod_cidr=$(kubectl get node "${cluster_name}-ctlplane-0.${domain_name}" \
    -o jsonpath='{.spec.podCIDR}')
  if [[ -z "$node_pod_cidr" ]]; then
    echo "Control plane has no assigned Pod CIDR" >&2
    return 1
  fi
  kcli ssh "${cluster_name}-ctlplane-0" << EOF
sudo su
if ! ip -4 route show dev cni0 2>/dev/null | grep -Fq "${node_pod_cidr} "; then
  ip link del cni0 2>/dev/null || true
fi
EOF
}

recover_pod_networking() {
  local timeout=400

  kubectl -n kube-system delete po -l k8s-app=kube-dns --ignore-not-found=true

  echo "## wait for coredns"
  kubectl -n kube-system wait --for=condition=available deploy/coredns --timeout=${timeout}s

}

deploy_internal_registry() {
  kubectl create namespace container-registry --dry-run=client -o yaml | kubectl apply -f -

  echo "## deploy internal registry"
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolume
metadata:
  name: registry-pv
spec:
  capacity:
    storage: ${registry_storage}
  volumeMode: Filesystem
  accessModes:
  - ReadWriteOnce
  persistentVolumeReclaimPolicy: Delete
  storageClassName: registry-local-storage
  local:
    path: /mnt/
  nodeAffinity:
    required:
      nodeSelectorTerms:
      - matchExpressions:
        - key: kubernetes.io/hostname
          operator: In
          values:
          - ${cluster_name}-ctlplane-0.${domain_name}
EOF

  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: registry-pv-claim
  namespace: container-registry
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: ${registry_storage}
  storageClassName: registry-local-storage
EOF

  cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
  namespace: container-registry
spec:
  replicas: 1
  selector:
    matchLabels:
      app: registry
  template:
    metadata:
      labels:
        app: registry
    spec:
      hostNetwork: true
      tolerations:
        - effect: NoSchedule
          key: node-role.kubernetes.io/control-plane
      containers:
      - image: quay.io/libpod/registry:2.8.2
        imagePullPolicy: Always
        name: registry
        volumeMounts:
        - name: data
          mountPath: /var/lib/registry
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: registry-pv-claim
      terminationGracePeriodSeconds: 10
EOF

  echo "## wait for registry to be ready"
  kubectl -n container-registry wait --for=condition=available deploy/registry --timeout=120s
}

build_and_push_driver_image() {
  export DRIVER_IMAGE="$controller_ip:5000/dra-multi-network"

  CONTAINER_TOOL=podman IMAGE_NAME="${DRIVER_IMAGE}" make -C deployments/container/
  podman push --tls-verify=false "${DRIVER_IMAGE}"
  podman rmi -fi "${DRIVER_IMAGE}"
}

deploy_dra_driver() {
  set +e
  make helm
  set -e
  local -a mock_values=()
  [[ -z $MOCK_SMARTNIC_REPO ]] || mock_values+=(--set kubeletPlugin.ovs.enabled=true)
  "${root}/bin/helm" upgrade -i dra-multi-network deployments/helm/dra-multi-network/ \
    --namespace dra-multi-network --create-namespace \
    --set "image.repository=${DRIVER_IMAGE}" "${mock_values[@]}"
}

wait_for_dra_driver_daemonset() {
  echo "## Waiting for daemonset to be ready..."
  local attempts=0
  local max_attempts=60

  while [[ $attempts -lt $max_attempts ]]; do
    local desired ready
    desired=$(kubectl -n dra-multi-network get ds/dra-multi-network-kubeletplugin \
      -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo "0")
    ready=$(kubectl -n dra-multi-network get ds/dra-multi-network-kubeletplugin \
      -o jsonpath='{.status.numberReady}' 2>/dev/null || echo "0")

    if [[ -n "$desired" && "$desired" != "0" && "$desired" == "$ready" ]]; then
      echo "## Daemonset is ready ($ready/$desired)"
      return 0
    fi
    echo "## Waiting for daemonset to be ready ($ready/$desired)..."
    sleep 5
    attempts=$((attempts + 1))
  done

  echo "## ERROR: Timed out waiting for daemonset to be ready"
  kubectl -n dra-multi-network get ds -o wide || true
  kubectl -n dra-multi-network get pods || true
  exit 1
}

verify_vfs_on_host() {
  local host_name=$1
  echo "## verify VFs were created after reboot on ${host_name}"
  kcli ssh "$host_name" << 'VERIFY_EOF'
echo "=== PCI ethernet devices ==="
if command -v lspci >/dev/null 2>&1; then
  lspci | grep -i ethernet
else
  ls /sys/bus/pci/drivers/igb/ 2>/dev/null
fi
echo "=== igb driver bindings ==="
ls -la /sys/bus/pci/drivers/igb/ 2>/dev/null | grep -E "^l" || echo "no igb bindings"
echo "=== sriov_totalvfs for all PCI devices ==="
for dev in /sys/bus/pci/devices/*/sriov_totalvfs; do
  if [ -f "$dev" ]; then
    addr=$(basename $(dirname $dev))
    total=$(cat $dev)
    numvfs=$(cat $(dirname $dev)/sriov_numvfs 2>/dev/null || echo "N/A")
    driver=$(basename $(readlink -f $(dirname $dev)/driver) 2>/dev/null || echo "none")
    echo "  $addr: driver=$driver totalvfs=$total numvfs=$numvfs"
  fi
done
echo "=== virtfn symlinks ==="
for dev in /sys/bus/pci/devices/*/virtfn0; do
  if [ -L "$dev" ]; then
    pf_dir=$(dirname $dev)
    count=$(find "$pf_dir" -maxdepth 1 -type l -name 'virtfn*' | wc -l)
    echo "  $(basename $pf_dir) has $count VFs"
  fi
done
echo "=== create-sriov-vfs service status ==="
systemctl status create-sriov-vfs.service --no-pager 2>&1 | head -10 || true
VERIFY_EOF
}

verify_vfs_and_restart_driver() {
  local -a sriov_hosts
  if $SINGLE_NODE; then
    sriov_hosts=("${cluster_name}-ctlplane-0")
  else
    for ((num=0; num<NUM_OF_WORKERS; num++)); do
      sriov_hosts+=("${cluster_name}-worker-${num}")
    done
  fi

  for host in "${sriov_hosts[@]}"; do
    verify_vfs_on_host "$host"
  done

  echo "## restart DRA driver pods so they discover newly created VFs"
  kubectl -n dra-multi-network delete pod -l app.kubernetes.io/name=dra-multi-network \
    --force --grace-period=0 || true

  echo "## wait for DRA driver pod to be ready again"
  sleep 10
  kubectl -n dra-multi-network wait --for=condition=ready pod \
    -l app.kubernetes.io/name=dra-multi-network --timeout=120s
}

if [[ "$render_only" == true ]]; then
  if [[ "$cleanup_only" == true ]]; then
    echo "--render cannot be combined with --cleanup" >&2
    exit 1
  fi
  write_cluster_plan
  cat "./${cluster_name}-plan.yaml"
  exit 0
fi

echo "## Checking requirements"
check_requirements

if [[ "$cleanup_only" == true ]]; then
  echo "## Only cleaning up existing cluster"
  cleanup
  exit 0
fi

echo "## Cleaning up existing cluster"
cleanup

echo "## Creating networks"
create_networks
write_cluster_plan

kcli create cluster generic --paramfile "./${cluster_name}-plan.yaml" "$cluster_name"

export KUBECONFIG="$HOME/.kcli/clusters/$cluster_name/auth/kubeconfig"
if [[ ! -f "${KUBECONFIG}" ]]; then
  echo "Cluster bootstrap failed: missing kubeconfig at ${KUBECONFIG}"
  exit 1
fi
export PATH=$PWD:$PATH

echo "## Waiting for cluster to be ready"
wait_for_cluster_ready

echo "## Labeling nodes"
label_nodes

echo "## Configuring hosts and rebooting"
get_controller_ip
configure_host_registry
configure_hosts_and_reboot

echo "## Waiting for nodes to be ready after reboot"
wait_for_nodes_after_reboot

if [[ -n $MOCK_SMARTNIC_REPO ]]; then
  if $SINGLE_NODE; then
    setup_mock_smartnic "${cluster_name}-ctlplane-0"
  else
    for ((num=0; num<NUM_OF_WORKERS; num++)); do
      setup_mock_smartnic "${cluster_name}-worker-${num}"
    done
  fi
fi

# Single-node reboots the control plane VM, which can leave a stale cni0 bridge
# without the node's assigned Flannel Pod subnet. Multi-node only reboots workers,
# so this fix is not needed (and would target the wrong node anyway).
if $SINGLE_NODE; then
  echo "## Fixing cni0 bridge for single-node cluster"
  fix_cni_bridge
fi

echo "## Recovering pod networking"
recover_pod_networking


echo "## Deploying internal registry"
deploy_internal_registry

echo "## Building and pushing driver image"
build_and_push_driver_image

echo "## Deploying DRA driver"
deploy_dra_driver
wait_for_dra_driver_daemonset

verify_vfs_and_restart_driver
echo "## Cluster deployed successfully"

echo "## KUBECONFIG=${KUBECONFIG}"
echo "## Controller IP: ${controller_ip}"
echo "## Driver image: ${DRIVER_IMAGE}"

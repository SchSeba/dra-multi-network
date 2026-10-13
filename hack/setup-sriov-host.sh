#!/usr/bin/env bash
# Sent to an XML-attested guest; prepare native and mock PFs before DRA starts.
set -Eeuo pipefail
repo=$1 native_mac=$2 mock_mac=$3
native=/sys/class/net/sriovtest0/device
[[ $(cat /sys/class/net/sriovtest0/address) == "$native_mac" ]]
[[ $(basename "$(readlink -f "$native/driver")") == igb ]]
[[ $(cat "$native/sriov_numvfs") == 0 ]]
native_pf=$(basename "$(readlink -f "$native")")
mock_pf=
if [[ -n $repo ]]; then
  names=()
  for path in /sys/class/net/*; do
    [[ $(cat "$path/address") == "$mock_mac" ]] && names+=("$path")
  done
  ((${#names[@]} == 1))
  mock_pf=$(basename "$(readlink -f "${names[0]}/device")")
  [[ $mock_pf != "$native_pf" ]]
  checkout=/var/tmp/switchdev-mock-driver
  [[ ! -e $checkout ]]
  command -v git >/dev/null || dnf install -y git
  git clone --depth 1 --single-branch --branch main -- "$repo" "$checkout"
  git -C "$checkout" rev-parse HEAD
  "$checkout/scripts/setup-worker.sh" --pf "$mock_pf" --emulated-pf
fi
# The installer runs before creating native VFs so it cannot displace igbvf users.
cat > /usr/local/bin/create-sriov-vfs.sh <<'VFS'
#!/usr/bin/env bash
set -Eeuo pipefail
native_pf=$1 mock_pf=${2:-}
modprobe igbvf
for pf in "$native_pf" "$mock_pf"; do
  [[ -n $pf ]] || continue
  [[ $pf =~ ^[0-9a-f]{4}:[0-9a-f]{2}:[01][0-9a-f]\.[0-7]$ ]]
  p=$(readlink -f "/sys/bus/pci/devices/$pf")
  driver=igb; vfdriver=igbvf
  if [[ $pf == "$mock_pf" ]]; then driver=mock_smartnic_pf; vfdriver=mock_smartnic_vf; fi
  [[ $(basename "$(readlink -f "$p/driver")") == "$driver" ]]
  [[ $(cat "$p/sriov_totalvfs") -ge 4 && $(cat "$p/sriov_drivers_autoprobe") == 1 ]]
  num=$(cat "$p/sriov_numvfs")
  if [[ $num == 0 ]]; then
    printf '4\n' > "$p/sriov_numvfs"
  else
    [[ $num == 4 ]] || { echo 'Unexpected VF count; refusing to destroy allocations.' >&2; exit 1; }
  fi
  for index in {0..3}; do
    vf=$(readlink -f "$p/virtfn$index")
    [[ $(readlink -f "$vf/physfn") == "$p" ]]
    [[ $(basename "$(readlink -f "$vf/driver")") == "$vfdriver" ]]
  done
  name=$(ls "$p/net")
  ip link set "$name" up
done
VFS
chmod 755 /usr/local/bin/create-sriov-vfs.sh
dependency=
[[ -z $mock_pf ]] || dependency='Requires=mock-smartnic-lab.service
After=mock-smartnic-lab.service'
cat > /etc/systemd/system/create-sriov-vfs.service <<UNIT
[Unit]
Description=Create VFs on the attested native and mock test PFs
After=systemd-udev-settle.service
$dependency
Before=kubelet.service
[Service]
Type=oneshot
RemainAfterExit=yes
SELinuxContext=system_u:system_r:unconfined_service_t:s0
ExecStart=/usr/local/bin/create-sriov-vfs.sh $native_pf $mock_pf
[Install]
WantedBy=multi-user.target
UNIT
mkdir -p /etc/systemd/system/kubelet.service.d
cat > /etc/systemd/system/kubelet.service.d/31-test-vfs.conf <<'UNIT'
[Unit]
Requires=create-sriov-vfs.service
After=create-sriov-vfs.service
UNIT
restorecon /usr/local/bin/create-sriov-vfs.sh /etc/systemd/system/create-sriov-vfs.service
systemctl daemon-reload
systemctl enable --now create-sriov-vfs.service

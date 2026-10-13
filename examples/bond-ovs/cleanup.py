#!/usr/bin/env python3
"""Drain the combined example before restoring its managed eswitch modes."""
import importlib.util
import json
import os
from pathlib import Path
import re
import shlex
import subprocess

spec = importlib.util.spec_from_file_location('manage', Path(__file__).resolve().parent.parent / 'manage.py')
manage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(manage)
cluster = os.environ.get('CLUSTER_NAME', 'multi-network')
os.environ.setdefault('KUBECONFIG', str(Path.home() / f'.kcli/clusters/{cluster}/auth/kubeconfig'))


def guest(vm, script):
    subprocess.run(['kcli', 'ssh', vm, 'sudo bash -s'], input=script, text=True, check=True)


def cleanup():
    policies = [p for p in manage.get('deviceexposurepolicies')['items'] if re.fullmatch(r'eswitch-[0-9]+', p['metadata']['name'])]
    for policy in policies:
        index = policy['metadata']['name'].removeprefix('eswitch-')
        namespace = f'bond-ovs-{index}'
        classes = set()
        for topology in [f'bond-vlan-{index}', f'switchdev-ovs-{index}']:
            obj = manage.get('networktopology', topology)
            if obj:
                classes.update(r['className'] for r in obj.get('status', {}).get('rootClasses', []))
        if manage.get('namespace', namespace):
            manage.remove_pods(namespace, ['network-mvp', 'network-mvp-peer', 'switchdev-ovs-a', 'switchdev-ovs-b'], classes)
            if manage.get('pods', namespace=namespace)['items'] or manage.get('resourceclaims', namespace=namespace)['items']:
                raise ValueError(f'{namespace} still has other consumers; refusing namespace deletion')
            manage.run('kubectl', 'delete', 'namespace', namespace, '--wait=true', '--timeout=180s')
        for kind in ['networktopology', 'deviceexposurepolicy']:
            for name in [f'bond-vlan-{index}', f'switchdev-ovs-{index}']:
                manage.run('kubectl', 'delete', kind, name, '--ignore-not-found', '--wait=true', '--timeout=180s')
    # Mode restoration requires a whole-node driver drain, including other examples.
    claims = subprocess.check_output(['kubectl', 'get', 'resourceclaims', '-A', '-o', 'json'], text=True)
    if any('dra-multi-network.schseba.github.io' in json.dumps(c.get('status', {}).get('allocation', {})) for c in json.loads(claims)['items']):
        raise ValueError('Drain remaining driver claims before restoring eswitch modes')
    for policy in policies:
        node = policy['spec']['nodeSelector']['matchLabels']['kubernetes.io/hostname']
        vm = node.removesuffix(f'.{cluster}.lab')
        # Read the PF from the installed exact-PF module configuration, never a NIC index.
        was_cordoned = manage.get('node', node)['spec'].get('unschedulable', False)
        manage.run('kubectl', 'cordon', node)
        pf = policy['spec']['selector']['pciAddresses'][0]
        prefix = 'set -euo pipefail\npf=' + shlex.quote(pf) + '\n[[ $(cat /sys/module/mock_smartnic/parameters/target_pf) == "$pf" ]]\n'
        guest(vm, prefix + '''
p=/sys/bus/pci/devices/$pf
[[ $(basename "$(readlink -f "$p/driver")") == mock_smartnic_pf ]]
ip link set "$(ls "$p/net")" down
''')
        manage.run('kubectl', 'delete', 'deviceexposurepolicy', policy['metadata']['name'], '--wait=true', '--timeout=180s')
        guest(vm, prefix + '''
ip link set "$(ls /sys/bus/pci/devices/$pf/net)" up
devlink dev eswitch show "pci/$pf"
''')
        if not was_cordoned:
            manage.run('kubectl', 'uncordon', node)


if __name__ == '__main__':
    cleanup()

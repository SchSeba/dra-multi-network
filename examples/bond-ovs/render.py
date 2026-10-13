#!/usr/bin/env python3
"""Render the existing examples against a node's attested test hardware."""
import json
from pathlib import Path
import sys
import yaml


def render(hardware):
    examples = Path(__file__).resolve().parent.parent
    node, suffix = hardware['node'], hardware['suffix']
    native, mock = hardware['native'], hardware['mock']
    if native == mock or len(set(hardware['vfs'])) != 2:
        raise ValueError('Native and mock PCI families must be distinct')
    selector = {'matchLabels': {'kubernetes.io/hostname': node}}
    bond_name, ovs_name = 'bond-vlan-' + suffix, 'switchdev-ovs-' + suffix
    def load(name):
        return yaml.safe_load((examples / name).read_text())
    native_policy = load('bond-vlan/policy.yaml')
    native_policy['metadata']['name'] = bond_name
    native_policy['spec']['nodeSelector'] = selector
    native_policy['spec']['selector']['parentPCIAddresses'] = [native]
    native_policy['spec']['selector']['driver'] = 'igbvf'
    native_policy['spec']['exposure']['persona'] = bond_name
    bond = load('bond-vlan/topology.yaml')
    bond['metadata']['name'] = bond_name
    for step in bond['spec']['steps']:
        if step['kind'] == 'Allocation':
            step['selectors'] = [
                f'device.attributes["dra-multi-network.schseba.github.io"].pfPciAddress == "{native}"',
                f'device.attributes["dra-multi-network.schseba.github.io"].persona == "{bond_name}"',
            ]
    eswitch = load('switchdev/eswitch-policy.yaml')
    eswitch['metadata']['name'] = 'eswitch-' + suffix
    eswitch['spec']['nodeSelector'] = selector
    eswitch['spec']['selector']['pciAddresses'] = [mock]
    ovs_policy = load('switchdev/ovs/policy.yaml')
    ovs_policy['metadata']['name'] = ovs_name
    ovs_policy['spec']['nodeSelector'] = selector
    ovs_policy['spec']['selector']['parentPCIAddresses'] = [mock]
    ovs_policy['spec']['selector']['pciAddresses'] = hardware['vfs']
    ovs_policy['spec']['exposure']['persona'] = ovs_name
    ovs_policy['spec']['exposure']['allowedOVSBridges'][0]['uuid'] = hardware['bridgeUUID']
    ovs = load('switchdev/ovs/topology.yaml')
    ovs['metadata']['name'] = ovs_name
    ovs['spec']['steps'][0]['selectors'] = [
        'device.attributes["dra-multi-network.schseba.github.io"].pciAddress in ' + json.dumps(hardware['vfs']),
        f'device.attributes["dra-multi-network.schseba.github.io"].persona == "{ovs_name}"',
    ]
    return [native_policy, bond, eswitch, ovs_policy, ovs]


if __name__ == '__main__':
    yaml.safe_dump_all(render(json.load(sys.stdin)), sys.stdout, sort_keys=False)

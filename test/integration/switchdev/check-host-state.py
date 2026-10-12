#!/usr/bin/env python3
"""Read-only checks for the documented four-VF mock fixture; run on its node."""
import json
from pathlib import Path
import subprocess
import sys


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def read_json(*args):
    return json.loads(command(*args))


def ovs_table(table, columns):
    data = read_json('ovs-vsctl', '--format=json', '--columns=' + columns, 'list', table)
    return sorted(data['data'], key=lambda row: json.dumps(row))


def stable_link_info(info):
    if info and 'info_data' in info:
        info = {**info, 'info_data': {key: value for key, value in info['info_data'].items()
                                     if not key.endswith('_timer')}}
    return info


def snapshot():
    links = read_json('ip', '-j', '-d', 'link', 'show')
    # Link-state restoration concerns admin state, not transient carrier/stats.
    selected = {}
    for link in links:
        name = link['ifname']
        if name.startswith('msnic') or name in ('swbr0', 'ovsbr0', 'ovs-system'):
            selected[name] = {key: link.get(key) for key in
                              ('address', 'mtu', 'master', 'ifalias', 'linkinfo')}
            selected[name]['linkinfo'] = stable_link_info(selected[name]['linkinfo'])
            selected[name]['up'] = 'UP' in link['flags']
            # VF namespace moves can assign another ifindex on return.
            if not name.startswith('msnicv'):
                selected[name]['ifindex'] = link['ifindex']
    vlans = [row for row in read_json('bridge', '-j', 'vlan', 'show')
             if row['ifname'].startswith('msnic') or row['ifname'] == 'swbr0']
    return {'links': selected, 'vlans': sorted(vlans, key=lambda row: row['ifname']),
            'bridges': ovs_table('Bridge', '_uuid,name,ports,external_ids,other_config,datapath_type'),
            'ports': ovs_table('Port', '_uuid,name,interfaces,tag,trunks,vlan_mode,external_ids'),
            'interfaces': ovs_table('Interface', '_uuid,name,type,mtu_request,options,external_ids')}


def check_active():
    links = {row['ifname']: row for row in read_json('ip', '-j', '-d', 'link', 'show')}
    assert all('msnicv' + str(i) not in links for i in range(4)), 'VF endpoint remained on host'
    vlans = {row['ifname']: row['vlans'] for row in read_json('bridge', '-j', 'vlan', 'show')}
    for i in range(4):
        rep = links['msnicr' + str(i)]
        assert 'UP' in rep['flags'] and rep.get('ifalias', '').startswith('dra-mn:'), rep
        if i < 2:
            assert rep['master'] == 'swbr0', rep
            allowed = {row['vlan']: row.get('flags', []) for row in vlans[rep['ifname']]}
            assert set(allowed) == {100, 200, 300}, allowed
            assert set(allowed[100]) == {'PVID', 'Egress Untagged'}, allowed
            assert not allowed[200] and not allowed[300], allowed
        else:
            assert command('ovs-vsctl', 'port-to-br', rep['ifname']) == 'ovsbr0'
            assert command('ovs-vsctl', 'get', 'Port', rep['ifname'], 'tag') == '100'
            assert read_json('ovs-vsctl', 'get', 'Port', rep['ifname'], 'trunks') == [200, 300]
            assert command('ovs-vsctl', 'get', 'Port', rep['ifname'], 'vlan_mode') == 'native-untagged'
            assert int(command('ovs-vsctl', 'get', 'Interface', rep['ifname'], 'ofport')) > 0
    print('PASS: endpoints moved; typed host representors and exact bridge/OVS VLAN configuration')


# Protect the explicitly excluded native management PF and its original VFs.
assert Path('/sys/bus/pci/devices/0000:15:00.0/driver').resolve().name == 'igb'
assert Path('/sys/bus/pci/devices/0000:15:00.0/sriov_numvfs').read_text().strip() == '5'
for pci in ('15:10.0', '15:10.2', '15:10.4', '15:10.6', '15:11.0'):
    assert Path('/sys/bus/pci/devices/0000:' + pci + '/driver').resolve().name == 'igbvf'
assert any(row.get('gateway') == '192.168.130.1' and row['dev'] == 'eth1'
           for row in read_json('ip', '-4', '-j', 'route', 'show', 'default'))
assert {'192.168.130.65', '192.168.130.250'} <= {
    addr['local'] for row in read_json('ip', '-4', '-j', 'address', 'show', 'eth1')
    for addr in row['addr_info']}

if sys.argv[1:] == ['snapshot']:
    print(json.dumps(snapshot(), sort_keys=True, indent=2))
elif sys.argv[1:] == ['active']:
    check_active()
elif len(sys.argv) == 3 and sys.argv[1] == 'restored':
    before = json.loads(Path(sys.argv[2]).read_text())
    after = snapshot()
    for link in before['links'].values():
        link['linkinfo'] = stable_link_info(link['linkinfo'])
    assert after == before, json.dumps({'before': before, 'after': after}, indent=2)
    print('PASS: exact saved host port/VLAN/OVS rows restored; native management PF unchanged')
else:
    raise SystemExit('usage: check-host-state.py snapshot|active|restored BEFORE.json')

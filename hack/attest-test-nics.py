#!/usr/bin/env python3
"""Verify the dedicated emulated NIC identities before guest driver changes."""
import subprocess
import sys
import xml.etree.ElementTree as ET


def attest(xml, network_xml, network, native_mac, mock_mac):
    vm = ET.fromstring(xml)
    net = ET.fromstring(network_xml)
    if vm.findall('./devices/hostdev') or vm.findall("./devices/interface[@type='hostdev']"):
        raise ValueError('Test VMs must not have PCI passthrough')
    bridge = net.find('bridge')
    macs = []
    for nic in vm.findall('./devices/interface'):
        model, source, mac = nic.find('model'), nic.find('source'), nic.find('mac')
        if model is not None and model.get('type') == 'igb' and source is not None:
            if source.get('network') == network or (bridge is not None and source.get('bridge') == bridge.get('name')):
                macs.append(mac.get('address') if mac is not None else None)
    expected = [native_mac, mock_mac] if mock_mac else [native_mac]
    if len(set(expected)) != len(expected) or sorted(macs) != sorted(expected):
        raise ValueError('Need exactly the configured native and mock test NICs')


if __name__ == '__main__':
    vm, network, native_mac, mock_mac = sys.argv[1:]
    def virsh(*args):
        return subprocess.check_output(['virsh', '--connect', 'qemu:///system', *args])
    attest(virsh('dumpxml', vm), virsh('net-dumpxml', network), network, native_mac, mock_mac)

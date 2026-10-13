import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('render', Path(__file__).with_name('render.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class RenderTest(unittest.TestCase):
    def test_separates_pci_families_and_node_bridge_grants(self):
        hardware = dict(node='worker-1', suffix='1', native='0000:29:00.0', mock='0000:3d:00.0',
                        vfs=['0000:3d:10.4', '0000:3d:10.6'], bridgeUUID='aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee')
        native, bond, eswitch, ovs_policy, ovs = module.render(hardware)
        for policy in [native, eswitch, ovs_policy]:
            self.assertEqual(policy['spec']['nodeSelector']['matchLabels'], {'kubernetes.io/hostname': 'worker-1'})
        self.assertEqual(native['spec']['selector']['parentPCIAddresses'], [hardware['native']])
        self.assertEqual(eswitch['spec']['selector']['pciAddresses'], [hardware['mock']])
        self.assertEqual(ovs_policy['spec']['selector']['pciAddresses'], hardware['vfs'])
        self.assertEqual(ovs_policy['spec']['exposure']['allowedOVSBridges'][0]['uuid'], hardware['bridgeUUID'])
        self.assertIn('bond-vlan-1', bond['spec']['steps'][0]['selectors'][1])
        self.assertIn('switchdev-ovs-1', ovs['spec']['steps'][0]['selectors'][1])
        hardware['mock'] = hardware['native']
        with self.assertRaises(ValueError):
            module.render(hardware)

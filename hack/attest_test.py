import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('attest', Path(__file__).with_name('attest-test-nics.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class AttestationTest(unittest.TestCase):
    def test_requires_exact_test_nics(self):
        def xml(macs):
            return '<domain><devices>' + ''.join(f'<interface><model type="igb"/><source network="test"/><mac address="{mac}"/></interface>' for mac in macs) + '</devices></domain>'
        network = '<network><bridge name="testbr"/></network>'
        module.attest(xml(['native', 'mock']), network, 'test', 'native', 'mock')
        for macs in [['management', 'mock'], ['native'], ['native', 'mock', 'foreign']]:
            with self.assertRaises(ValueError):
                module.attest(xml(macs), network, 'test', 'native', 'mock')
        with self.assertRaises(ValueError):
            module.attest(xml(['native', 'mock']).replace('<devices>', '<devices><hostdev/>'), network, 'test', 'native', 'mock')
        with self.assertRaises(ValueError):
            module.attest(xml(['native', 'native']), network, 'test', 'native', 'native')

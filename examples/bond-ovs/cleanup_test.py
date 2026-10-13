import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('cleanup', Path(__file__).with_name('cleanup.py'))
cleanup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cleanup)


class ModeCleanupTest(unittest.TestCase):
    def get(self, kind, name=None, namespace=None):
        if kind == 'deviceexposurepolicies':
            return {'items': [{'metadata': {'name': 'eswitch-0'}, 'spec': {'nodeSelector': {'matchLabels': {'kubernetes.io/hostname': 'multi-network-worker-0.multi-network.lab'}}, 'selector': {'pciAddresses': ['0000:2a:00.0']}}}]}
        if kind == 'node':
            return {'spec': {}}
        return None

    def test_cordon_and_quiesce_before_mode_restore_then_uncordon(self):
        calls = []
        with patch.object(cleanup.manage, 'get', side_effect=self.get), patch.object(cleanup.manage, 'run', side_effect=lambda *args: calls.append(args)), patch.object(cleanup, 'guest', side_effect=lambda *args: calls.append(('guest', *args))), patch.object(cleanup.subprocess, 'check_output', return_value='{"items":[]}'):
            cleanup.cleanup()
        cordon = next(i for i, c in enumerate(calls) if 'cordon' in c)
        restore = next(i for i, c in enumerate(calls) if 'eswitch-0' in c)
        uncordon = next(i for i, c in enumerate(calls) if 'uncordon' in c)
        self.assertEqual(calls[cordon + 1][0], 'guest')
        self.assertIn('"$p/net")" down', calls[cordon + 1][2])
        self.assertLess(cordon, restore)
        self.assertLess(restore, uncordon)
        self.assertIn('0000:2a:00.0', calls[cordon + 1][2])

    def test_other_driver_claim_blocks_host_mutation(self):
        claims = {'items': [{'status': {'allocation': {'driver': 'dra-multi-network.schseba.github.io'}}}]}
        with patch.object(cleanup.manage, 'get', side_effect=self.get), patch.object(cleanup.manage, 'run') as run, patch.object(cleanup, 'guest') as guest, patch.object(cleanup.subprocess, 'check_output', return_value=json.dumps(claims)):
            with self.assertRaisesRegex(ValueError, 'Drain remaining'):
                cleanup.cleanup()
            guest.assert_not_called()
            self.assertFalse(any('cordon' in c.args for c in run.call_args_list))

    def test_preserves_an_administrators_cordon(self):
        def get(kind, name=None, namespace=None):
            return {'spec': {'unschedulable': True}} if kind == 'node' else self.get(kind, name, namespace)
        with patch.object(cleanup.manage, 'get', side_effect=get), patch.object(cleanup.manage, 'run') as run, patch.object(cleanup, 'guest'), patch.object(cleanup.subprocess, 'check_output', return_value='{"items":[]}'):
            cleanup.cleanup()
        self.assertFalse(any('uncordon' in c.args for c in run.call_args_list))

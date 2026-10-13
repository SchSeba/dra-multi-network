import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('manage', Path(__file__).with_name('manage.py'))
manage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(manage)


class CleanupTest(unittest.TestCase):
    def setUp(self):
        self.pod = {'metadata': {'name': 'demo', 'uid': 'pod-uid'}, 'spec': {'resourceClaims': [{'resourceClaimTemplateName': 'template'}]}}
        self.template = {'metadata': {'name': 'template'}, 'spec': {'spec': {'devices': {'requests': [{'exactly': {'deviceClassName': 'owned-class'}}]}}}}

    def get(self, kind, name=None, namespace=None):
        return {'pod': self.pod, 'resourceclaimtemplate': self.template,
                'resourceclaimtemplates': {'items': [self.template]},
                'resourceclaims': {'items': [{'metadata': {'name': 'claim', 'ownerReferences': [{'uid': 'pod-uid'}]}}, {'metadata': {'name': 'foreign-claim', 'ownerReferences': [{'uid': 'other-uid'}]}}]},
                'pods': {'items': []}}[kind]

    def test_claims_drain_before_templates_and_foreign_claims_survive(self):
        with patch.object(manage, 'get', side_effect=self.get), patch.object(manage, 'run') as run:
            manage.remove_pods('example', ['demo'], {'owned-class'})
        self.assertEqual(run.call_args_list[0].args[3:5], ('delete', 'pod'))
        self.assertIn('resourceclaim/claim', run.call_args_list[1].args)
        self.assertEqual(run.call_args_list[2].args[3:5], ('delete', 'resourceclaimtemplate'))
        self.assertNotIn('foreign-claim', str(run.call_args_list))

    def test_foreign_pod_is_rejected_before_any_deletion(self):
        with patch.object(manage, 'get', side_effect=self.get), patch.object(manage, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'another example'):
                manage.remove_pods('example', ['demo'], {'other-class'})
            run.assert_not_called()

    def test_template_used_by_another_pod_is_preserved(self):
        def get(kind, name=None, namespace=None):
            if kind == 'pods':
                return {'items': [self.pod]}
            return self.get(kind, name, namespace)
        with patch.object(manage, 'get', side_effect=get), patch.object(manage, 'run') as run:
            manage.remove_pods('example', ['demo'], {'owned-class'})
        self.assertEqual(run.call_count, 2)

    def test_existing_workload_examples_are_all_listed(self):
        readmes = {p.parent.relative_to(manage.ROOT / 'examples').as_posix() for p in (manage.ROOT / 'examples').rglob('README.md')}
        self.assertEqual({entry[0] for entry in manage.EXAMPLES.values()}, readmes - {'.', 'switchdev', 'bond-ovs'})

    def test_shared_ipam_pool_survives_other_topology_users(self):
        for referenced in [True, False]:
            with self.subTest(referenced=referenced):
                def get(kind, name=None, namespace=None):
                    if kind == 'networktopologies':
                        return {'items': [{'spec': {'steps': [{'config': {'network': 'blue'}}]}}] if referenced else []}
                    return None
                with patch.object(manage, 'get', side_effect=get), patch.object(manage, 'run') as run:
                    manage.cleanup('ipam')
                self.assertEqual(any('examples/ipam/pool.yaml' in c.args for c in run.call_args_list), not referenced)

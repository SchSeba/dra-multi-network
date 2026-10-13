#!/usr/bin/env python3
"""Apply an example or remove its consumers before administrator resources."""
import json
import os
from pathlib import Path
import subprocess
import sys
import yaml

ROOT = Path(__file__).resolve().parent.parent
# Borrowed policies and the shared IPAM pool are handled separately at cleanup.
EXAMPLES = {
    'single-vf': ('single-vf', ['single-vf'], ['policy.yaml', 'topology.yaml']),
    'bond-vlan': ('bond-vlan', ['network-mvp', 'network-mvp-peer'], ['policy.yaml', 'topology.yaml']),
    'effect-program': ('effect-program', ['network-mvp', 'network-mvp-peer'], ['plugin.yaml', 'topology.yaml']),
    'link-config': ('link-config', ['labeled-vlan-mvp'], ['plugin.yaml', 'topology.yaml']),
    'kernel-rebind': ('kernel-rebind', ['kernel-rebind-mvp'], ['policy.yaml', 'topology.yaml']),
    'reset': ('reset', ['kernel-reset-mvp'], ['policy.yaml', 'topology.yaml']),
    'shared': ('shared', ['macvlan', 'ipvlan'], ['policies.yaml', 'vf-policies.yaml', 'topologies.yaml', 'vf-topologies.yaml']),
    'root-effect-program': ('root-effect-program', ['network-mvp', 'network-mvp-peer'], ['plugin.yaml', 'policy.yaml', 'topology.yaml']),
    'vf-config': ('vf-config', ['vf-config-mvp'], ['policy.yaml', 'topology.yaml']),
    'vfio': ('vfio', ['vfio-mvp'], ['policy.yaml', 'topology.yaml']),
    'ipam': ('ipam', ['ipam-demo-1'], ['policy.yaml', 'topology.yaml']),
    'ipam-two-vf': ('ipam/two-vf', ['two-vf-ipam'], ['policy.yaml', 'topology.yaml']),
    'roce': ('roce', ['roce-mvp'], ['policy.yaml', 'topology.yaml']),
    'ipoib': ('ipoib', ['ipoib-mvp'], ['policy.yaml', 'topology.yaml']),
    'switchdev-ovs': ('switchdev/ovs', ['switchdev-ovs-a', 'switchdev-ovs-b'], ['policy.yaml', 'topology.yaml']),
    'switchdev-accelerated-bridge': ('switchdev/accelerated-bridge', ['switchdev-bridge-a', 'switchdev-bridge-b'], ['policy.yaml', 'topology.yaml']),
}


def run(*args):
    subprocess.run(args, cwd=ROOT, check=True)


def get(kind, name=None, namespace=None):
    args = ['kubectl']
    if namespace:
        args += ['-n', namespace]
    args += ['get', kind]
    if name:
        args += [name, '--ignore-not-found']
    args += ['-o', 'json']
    text = subprocess.check_output(args, text=True, cwd=ROOT)
    return json.loads(text) if text.strip() else None


def classes(template):
    return {r['exactly']['deviceClassName'] for r in template['spec']['spec']['devices']['requests'] if 'exactly' in r}


def remove_pods(namespace, pods, allowed_classes):
    templates = {t["metadata"]["name"] for t in get("resourceclaimtemplates", namespace=namespace)["items"] if classes(t) and classes(t).issubset(allowed_classes)}
    # Check all identities before deleting anything, including shared Pod names.
    consumers = []
    for name in pods:
        pod = get('pod', name, namespace)
        if pod is None:
            continue
        refs = [r['resourceClaimTemplateName'] for r in pod['spec'].get('resourceClaims', []) if 'resourceClaimTemplateName' in r]
        if not refs:
            raise ValueError(f'{namespace}/{name} has no example claim template; refusing deletion')
        for ref in refs:
            template = get('resourceclaimtemplate', ref, namespace)
            if template is None or not classes(template) or not classes(template).issubset(allowed_classes):
                raise ValueError(f'{namespace}/{name} belongs to another example; refusing deletion')
        templates.update(refs)
        consumers.append(pod)
    if consumers:
        claims = get('resourceclaims', namespace=namespace)['items']
        for pod in consumers:
            uid = pod['metadata']['uid']
            owned = [c['metadata']['name'] for c in claims if any(r['uid'] == uid for r in c['metadata'].get('ownerReferences', []))]
            run('kubectl', '-n', namespace, 'delete', 'pod', pod['metadata']['name'], '--wait=true', '--timeout=180s')
            for claim in owned:
                run('kubectl', '-n', namespace, 'wait', '--for=delete', f'resourceclaim/{claim}', '--timeout=180s')
    remaining = get('pods', namespace=namespace)['items']
    in_use = {r['resourceClaimTemplateName'] for p in remaining for r in p['spec'].get('resourceClaims', []) if 'resourceClaimTemplateName' in r}
    for template in sorted(templates - in_use):
        run('kubectl', '-n', namespace, 'delete', 'resourceclaimtemplate', template, '--ignore-not-found')


def objects(folder, manifests):
    return [obj for name in manifests for obj in yaml.safe_load_all((ROOT / 'examples' / folder / name).read_text()) if obj]


def cleanup(name):
    folder, pods, manifests = EXAMPLES[name]
    namespace = {'ipam': 'ipam-example', 'ipam-two-vf': 'ipam-example', 'roce': 'multi-network-roce-example', 'ipoib': 'multi-network-ipoib-example'}.get(name, 'multi-network-example')
    if name in ['single-vf', 'effect-program', 'link-config', 'kernel-rebind', 'reset', 'shared', 'switchdev-ovs', 'switchdev-accelerated-bridge']:
        namespace = os.environ.get('DRA_EXAMPLE_NAMESPACE', namespace)
    docs = objects(folder, manifests)
    allowed = set()
    for obj in docs:
        if obj['kind'] == 'NetworkTopology':
            topology = get('networktopology', obj['metadata']['name'])
            if topology:
                allowed.update(r['className'] for r in topology.get('status', {}).get('rootClasses', []))
    # A missing namespace means no consumers, without masking API/auth failures.
    if get('namespace', namespace):
        remove_pods(namespace, pods, allowed)
    for kind in ['NetworkTopology', 'DeviceExposurePolicy', 'NetworkPlugin']:
        for obj in docs:
            if obj['kind'] == kind:
                run('kubectl', 'delete', kind, obj['metadata']['name'], '--ignore-not-found', '--wait=true', '--timeout=180s')
    if name in ['ipam', 'ipam-two-vf']:
        pool = yaml.safe_load((ROOT / 'examples/ipam/pool.yaml').read_text())
        network = json.loads(pool['data']['config'])['network']
        topologies = get('networktopologies')['items']
        if not any(step.get('config', {}).get('network') == network for topology in topologies for step in topology['spec']['steps']):
            run('kubectl', 'delete', '-f', 'examples/ipam/pool.yaml', '--ignore-not-found', '--wait=true', '--timeout=180s')


def deploy(name):
    folder, _, manifests = EXAMPLES[name]
    script = ROOT / 'examples' / folder / 'apply.sh'
    if script.exists():
        run('bash', str(script))
        return
    args = ['kubectl', 'apply']
    if name in ['effect-program', 'link-config']:
        args += ['-f', 'examples/bond-vlan/policy.yaml']
    for manifest in manifests:
        args += ['-f', f'examples/{folder}/{manifest}']
    run(*args)
    if name == 'effect-program':
        subprocess.run(['bash', 'test/integration/bond-vlan/test-traffic.sh'], cwd=ROOT, env={**os.environ, 'DRA_EXAMPLE_TOPOLOGY': 'dual-vf-bond-program-vlan'}, check=True)
    else:
        topology = {'link-config': 'labeled-vlan', 'kernel-rebind': 'kernel-rebind', 'reset': 'kernel-reset'}[name]
        run('bash', 'examples/connect.sh', topology, EXAMPLES[name][1][0])


if __name__ == '__main__':
    cluster = os.environ.get('CLUSTER_NAME', 'multi-network')
    os.environ.setdefault('KUBECONFIG', str(Path.home() / f'.kcli/clusters/{cluster}/auth/kubeconfig'))
    action, name = sys.argv[1:]
    if action not in ['deploy', 'delete'] or name not in EXAMPLES:
        raise SystemExit('usage: manage.py deploy|delete EXAMPLE')
    (deploy if action == 'deploy' else cleanup)(name)

#!/usr/bin/env python3
"""Allow K3s node traffic to reach the PVMBG test controls."""
import ipaddress
import json
import subprocess

KUBE = ['kubectl', '--context', 'aat-dev']
NAMESPACE = 'sistem-informasi-kebencanaan-bnpb'


def main():
    nodes = json.loads(subprocess.check_output(KUBE + ['get', 'nodes', '-o', 'json']))['items']
    addresses = set()
    for node in nodes:
        for address in node['status'].get('addresses', []):
            if address['type'] == 'InternalIP':
                addresses.add(ipaddress.ip_address(address['address']))
        spec = node['spec']
        for cidr in spec.get('podCIDRs') or ([spec['podCIDR']] if 'podCIDR' in spec else []):
            # K3s with Flannel VXLAN can SNAT a cross-node ClusterIP request
            # from hostNetwork to the node's Flannel endpoint address.
            addresses.add(ipaddress.ip_network(cidr).network_address)
    if not addresses:
        raise SystemExit('No cluster addresses found for Tester Agent access.')
    peers = [{'ipBlock': {'cidr': f'{ip}/{ip.max_prefixlen}'}} for ip in sorted(
        addresses, key=lambda ip: (ip.version, int(ip)))]
    policy = {
        'apiVersion': 'networking.k8s.io/v1',
        'kind': 'NetworkPolicy',
        'metadata': {'name': 'tester-mock-host-access', 'namespace': NAMESPACE},
        'spec': {
            'podSelector': {'matchLabels': {'app': 'pvmbg-mock'}},
            'policyTypes': ['Ingress'],
            'ingress': [{'from': peers, 'ports': [{'protocol': 'TCP', 'port': 8088}]}],
        },
    }
    subprocess.run(KUBE + ['apply', '-f', '-'], input=json.dumps(policy), text=True, check=True)


if __name__ == '__main__':
    main()

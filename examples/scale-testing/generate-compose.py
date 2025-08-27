#!/usr/bin/env python3
"""Generate a docker-compose.yml file with explicit service definitions for scale testing."""

import sys
import yaml

def generate_compose(num_coordinators=5, nodes_per_coordinator=10):
    """Generate docker-compose configuration."""
    
    config = {
        'version': '3.8',
        'services': {},
        'networks': {
            'testnet': {
                'driver': 'bridge',
                'ipam': {
                    'config': [{
                        'subnet': '10.150.0.0/16'
                    }]
                }
            },
            'bridge': {
                'driver': 'bridge'
            }
        }
    }
    
    # Generate coordinator services
    for i in range(1, num_coordinators + 1):
        # Build bootstrap peers list
        peers = []
        if i > 1:
            # Connect to up to 3 previous coordinators
            start = max(1, i - 3)
            for j in range(start, i):
                peers.append(f"member-{j}:coordinator-{j}:8001")
        
        peers_args = ""
        if peers:
            # StringSlice flag takes comma-separated values
            peers_args = f"--bootstrap-peers {','.join(peers)}"
        
        config['services'][f'coordinator-{i}'] = {
            'build': '..',
            'image': 'collective:latest',
            'container_name': f'scale-coordinator-{i}',
            'networks': ['testnet'],
            'command': f'coordinator --member-id member-{i} --address :8001 --data-dir /data {peers_args}'.strip(),
            'volumes': ['/data'],
            'deploy': {
                'resources': {
                    'limits': {
                        'memory': '256M',
                        'cpus': '0.5'
                    }
                }
            }
        }
    
    # Generate node services
    total_nodes = num_coordinators * nodes_per_coordinator
    for i in range(1, total_nodes + 1):
        coord_num = ((i - 1) // nodes_per_coordinator) + 1
        node_num = ((i - 1) % nodes_per_coordinator) + 1
        
        config['services'][f'node-{i}'] = {
            'build': '..',
            'image': 'collective:latest',
            'container_name': f'scale-node-{i}',
            'networks': ['testnet'],
            'command': f'node --member-id member-{coord_num} --node-id member-{coord_num}-node-{node_num} --address :7001 --coordinator coordinator-{coord_num}:8001 --capacity 52428800 --data-dir /data',
            'depends_on': [f'coordinator-{coord_num}'],
            'volumes': ['/data'],
            'deploy': {
                'resources': {
                    'limits': {
                        'memory': '128M',
                        'cpus': '0.2'
                    }
                }
            }
        }
    
    # Add external coordinator for status checking
    # Connect to all coordinators in the network
    all_peers = []
    for i in range(1, num_coordinators + 1):
        all_peers.append(f"member-{i}:coordinator-{i}:8001")
    
    config['services']['coordinator-external'] = {
        'build': '..',
        'image': 'collective:latest',
        'container_name': 'scale-coordinator-external',
        'networks': ['testnet', 'bridge'],
        'command': f'coordinator --member-id member-external --address :8001 --data-dir /data --bootstrap-peers {",".join(all_peers)}',
        'ports': ['8001:8001'],
        'volumes': ['/data'],
        'deploy': {
            'resources': {
                'limits': {
                    'memory': '256M',
                    'cpus': '0.5'
                }
            }
        }
    }
    
    return config

def main():
    if len(sys.argv) > 1:
        num_coordinators = int(sys.argv[1])
    else:
        num_coordinators = 5
    
    if len(sys.argv) > 2:
        nodes_per_coordinator = int(sys.argv[2])
    else:
        nodes_per_coordinator = 10
    
    config = generate_compose(num_coordinators, nodes_per_coordinator)
    
    with open('docker-compose-generated.yml', 'w') as f:
        yaml.dump(config, f, default_flow_style=False, sort_keys=False)
    
    print(f"Generated docker-compose-generated.yml with {num_coordinators} coordinators and {num_coordinators * nodes_per_coordinator} nodes")
    print(f"Total simulated storage: {num_coordinators * nodes_per_coordinator * 50}MB")
    print("")
    print("Run with: docker-compose -f docker-compose-generated.yml up -d")

if __name__ == '__main__':
    main()
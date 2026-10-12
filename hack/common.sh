#!/usr/bin/env bash

cluster_version=${CLUSTER_VERSION:-1.37.0}
cluster_name=${CLUSTER_NAME:-multi-network}
domain_name=$cluster_name.lab
network_name=${NETWORK_NAME:-multi-net}
sriov_network_name=${SRIOV_NETWORK_NAME:-multi-sriov}
network_cidr=${NETWORK_CIDR:-192.168.130.0/24}
sriov_network_cidr=${SRIOV_NETWORK_CIDR:-192.168.210.0/24}
pod_cidr=${POD_CIDR:-10.246.0.0/16}
service_cidr=${SERVICE_CIDR:-10.112.0.0/16}

api_ip=${API_IP:-192.168.130.250}
virtual_router_id=${VIRTUAL_ROUTER_ID:-210}

export NAMESPACE="dra-multi-network"
export KUBECONFIG=${KUBECONFIG:-$HOME/.kcli/clusters/$cluster_name/auth/kubeconfig}


here="$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")"
root="$(readlink --canonicalize "$here/..")"

#!/bin/bash

set -e
set -x

export CTX_CLUSTER1=kind-kne; export CTX_CLUSTER2=kind-bookinfo

if [[ -z "$LICENSE" ]]; then
    echo "Set LICENSE to name of license file (should be in current directory)"
    exit 1
fi

set +e # ok if not exists
kind delete cluster --name=kne
kind delete cluster --name=bookinfo
set -e

# KNE cluster
# Creates cluster with name kne
kne deploy ../deploy/kne/kind-bridge-srlinux-only.yaml

set +e # ok if exists
kubectl create namespace srlinux-controller
set -e
kubectl create -n srlinux-controller \
    secret generic srlinux-licenses --from-file=all.key=$LICENSE \
    --dry-run=client --save-config -o yaml | \
    kubectl apply -f -

kind load docker-image ghcr.io/nokia/srlinux:23.3.1 --name kne

# Bookinfo cluster
./cluster2.sh

# XXX Skipped for now since just trying to expose clusters
#kind load docker-image hub/pilot:tag --name bookinfo
#kind load docker-image hub/proxyv2:tag --name bookinfo
# XXX set context, update proxyv2 image to account for eastwest-gateway
# XXX point this to correct version of istioctl if have multiple
#istioctl install --set profile=demo --set hub=hub --set tag=tag -y --set 'meshConfig.defaultConfig.proxyStatsMatcher.inclusionRegexps[0]=.*alpn.*'
#kubectl label namespace default istio-injection=enabled
#kubectl apply -f ../../proxy/src/envoy/http/alpn/alpn.yaml

# TODO if this works: XXXs, update comments, readme

# Expose clusters to each other
./multicluster.sh

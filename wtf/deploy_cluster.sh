#!/bin/bash

# Deploy kind cluster with KNE and Istio installed

set -e

if [[ -z "$LICENSE" ]]; then
    echo "Set LICENSE to name of license file (should be in current directory)"
    exit 1
fi

set +e # ok if not exists
kind delete cluster --name=kne
set -e

# KNE stuff
kne deploy ../deploy/kne/kind-bridge-srlinux-only.yaml

set +e # ok if exists
kubectl create namespace srlinux-controller
set -e
kubectl create -n srlinux-controller \
    secret generic srlinux-licenses --from-file=all.key=$LICENSE \
    --dry-run=client --save-config -o yaml | \
    kubectl apply -f -

kind load docker-image ghcr.io/nokia/srlinux:23.3.1 --name kne

# Istio stuff
kind load docker-image hub/pilot:tag --name kne
kind load docker-image hub/proxyv2:tag --name kne
istioctl install --set profile=demo --set hub=hub --set tag=tag -y --set 'meshConfig.defaultConfig.proxyStatsMatcher.inclusionRegexps[0]=.*alpn.*'
kubectl label namespace default istio-injection=enabled
kubectl apply -f ../../proxy/src/envoy/http/alpn/alpn.yaml

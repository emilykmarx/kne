#!/bin/bash
set -xe

kind delete cluster --name=kne
kne deploy deploy/kne/kind-bridge-srlinux-only.yaml

set +e # ok if exists
kubectl create namespace srlinux-controller
set -e
kubectl create -n srlinux-controller \
    secret generic srlinux-licenses --from-file=all.key=licenses_do_not_push.key \
    --dry-run=client --save-config -o yaml | \
    kubectl apply -f -

kind load docker-image ghcr.io/nokia/srlinux:23.3.1 --name kne

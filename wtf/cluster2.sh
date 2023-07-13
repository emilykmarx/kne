#!/bin/bash
set -e
set -x

export CTX_CLUSTER1=kind-kne; export CTX_CLUSTER2=kind-bookinfo

# Create cluster 2
set +e # ok if not exists
kind delete cluster --name=bookinfo
set -e
kind create cluster --config=kind-bookinfo.yaml

## MetalLB
kubectl --context="${CTX_CLUSTER2}" get configmap kube-proxy -n kube-system -o yaml | \
  sed -e "s/strictARP: false/strictARP: true/" | \
  kubectl --context="${CTX_CLUSTER2}" apply -f - -n kube-system

# Same version as kne
kubectl --context="${CTX_CLUSTER2}" apply -f https://raw.githubusercontent.com/metallb/metallb/v0.13.5/config/manifests/metallb-native.yaml

# Avoid `error: no matching resources found` on wait (not sure if long enough)
sleep 15
kubectl --context="${CTX_CLUSTER2}" wait --namespace metallb-system \
  --for=condition=ready pod \
  --selector=app=metallb \
  --timeout=90s

# Would need to automate this
docker network inspect -f '{{.IPAM.Config}}' kind

# This overlaps (or is the same as) the kne cluster's metalLB range -- ok?
cat <<EOF > metallb_cluster2.yaml
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: example
  namespace: metallb-system
spec:
  addresses:
  - 172.18.0.0/16
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: empty
  namespace: metallb-system
EOF

kubectl --context="${CTX_CLUSTER2}" apply -f metallb_cluster2.yaml

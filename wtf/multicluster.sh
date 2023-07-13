#!/bin/bash
set -e
set -x

ISTIO_VERSION=1.18.0
pushd ../../istio-$ISTIO_VERSION

export CTX_CLUSTER1=kind-kne; export CTX_CLUSTER2=kind-bookinfo

# Istio multicluster setup
set +e
kubectl --context="${CTX_CLUSTER1}" create namespace istio-system
set -e

kubectl --context="${CTX_CLUSTER1}" get namespace istio-system && \
  kubectl --context="${CTX_CLUSTER1}" label namespace istio-system topology.istio.io/network=network1

cat <<EOF > cluster1.yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  values:
    global:
      meshID: mesh1
      multiCluster:
        clusterName: cluster1
      network: network1
EOF

istioctl install -y --set values.pilot.env.EXTERNAL_ISTIOD=true --context="${CTX_CLUSTER1}" -f cluster1.yaml

samples/multicluster/gen-eastwest-gateway.sh \
    --mesh mesh1 --cluster cluster1 --network network1 | \
    istioctl --context="${CTX_CLUSTER1}" install -y -f -

# Should automate to wait for external IP, and check where else to wait
kubectl --context="${CTX_CLUSTER1}" get svc istio-eastwestgateway -n istio-system

kubectl apply --context="${CTX_CLUSTER1}" -n istio-system -f \
    samples/multicluster/expose-istiod.yaml

kubectl --context="${CTX_CLUSTER1}" apply -n istio-system -f \
    samples/multicluster/expose-services.yaml

## Cluster 2
set +e
kubectl --context="${CTX_CLUSTER2}" create namespace istio-system
set -e
kubectl --context="${CTX_CLUSTER2}" annotate namespace istio-system topology.istio.io/controlPlaneClusters=cluster1

kubectl --context="${CTX_CLUSTER2}" label namespace istio-system topology.istio.io/network=network2

export DISCOVERY_ADDRESS=$(kubectl \
    --context="${CTX_CLUSTER1}" \
    -n istio-system get svc istio-eastwestgateway \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}')

cat <<EOF > cluster2.yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  profile: remote
  values:
    istiodRemote:
      injectionPath: /inject/cluster/cluster2/net/network2
    global:
      remotePilotAddress: ${DISCOVERY_ADDRESS}
EOF

istioctl install -y --context="${CTX_CLUSTER2}" -f cluster2.yaml

istioctl x create-remote-secret \
    --context="${CTX_CLUSTER2}" \
    --name=cluster2 | \
    kubectl apply -f - --context="${CTX_CLUSTER1}"

samples/multicluster/gen-eastwest-gateway.sh \
    --mesh mesh1 --cluster cluster2 --network network2 | \
    istioctl --context="${CTX_CLUSTER2}" install -y -f -

# Should automate to wait for external IP
kubectl --context="${CTX_CLUSTER2}" get svc istio-eastwestgateway -n istio-system

# This doesn't work, but verification step in the doc still passes.
# Because cluster2 uses remote profile, which doesn't install Gateway crd (kubectl get crd)
# If becomes a problem, could try removing profile:remote from cluster2.yaml? (looking at history of remote.yaml, at one point it was "equivalent to default")
kubectl --context="${CTX_CLUSTER2}" apply -n istio-system -f \
    samples/multicluster/expose-services.yaml

popd

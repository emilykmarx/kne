#!/bin/bash

# Assumes kind cluster already deployed with KNE and Istio installed
# Dependencies: networkx and matplotlib python package
# Clean up any existing routers, bookinfo app, and Istio ingress in the cluster
# Create KNE topology and deploy it
# Deploy bookinfo app
# Run WTF tests if specified

set -e

pushd ../..
export WTF_KNE_WORKDIR=wtf
export WTF_TOPOGRAPH=topo.json
export WTF_TOPOFILE=wtf_topo.pbtxt
export WTF_TESTBEDFILE=wtf_testbed.textproto
export WTF_TESTOUTFILE=test_out.json
ISTIODIR=istio-1.15.0
pushd kne/$WTF_KNE_WORKDIR
mkdir -p out
mkdir -p egress/out

# Clean up existing cluster (if any)
set +e # ok if not exists
# Seems ok to leave istio installed when recreating other stuff
NAMESPACE=default ../../$ISTIODIR/samples/bookinfo/platform/kube/cleanup.sh
# Clear any existing logs in ingress
kubectl -n=istio-system delete pod $(kubectl get pods -A --no-headers -o custom-columns=":metadata.name" | grep ingress)
kne delete out/$WTF_TOPOFILE
sleep 5 # wait for topo to be fully deleted before creating new one, and for ingress to be deleted before creating gateway
set -e

# Create KNE topology
# -n 32 -p 0.05 makes a reasonable topo with long paths
pushd out
../gen_topo_graph.py -n 4 -p 0.01 -o $WTF_TOPOGRAPH # gen random topo and paths
popd
# Go must be run from kne root
pushd ..
go run ./$WTF_KNE_WORKDIR -loop # gen topo file, testbed file, configs
popd
# Need to build egress image after go run to copy in iface setup script
docker build -t egress ./egress
kind load docker-image egress:latest --name kne

kne create out/$WTF_TOPOFILE

# Create microservice app
kubectl apply -f ../../$ISTIODIR/samples/bookinfo/platform/kube/bookinfo.yaml
kubectl scale deployment productpage-v1 --replicas=24
kubectl scale deployment details-v1 --replicas=24
kubectl scale deployment ratings-v1 --replicas=5
kubectl scale deployment reviews-v1 --replicas=5
kubectl scale deployment reviews-v2 --replicas=5
kubectl scale deployment reviews-v3 --replicas=5

sleep 50 # Wait for gateway to come up before getting its IP, and routers to load startup configs before running tests

# TODO change arbitrary sleeps to waiting for the right condition (these are not always long enough)
echo "If not all pods are running, pause until they are"
kubectl get pods -A
kubectl apply -f ../../$ISTIODIR/samples/bookinfo/networking/bookinfo-gateway.yaml

export INGRESS_HOST=$(kubectl -n istio-system get service istio-ingressgateway -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
export INGRESS_PORT=$(kubectl -n istio-system get service istio-ingressgateway -o jsonpath='{.spec.ports[?(@.name=="http2")].port}')
export SECURE_INGRESS_PORT=$(kubectl -n istio-system get service istio-ingressgateway -o jsonpath='{.spec.ports[?(@.name=="https")].port}')
export GATEWAY_URL=$INGRESS_HOST:$INGRESS_PORT
echo "Istio gateway URL (if this is not set, pause until it is): $GATEWAY_URL"
echo "Routers take time to load startup configs; confirm they're loaded before proceeding"

# Run go tests, plot result
if [ "$1" = "run-tests" ]; then
    # Note -config option is deprecated
    # *.go compiles stuff from non-test file into test file
    go test -v *.go -topology out/$WTF_TOPOFILE -testbed out/$WTF_TESTBEDFILE -vendor_creds=NOKIA/admin/NokiaSrl1! -skip_reset=true
    pushd out
    ../kne_plot_trace.py --input-file $WTF_TESTOUTFILE
    ../../../proxy/src/envoy/http/alpn/istio_plot_trace.py --input-file $WTF_TESTOUTFILE
    popd

fi

popd

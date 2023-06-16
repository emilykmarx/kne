#!/bin/bash
# Dependencies (tested on Ubuntu 20):
    # KNE: kne fork, ondatra repo (0.1.15), Nokia srlinux image, Nokia license file.
    # Istio: istio 1.15.0 installed, proxy fork
    # Plotting: networkx, matplotlib
    # Note the below makes some assumptions about directory structure
# Follow kne's setup guide, except clone my fork of kne (and of ondatra if applicable -- checkout desired commit of ondatra)
# Note cluster stays up after vm reboot
# For lots of pods (but < 110), to fix "too many open files" when starting proxy container:
    # sudo bash -c 'echo "fs.inotify.max_user_watches=1048576" >> /etc/sysctl.conf'
    # sudo bash -c 'echo "fs.inotify.max_user_instances=1024" >> /etc/sysctl.conf'
    # sudo sysctl -p /etc/sysctl.conf
# For > 110 pods:
    # See kne's multinode doc
set -xe

WTFDIR=go_expts
GENPATH=$WTFDIR/out
KNEPATH=~/projects/wtf_project/kne
OUTFILE=test_out.json
ISTIOPATH=~/go/src/istio.io/istio
# Go must be run from project root
pushd $KNEPATH
mkdir -p $GENPATH
mkdir -p $WTFDIR/egress/out

if [ "$1" = "deploy" ]; then
    # Deploy cluster
    set +e # ok if not exists
    kind delete cluster --name=kne
    set -e

    # KNE stuff
    kne deploy deploy/kne/kind-bridge-srlinux-only.yaml

    set +e # ok if exists
    kubectl create namespace srlinux-controller
    set -e
    kubectl create -n srlinux-controller \
        secret generic srlinux-licenses --from-file=all.key=licenses_do_not_push.key \
        --dry-run=client --save-config -o yaml | \
        kubectl apply -f -

    docker pull ghcr.io/nokia/srlinux:23.3.1
    kind load docker-image ghcr.io/nokia/srlinux:23.3.1 --name kne

    # Istio stuff
    pushd $ISTIOPATH
    docker pull istio/pilot:1.15.0 # didn't modify pilot, but kind expects pilot & proxyv2 to be in same registry
    docker tag istio/pilot:1.15.0 hub/pilot:tag
    kind load docker-image hub/pilot:tag --name kne

    docker pull emarx1/wtf-project-istio:proxyv2_9d02d7b2 # dictates version of WTF agent
    docker tag emarx1/wtf-project-istio:proxyv2_9d02d7b2 hub/proxyv2:tag
    kind load docker-image hub/proxyv2:tag --name kne
    istioctl install --set profile=demo --set hub=hub --set tag=tag -y --set 'meshConfig.defaultConfig.proxyStatsMatcher.inclusionRegexps[0]=.*alpn.*'
    kubectl label namespace default istio-injection=enabled
    kubectl apply -f ../proxy/src/envoy/http/alpn/alpn.yaml
    popd
fi

# Clean up existing cluster (if any)
set +e # ok if not exists
# Seems ok to leave istio installed when recreating other stuff
NAMESPACE=default $ISTIOPATH/samples/bookinfo/platform/kube/cleanup.sh
# Clear any existing logs in ingress
kubectl -n=istio-system delete pod $(kubectl get pods -A --no-headers -o custom-columns=":metadata.name" | grep ingress)
kne delete $GENPATH/wtf_topo.pbtxt
sleep 5 # wait for topo to be fully deleted before creating new one, and for ingress to be deleted before creating gateway
set -e

# Create KNE topology
# -n 32 -p 0.05 makes a reasonable topo with long paths
$WTFDIR/gen_topo_graph.py -n 4 -p 0.01 # gen random topo and paths
go run ./$WTFDIR -loop # gen topo file, testbed file, configs
# Need to build egress image after go run to copy in iface setup script
docker build -t egress $WTFDIR/egress
kind load docker-image egress:latest --name kne

kne create $GENPATH/wtf_topo.pbtxt

# 2. Create microservice app
pushd $ISTIOPATH
kubectl apply -f samples/bookinfo/platform/kube/bookinfo.yaml

# TODO change arbitrary sleeps to waiting for the right condition
echo Istio pods:
kubectl get pods -A
kubectl apply -f samples/bookinfo/networking/bookinfo-gateway.yaml

sleep 50 # Wait for gateway to come up before getting its IP, and routers to load startup configs before running tests
export INGRESS_HOST=$(kubectl -n istio-system get service istio-ingressgateway -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
export INGRESS_PORT=$(kubectl -n istio-system get service istio-ingressgateway -o jsonpath='{.spec.ports[?(@.name=="http2")].port}')
export SECURE_INGRESS_PORT=$(kubectl -n istio-system get service istio-ingressgateway -o jsonpath='{.spec.ports[?(@.name=="https")].port}')
export GATEWAY_URL=$INGRESS_HOST:$INGRESS_PORT
echo Istio gateway URL: $GATEWAY_URL

popd

# 3. Make requests
pushd $WTFDIR

# Note -config option is deprecated
# *.go compiles stuff from non-test file into test file
go test -v *.go -topology out/wtf_topo.pbtxt -testbed out/wtf_testbed.textproto -vendor_creds=NOKIA/admin/NokiaSrl1! -skip_reset=true
pushd out
../plot_trace.py
$ISTIOPATH/../proxy/src/envoy/http/alpn/wtf_trace_tests.py --input-file $KNEPATH/$GENPATH/$OUTFILE

popd
popd

popd

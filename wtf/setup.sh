#!/bin/bash

# One-time stuff that needs to happen before deploying
# Note this makes some sysctl changes

set -e

pushd ../..

# Get repos
git clone https://github.com/emilykmarx/proxy.git

# Get Istio proxy and SR Linux images
docker pull ghcr.io/nokia/srlinux:23.3.1
docker pull istio/pilot:1.15.0 # didn't modify pilot, but kind expects pilot & proxyv2 to be in same registry
docker tag istio/pilot:1.15.0 hub/pilot:tag

docker pull emarx1/wtf-project-istio:proxyv2_9d02d7b2 # dictates version of WTF agent
docker tag emarx1/wtf-project-istio:proxyv2_9d02d7b2 hub/proxyv2:tag

# Install Istio
ISTIO_VERSION=1.15.0
curl -L https://istio.io/downloadIstio | ISTIO_VERSION=$ISTIO_VERSION TARGET_ARCH=x86_64 sh -
cd istio-$ISTIO_VERSION
export PATH=$PWD/bin:$PATH
echo "PATH=$PWD/bin:$PATH" >> ~/.bashrc

# For large deployments
# Prevent "too many open files" when starting proxy container
sudo bash -c 'echo "fs.inotify.max_user_watches=1048576" >> /etc/sysctl.conf'
sudo bash -c 'echo "fs.inotify.max_user_instances=1024" >> /etc/sysctl.conf'
sudo sysctl -p /etc/sysctl.conf
# Prevent routers crashing due to arp cache overflow
sudo sysctl -w net.ipv4.neigh.default.gc_thresh1=12800
sudo sysctl -w net.ipv4.neigh.default.gc_thresh2=51200
sudo sysctl -w net.ipv4.neigh.default.gc_thresh3=102400

popd

#!/bin/bash

# One-time stuff that needs to happen before deploying
# Dependencies: KNE's dependencies (see ../docs/setup.md), but not KNE itself
# Tested with Ubuntu 20
# Note this makes some sysctl changes

set -e

pushd ../..

# Get repos, install KNE
git clone https://github.com/emilykmarx/proxy.git
git clone https://github.com/openconfig/ondatra.git
pushd kne
go mod tidy
make install
kne help
popd

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
echo "PATH=$PWD/bin:$PATH" >> ~/.bashrc
source ~/.bashrc

# For large deployments, to prevent "too many open files" when starting proxy container
sudo bash -c 'echo "fs.inotify.max_user_watches=1048576" >> /etc/sysctl.conf'
sudo bash -c 'echo "fs.inotify.max_user_instances=1024" >> /etc/sysctl.conf'
sudo sysctl -p /etc/sysctl.conf

popd

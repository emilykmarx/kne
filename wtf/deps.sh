#!/bin/bash

# Installs all dependencies needed for the rest of setup

set -e

# KNE dependencies
## Golang
curl -O https://dl.google.com/go/go1.20.1.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.20.1.linux-amd64.tar.gz
rm go1.20.1.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
export PATH=$PATH:$(go env GOPATH)/bin
echo "PATH=$PATH:/usr/local/go/bin" >> ~/.bashrc
echo "PATH=$PATH:$(go env GOPATH)/bin" >> ~/.bashrc

## Docker
sudo apt-get update
sudo apt-get install ca-certificates curl gnupg
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg
echo \
  "deb [arch="$(dpkg --print-architecture)" signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
  "$(. /etc/os-release && echo "$VERSION_CODENAME")" stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
VERSION_STRING=5:20.10.16~3-0~ubuntu-focal
sudo apt-get install docker-ce=$VERSION_STRING docker-ce-cli=$VERSION_STRING containerd.io docker-buildx-plugin docker-compose-plugin -y
sudo docker run hello-world
set +e # ok if exists
sudo groupadd docker
set -e
sudo usermod -aG docker $USER
# can't newgrp here, so won't be added to group yet

## Kubectl
curl -LO https://dl.k8s.io/release/v1.27.3/bin/linux/amd64/kubectl
sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl
rm kubectl

## Kind
go install sigs.k8s.io/kind@v0.19.0

## Make
sudo apt-get update
sudo apt-get install build-essential -y

# WTF dependencies
## Python packages
sudo apt install python3-pip -y
pip3 install networkx matplotlib

# KNE
pushd ..
make install
kne help
popd

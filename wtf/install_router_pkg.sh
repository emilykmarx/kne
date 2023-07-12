#!/bin/bash

set -e
PKGS=(iptables arptables) # iptables installs ip6tables

for PKG in "${PKGS[@]}"; do
  echo $PKG
  # on Centos 8 VM:
  #https://unix.stackexchange.com/a/260261

  # on local, in wtf:
  # mkdir -p iptables/rpm
  # gcloud compute scp 'centos-8-2:/etc/yum.repos.d/offline-iptables.repo' ./iptables
  # gcloud compute scp --recurse 'centos-8-2:/var/tmp/iptables/*' ./iptables/rpm

  # copy the rpms and repo file
  for pod in $(kubectl get pods -n wtf --no-headers -o custom-columns=":metadata.name" | grep srl); do kubectl cp $PKG/rpm wtf/$pod:/var/tmp/$PKG; done
  for pod in $(kubectl get pods -n wtf --no-headers -o custom-columns=":metadata.name" | grep srl); do kubectl cp $PKG/offline-$PKG.repo wtf/$pod:/etc/yum.repos.d; done

  # install
  for pod in $(kubectl get pods -n wtf --no-headers -o custom-columns=":metadata.name" | grep srl); do kubectl -n wtf exec -it $pod -- yum --disablerepo=\* --enablerepo=offline-$PKG install --nogpgcheck $PKG -y; done
done

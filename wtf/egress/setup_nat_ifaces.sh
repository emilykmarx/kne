#!/bin/sh

# NAT
sed -i '/ip_forward/s/^#//g' /etc/sysctl.conf
sysctl -p
echo iptables-persistent iptables-persistent/autosave_v4 boolean true | debconf-set-selections
echo iptables-persistent iptables-persistent/autosave_v6 boolean true | debconf-set-selections
apt-get update
apt-get -y install iptables-persistent
iptables -t nat -A POSTROUTING -j MASQUERADE
sh -c "iptables-save > /etc/iptables/rules.v4"

# Ifaces
sh egress_setup_ifaces.sh

sleep 2000000000000

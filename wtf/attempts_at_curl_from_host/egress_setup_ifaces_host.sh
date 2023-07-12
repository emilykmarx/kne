#!/bin/sh
ip addr add 192.168.0.7/31 dev e1-1
ip route add 172.18.0.0/24 via 192.168.0.6 dev e1-1 src 192.168.0.7

sleep 2000000000000

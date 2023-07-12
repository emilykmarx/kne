ip addr add 192.168.0.5/31 dev e1-1
ip route add 192.168.0.2/32 via 192.168.0.4 dev e1-1 src 192.168.0.5
ip route add 192.168.0.0/32 via 192.168.0.4 dev e1-1 src 192.168.0.5
ip route add 192.168.0.0/32 via 192.168.0.4 dev e1-1 src 192.168.0.5
ip route add 192.168.0.7/32 via 192.168.0.4 dev e1-1 src 192.168.0.5

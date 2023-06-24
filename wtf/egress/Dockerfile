FROM ubuntu:22.04

WORKDIR /usr/src/app

# Note: ondatra test may use some of this; check before removing
RUN apt-get update \
    && apt-get install -y \
    curl --no-install-recommends \
    iproute2 \
    tcpdump \
    iputils-ping \
    && rm -rf /var/lib/apt/lists/*

# Since docker isn't in bridge mode it doesn't set up its own iptables rules,
# so should be fine messing with iptables.
# But, can't do that at build time since requires privileged mode
COPY setup_nat_ifaces.sh .
COPY out/egress_setup_ifaces.sh .
RUN chmod +x ./setup_nat_ifaces.sh

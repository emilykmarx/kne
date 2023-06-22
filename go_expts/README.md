# End-to-end scoping in network + microservice testbed
A testbed consisting of an emulated network (KNE) connected to a service mesh (Istio).
## Deploy
To deploy the testbed from scratch: `go_expts/deploy.sh deploy` (TODO wow fix silly name). See comment in that script for dependencies.

To deploy in an existing cluster already set up for KNE & Istio, remove `deploy`.
This will clean up any existing routers, microservice pods, and Istio gateway in the cluster before recreating them and sending requests.

## Architecture
The KNE portion is a random topology of Nokia routers with L3 interfaces. IPs and routes are configured statically, with a default route to the "egress" node. This node is a bit special since it connects KNE and Istio. It is of KNE type HOST, with the KNE interfaces connected to the routers and the regular Kubernetes interface (eth0) connected to Istio. It is also a NAT, translating between the KNE and Istio address spaces.

The egress node sends traffic to the Istio ingress gateway, which routes to pods running the "bookinfo" application.

In KNE, the WTF agent is a standalone ondatra client, which uses gNMI to get telemetry and routing tables from the routers (currently the egress node doesn't have a WTF agent).

In Istio, the WTF agent is a proxy extension running in the ingress gateway and in front of each application container, as an Envoy HTTP filter.

## Inject faults
Faults can be injected in KNE and Istio. At the time of writing the following faults have been tested:
KNE: Transient routing loop

Istio: Sporadic stream resets

## Add metrics
Metrics can be collected from KNE and Istio. Convenient ways to do this include:

KNE: Telemetry exposed by gNMI, e.g. interface counters. Custom counters can be added by creating an ACL to match packets of interest and attaching it to enabled interfaces.

Istio: Log things in the application or proxy.

## Send and scope requests
Requests can be sent from a router or a pod in the Istio mesh, and to a router or the Istio gateway (from which it will traverse the mesh based on the request type). To send a trace request for request ID `X`, set the `x-request-id` HTTP header to `WTFTRACE-X`.

KNE uses the request destination IP and routing tables for scoping.

Istio uses the request ID HTTP header and history stored by the proxy agent.

## Some code details
This is in a kne fork for convenience, but does not require changes to kne itself (other than upgrading ondatra to fix a problem with large networks, but that may be fixed in a newer version of kne -- should check).

It does require changes to istio's "proxy" repo and to Envoy (there is a script in the proxy repo to pull those changes into a Docker image which this repo's deploy script assumes is already built). Due to these changes the extension is built into Envoy rather than loaded as a WASM module.

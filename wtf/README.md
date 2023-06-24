# End-to-end network + microservice testbed
A testbed consisting of an emulated network (KNE) connected to a service mesh (Istio).

# General

## Deploy
```
# Note: the scripts below use the parent directory of this repo (i.e. `../..`) as a working directory.
# See comments at top of each script for dependencies.

./setup.sh

# Place the license file for SR Linux 23.3.1 in current directory.
LICENSE=<license filename> ./deploy_cluster.sh

# Watch output of this script as it runs.
# There are a few points where it sleeps for a bit instead of polling for the relevant condition.
# Whether these sleeps are long enough depends on size of experiment, resources, etc -- may need to manually pause execution.
# Also, note the Istio gateway URL for later
./deploy_topo_app.sh
```

## Architecture
The KNE portion is a random topology of Nokia routers with L3 interfaces.
The size and connectedness of the topology can be changed by editing the call to `gen_topo_graph.py` in `deploy_topo_app.sh`.
(The current values spin up a small topology that reproduces the crash problem.)

IPs and routes are configured statically, with a default route to the "egress" node.
This node is of KNE type HOST, with the KNE interfaces connected to the routers and eth0 connected to Istio.
It is also a NAT, translating between the KNE and Istio address spaces.

The egress node sends traffic to the Istio ingress gateway, which routes to pods running the [bookinfo](https://istio.io/latest/docs/examples/bookinfo/) application.
The gateway and application pods run a modified version of the Istio proxy (details below).

## Request types
There are two request types. Type 1 succeeds, and Type 2 causes routers to crash when there are many application pods.
The two types differ in the `x-request-id` HTTP header.
Both are requests to the Istio gateway for bookinfo's `productpage`.

```
GATEWAY_URL=172.18.0.50:80 # Istio ingress gateway URL (from output of `deploy_topo_app.sh`)
kubectl exec -n wtf srl0 -- ip netns exec srbase-DEFAULT curl --interface 192.168.0.0 http://$GATEWAY_URL/productpage -H 'x-request-id: <...>'
```

Type 1 requests can have any `x-request-id` that does not start with `WTFTRACE-`.

Type 2 requests have an `x-request-id` of the form `WTFTRACE-X`, where `X` is any string that does not match a previously used `x-request-id`.

## Some code details
This is in a kne fork for convenience, but does not require changes to kne itself (other than upgrading ondatra to fix a problem with large networks, but that may be fixed in a newer version of kne -- should check).

It does require changes to istio's `proxy` repo and to Envoy (there is a script in the `proxy` repo to pull those changes into a Docker image which this repo's deploy script assumes is already built). Due to these changes the extension is built into Envoy rather than loaded as a WASM module.


# WTF

In KNE, the WTF agent is a standalone ondatra client, which uses gNMI to get telemetry and routing tables from the routers (currently the egress node doesn't have a WTF agent).

In Istio, the WTF agent is a proxy extension running in the ingress gateway and in front of each application container, as an Envoy HTTP filter.
To run WTF tests, pass `run-tests` to `deploy_topo_app.sh` (this will also re-deploy the routers and app)

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

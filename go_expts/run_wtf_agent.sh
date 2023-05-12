#!/bin/bash
set -xe

# Go must be run from project root
pushd ~/projects/wtf_project/kne
kne delete go_expts/wtf_topo.pbtxt
# 4 nodes is ok locally; 8 needs more (per kne troubleshooting doc)
go_expts/gen_topo_graph.py -n 4 -p 0.01 # gen random topo and paths
go run ./go_expts # gen topo file, testbed file, configs
sleep 8 # wait for topo to be fully deleted
kne create go_expts/wtf_topo.pbtxt
# Takes a while after topo is created for the routers to be fully ready (e.g. have network-instance up)
# Should do this more programmatically in the test
sleep 30

pushd go_expts

# Note -config option is deprecated
# *.go compiles stuff from non-test file into test file
go test -v *.go -topology wtf_topo.pbtxt -testbed wtf_testbed.textproto -vendor_creds=NOKIA/admin/NokiaSrl1! -skip_reset=true
./plot_trace.py

popd
popd

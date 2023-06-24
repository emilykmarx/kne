package main

import (
	"bytes"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/openconfig/ondatra"
	"github.com/openconfig/ondatra/gnmi"
	oc "github.com/openconfig/ondatra/gnmi/oc"
	"github.com/openconfig/ygnmi/ygnmi"

	//oc "github.com/openconfig/ondatra/gnmi/oc" // In case of lost import
	kinit "github.com/openconfig/ondatra/knebind/init"
)

const namespace = "wtf"

const shell_ping_cmd = "ip netns exec srbase-%v ping %v -c%v -I%v"
const curl_cmd = "ip netns exec srbase-%v curl --interface %v http://%v/productpage -H 'x-request-id: %v'"

var outfile = os.Getenv("WTF_TESTOUTFILE")
var gateway_url = os.Getenv("GATEWAY_URL")
var gateway_ip = strings.Split(gateway_url, ":")[0]

func TestMain(m *testing.M) {
	os.Remove(outfile)
	ondatra.RunTests(m, kinit.Init)
}

// TODO share these field names with python plot script
type OnPathRouter struct {
	// The topo names, not the testbed names
	Name        string
	PrevHopLink *OnPathRouter `json:"-"`
	// Ifaces the ping would have come in/gone out on
	InIface  Iface
	OutIface Iface
}

type Router struct {
	Name      string
	BadIfaces []string
}

// One request to be traced (by KNE and Istio)
type RequestOutput struct {
	TracedIP  string           // for KNE scoping
	RequestID string           // for Istio scoping
	Paths     [][]OnPathRouter // for KNE scoping
}

// Info about all traced requests, for use by KNE and Istio plot scripts
type Output struct {
	// Includes bits. In reality, this would be a window
	AllRouters []Router
	Requests   []RequestOutput
}

// Make sure to lock this if run tests in //
var global_output Output

type ExecLocation int64

const (
	Shell ExecLocation = iota
	RouterShell
	RouterCLI
)

// Exec command from location
func exec_wrapper(topo_name string, command string, location ExecLocation) (string, string, error) {
	full_command := command
	if location == RouterCLI {
		command = "sr_cli " + command
	}
	if location == RouterShell || location == RouterCLI {
		full_command = fmt.Sprintf("kubectl -n %v exec -it %v -- %v", namespace, topo_name, command)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.Command("bash", "-c", full_command)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// Wrapper around gnmi.LookupAll: Filter out entries without values
func LookupAllVals[V any](t *testing.T, dut *ondatra.DUTDevice, q ygnmi.WildcardQuery[V]) []V {
	all_entries := gnmi.LookupAll(t, dut, q)
	vals := []V{}
	for _, entry := range all_entries {
		entry_val, has_val := entry.Val()
		if !has_val {
			t.Fatalf("Entry has no val")
		}
		vals = append(vals, entry_val)
	}
	return vals
}

// Get all enabled ifaces in the network
// Note: Only deal with IPv4 for now
func getAllIfaces(t *testing.T) map[string]struct {
	string
	*ondatra.DUTDevice
} {

	iface_ips := map[string]struct {
		string
		*ondatra.DUTDevice
	}{} // iface IP => {iface name, owning dut}

	for _, dut := range ondatra.DUTs(t) {
		ifaces := LookupAllVals(t, dut, gnmi.OC().InterfaceAny().State())
		for _, iface := range ifaces {
			if *iface.Enabled && !strings.Contains(*iface.Name, "mgmt") { // ignore mgmt iface
				subifaces := LookupAllVals(t, dut, gnmi.OC().Interface(*iface.Name).SubinterfaceAny().State())
				for _, subiface := range subifaces {
					if *subiface.Enabled {
						iface_name := fmt.Sprintf("%v.%v", *iface.Name, *subiface.Index)
						// Get IP
						for ip := range subiface.Ipv4.Address {
							iface_ips[ip] = struct {
								string
								*ondatra.DUTDevice
							}{iface_name, dut}
						}
					}
				}
			}
		}

	}

	return iface_ips
}

// Haven't found an OpenConfig way to do LPM (no /routing-policy set)
func lpm(t *testing.T, dut *ondatra.DUTDevice, dest_ip string) *oc.NetworkInstance_Afts_Ipv4Entry {
	route_entries := LookupAllVals(t, dut, gnmi.OC().NetworkInstance(network_instance).Afts().Ipv4EntryAny().State())
	var longest_match *oc.NetworkInstance_Afts_Ipv4Entry
	longest_match_len := -1
	var default_match *oc.NetworkInstance_Afts_Ipv4Entry
	for _, entry := range route_entries {
		entry_prefix := netip.MustParsePrefix(*entry.Prefix)
		if entry_prefix.Contains(netip.MustParseAddr(dest_ip)) && entry_prefix.Bits() > longest_match_len {
			longest_match_len = entry_prefix.Bits()
			longest_match = entry
		} else if entry_prefix.String() == "0.0.0.0/0" {
			default_match = entry
		}

	}
	if longest_match != nil {
		return longest_match
	}
	if default_match == nil {
		t.Fatalf("No LPM for %v on %v\n", dest_ip, dut)
	}
	return default_match
}

// Figure out which of src's ifaces the dest has a route to
// Needed when routers on reply path only have a route to some of the src's ifaces
func sourceAddr(t *testing.T, src_dut *ondatra.DUTDevice, dest_ip string) string {
	iface_ips := getAllIfaces(t)
	info := OnPathRouter{}
	nexthop_dut(t, src_dut, &info, dest_ip, iface_ips)
	return info.OutIface.IP
}

// Ping egress from all routers
// This is a way to check KNE is set up correctly
/* Ondatra's raw gNOI ping doesn't work bc it doesn't allow setting network instance,
* and router CLI doesn't allow ping in non-interactive mode */
func TestPingEgress(t *testing.T) {
	ping_count := 1
	// Ondatra doesn't seem to support hosts, so must do raw kubectl
	ip_line, _, err := exec_wrapper("", "kubectl -n wtf exec -it egress -- ip addr | grep 192", Shell)
	if err != nil {
		t.Fatal(err)
	}

	egress_ip := netip.MustParsePrefix(strings.Split(strings.TrimSpace(ip_line), " ")[1]).Addr().String()
	for _, dut := range ondatra.DUTs(t) {
		full_shell_ping_cmd := fmt.Sprintf(shell_ping_cmd, network_instance, egress_ip, ping_count, sourceAddr(t, dut, egress_ip))
		// ping stderr will complain about tty and whatnot
		stdout, _, err := exec_wrapper(dut.Name(), full_shell_ping_cmd, RouterShell)
		ping_success := strings.Contains(stdout, fmt.Sprintf("%v received", ping_count))
		if !ping_success {
			t.Fatalf("Ping received < sent: %v from %v\n\nStdout: %v\nErr: %v\n", egress_ip, dut.Name(), stdout, err)
		}
	}
}

// Create loop, curl gateway (from beginning of path with loop), undo loop, curl again
// Trace the first request afterwards, accounting for both the looped and normal routing tables (simulating a transient loop)
func TestLoop(t *testing.T) {
	// 1. Get the loop details (routers involved) from the configs
	// If multiple looped paths just use the first for now
	loop_create_files, err := filepath.Glob("out/" + loop_create_filename[0:strings.Index(loop_create_filename, "%")] + "*")
	if err != nil {
		t.Fatal(err)
	}
	var looped_router int
	var src_router int
	if _, err := fmt.Sscanf(loop_create_files[0], "out/"+loop_create_filename, &looped_router, &src_router); err != nil {
		t.Fatalf("Loop create file named wrong?\n")
	}
	looped_dut := topoNameToDUT(t, fmt.Sprintf("%v%v", dut_name_prefix, looped_router))
	src_dut := topoNameToDUT(t, fmt.Sprintf("%v%v", dut_name_prefix, src_router))

	// 2. Create loop, curl should fail
	loop_create_cmd := fmt.Sprintf("kne topology push %v %v %v", "out/"+topo_filename, looped_dut.Name(), loop_create_files[0])
	_, _, err = exec_wrapper(looped_dut.Name(), loop_create_cmd, Shell)
	if err != nil {
		t.Fatal(err)
	}
	loop_request_id := fmt.Sprintf("test-request-%v-loop", src_dut.Name())
	stdout, _, _ := curlIstioGateway(t, loop_request_id, src_dut)
	curl_success := strings.Contains(stdout, "<html>")
	if curl_success {
		t.Fatalf("Expected loop curl to fail\n")
	}

	// Get path before changing routing tables back (in lieu of watching routing tables)
	paths := [][]OnPathRouter{getPath(t, src_dut, gateway_ip), {}} // will fill in second path after fixing loop

	// 3. Undo loop, curl should succeed
	loop_undo_files, err := filepath.Glob("out/" + loop_undo_filename[0:strings.Index(loop_undo_filename, "%")] + "*")
	if err != nil {
		t.Fatal(err)
	}

	loop_undo_cmd := fmt.Sprintf("kne topology push %v %v %v", "out/"+topo_filename, looped_dut.Name(), loop_undo_files[0])
	_, _, err = exec_wrapper(looped_dut.Name(), loop_undo_cmd, Shell)
	if err != nil {
		t.Fatal(err)
	}

	request_id := fmt.Sprintf("test-request-%v-loop-undone", src_dut.Name())
	stdout, _, _ = curlIstioGateway(t, request_id, src_dut)
	curl_success = strings.Contains(stdout, "<html>")
	if !curl_success {
		t.Fatalf("Expected undone loop curl to succeed\n")
	}

	// 4. Trace looped request
	paths[1] = getPath(t, src_dut, gateway_ip)
	traceRequest(t, src_dut, loop_request_id, paths)
}

func curlIstioGateway(t *testing.T, request_id string, dut *ondatra.DUTDevice) (string, string, error) {
	src_addr := sourceAddr(t, dut, gateway_ip)
	// Note src addr needed even if directly attached to host
	full_curl_cmd := fmt.Sprintf(curl_cmd, network_instance, src_addr, gateway_url, request_id)
	return exec_wrapper(dut.Name(), full_curl_cmd, RouterShell)
}

// For each dut, fetch the productpage a bunch of times.
// Trace requests that failed (presumably due to a problem in the microservice part,
// since by this point the network faults have been fixed)
func TestCurlIstioGateway(t *testing.T) {
	var failed_requests []struct {
		string
		*ondatra.DUTDevice
	}
	for _, dut := range ondatra.DUTs(t) {
		for i := 0; i < 3; i++ {
			request_id := fmt.Sprintf("test-request-%v-%v", dut.Name(), i)
			stdout, stderr, err := curlIstioGateway(t, request_id, dut)
			curl_success := strings.Contains(stdout, "<html>") && !strings.Contains(stdout, "details are currently unavailable")
			if !curl_success {
				t.Logf("Curl Istio gateway from %v failed\n\nStdout: %v\nStderr: %v\nErr: %v\n", dut.Name(), stdout[:20], stderr, err)
				failed_requests = append(failed_requests, struct {
					string
					*ondatra.DUTDevice
				}{request_id, dut})
			}
		}
	}

	// Trace if failed (ideally on a path not involving the previous loop, to show impact of scoping)
	for _, request := range failed_requests {
		paths := [][]OnPathRouter{getPath(t, request.DUTDevice, gateway_ip)}
		traceRequest(t, request.DUTDevice, request.string, paths)
	}
}

// Get all ifaces with nonzero TTL expire counts
// Also output non-bad routers, for completeness
func getAllBits(t *testing.T) []Router {
	all_routers := []Router{}
	// Nokia doesn't support /acl OpenConfig it seems
	get_ttl_counts_cmd := fmt.Sprintf("info from state acl ipv4-filter %v", ttl_acl)
	for _, dut := range ondatra.DUTs(t) {
		router := Router{Name: dut.Name()}
		stdout, _, err := exec_wrapper(dut.Name(), get_ttl_counts_cmd, RouterCLI)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(stdout, "\n")
		for i, line := range lines {
			if strings.Contains(line, "subinterface ") {
				iface := strings.Split(strings.TrimSpace(line), " ")[1]
				count, err := strconv.Atoi(strings.Split(strings.TrimSpace(lines[i+1]), " ")[1])
				if err != nil {
					t.Fatal(err)
				}
				if count > 0 {
					router.BadIfaces = append(router.BadIfaces, iface)
				}
			}
		}
		all_routers = append(all_routers, router)
	}
	return all_routers
}

// debug
func showAllRouteTables(t *testing.T) {
	for _, dut := range ondatra.DUTs(t) {
		show_route_cmd := fmt.Sprintf("show network-instance %v route-table", network_instance)
		stdout, stderr, err := exec_wrapper(dut.Name(), show_route_cmd, RouterCLI)
		if err != nil {
			fmt.Println(stderr)
			t.Fatal(err)
		}
		fmt.Println(dut.Name())
		fmt.Println(stdout)
	}
}

func topoNameToDUT(t *testing.T, topo_name string) *ondatra.DUTDevice {
	for _, topo_dut := range ondatra.DUTs(t) {
		if topo_name == topo_dut.Name() {
			return topo_dut
		}
	}
	t.Fatalf("Dut with topo name %v (from iface neighbor) not found\n", topo_name)
	return nil
}

// Find the (enabled) iface on the dut with the given neighbor IP
// Note: Only deal with IPv4 for now
func neighIPToIface(t *testing.T, dut *ondatra.DUTDevice, target_neigh_ip string) Iface {
	ifaces := LookupAllVals(t, dut, gnmi.OC().InterfaceAny().State())
	for _, iface := range ifaces {
		if *iface.Enabled {
			subifaces := LookupAllVals(t, dut, gnmi.OC().Interface(*iface.Name).SubinterfaceAny().State())
			for _, subiface := range subifaces {
				if *subiface.Enabled {
					for neigh_ip := range subiface.Ipv4.Neighbor {
						if neigh_ip == target_neigh_ip {
							for ip := range subiface.Ipv4.Address {
								// Using first IP as IP of iface, if iface has multiple
								return Iface{Name: fmt.Sprintf("%v.%v", *iface.Name, *subiface.Index), IP: ip}
							}
						}
					}
				}
			}
		}
	}
	t.Fatalf("Iface with neigh IP %v not found on dut %v\n", target_neigh_ip, dut.Name())
	return Iface{}
}

// If static route: other endpoint. If direct route: this node
func route_node(t *testing.T, dut *ondatra.DUTDevice, dest_ip string) *oc.NetworkInstance_Afts_NextHop {
	nexthop_route := lpm(t, dut, dest_ip)
	nexthop_group_id := *nexthop_route.NextHopGroup
	nexthops := LookupAllVals(t, dut,
		gnmi.OC().NetworkInstance(network_instance).Afts().NextHopGroup(nexthop_group_id).NextHopAny().State())
	if len(nexthops) != 1 {
		t.Fatalf("Next hop group %v has %v next hops, expected 1\n", nexthop_group_id, len(nexthops))
	}
	nexthop_idx := *nexthops[0].Index
	nexthop, has_val := gnmi.Lookup(t, dut,
		gnmi.OC().NetworkInstance(network_instance).Afts().NextHop(nexthop_idx).State()).Val()
	if !has_val {
		t.Fatalf("Next hop %v has no val\n", nexthop_idx)
	}
	return nexthop
}

// EASYTODO better logging statements
func nexthop_dut(t *testing.T, dut *ondatra.DUTDevice, curhop *OnPathRouter, dest_ip string,
	iface_ips map[string]struct {
		string
		*ondatra.DUTDevice
	}) (*ondatra.DUTDevice, OnPathRouter) {

	found_route_node := route_node(t, dut, dest_ip)
	if found_route_node.IpAddress == nil {
		// No next hop
		neigh_ip := curhop.PrevHopLink.OutIface.IP
		curhop.InIface = neighIPToIface(t, dut, neigh_ip)
		return nil, OnPathRouter{}
	}

	nexthop_info, ok := iface_ips[*found_route_node.IpAddress]
	if !ok {
		// No dut found with this IP
		// Expected if IP is one of egress's =>
		// If route to egress is direct (normal case): get out iface IP from route to egress
		// Else (presumably a loop): resolve it
		found_route_node = route_node(t, dut, *found_route_node.IpAddress)
		nexthop_info = iface_ips[*found_route_node.IpAddress]
	}

	if nexthop_info.DUTDevice != nil && nexthop_info.DUTDevice.Name() != dut.Name() {
		return nexthop_static(t, dut, curhop, found_route_node, nexthop_info)
	}

	return nexthop_direct(t, dut, curhop, found_route_node)
}

func nexthop_static(t *testing.T, dut *ondatra.DUTDevice, curhop *OnPathRouter,
	found_route_node *oc.NetworkInstance_Afts_NextHop, nexthop_info struct {
		string
		*ondatra.DUTDevice
	}) (*ondatra.DUTDevice, OnPathRouter) {
	nexthop_router := OnPathRouter{Name: nexthop_info.DUTDevice.Name(), PrevHopLink: curhop,
		InIface: Iface{Name: nexthop_info.string, IP: *found_route_node.IpAddress}}

	curhop.OutIface = neighIPToIface(t, dut, *found_route_node.IpAddress)
	return nexthop_info.DUTDevice, nexthop_router
}

func nexthop_direct(t *testing.T, dut *ondatra.DUTDevice, curhop *OnPathRouter,
	found_route_node *oc.NetworkInstance_Afts_NextHop) (*ondatra.DUTDevice, OnPathRouter) {
	if found_route_node.GetInterfaceRef() == nil {
		t.Fatalf("next hop iface nil")
	}
	// local name/IP of outgoing iface
	iface_name := *found_route_node.InterfaceRef.Interface
	iface_ip := *found_route_node.IpAddress
	subiface := *found_route_node.InterfaceRef.Subinterface
	curhop.OutIface = Iface{Name: fmt.Sprintf("%v.%v", iface_name, subiface), IP: iface_ip}

	iface_info, has_val := gnmi.Lookup(t, dut, gnmi.OC().Lldp().Interface(iface_name).State()).Val()
	if !has_val {
		t.Fatalf("No iface info")
	}
	if len(iface_info.Neighbor) > 1 {
		t.Fatalf("Interface %v has %v neighbor DUTs; expected exactly one\n", iface_name, len(iface_info.Neighbor))
	}
	if len(iface_info.Neighbor) == 0 {
		// Iface to egress is expected to have none
		return nil, OnPathRouter{}
	}

	var iface_neighbor_dut string
	for _, neigh := range iface_info.Neighbor {
		iface_neighbor_dut = *neigh.SystemName // The topo name (this may be Nokia-specific)
	}

	nexthop_dut := topoNameToDUT(t, iface_neighbor_dut)
	nexthop_router := OnPathRouter{Name: iface_neighbor_dut, PrevHopLink: curhop}

	return nexthop_dut, nexthop_router
}

// Get routers on path, using currently active routing tables (so if changing routing tables, call before & after change).
// In reality, would watch for routing table change instead of tracing twice (https://netdevops.me/2020/arista-eos-gnmi-tutorial/).
// Note: Does not include host if host is on path
func getPath(t *testing.T, src_dut *ondatra.DUTDevice, dest_ip string) []OnPathRouter {
	iface_ips := getAllIfaces(t)
	dut := src_dut
	info := OnPathRouter{Name: src_dut.Name()}
	path := []OnPathRouter{info}
	path_ids := map[string]bool{src_dut.Name(): true} // to detect loop
	i := 1
	for dut != nil {
		dut, info = nexthop_dut(t, dut, &path[i-1], gateway_ip, iface_ips)
		if dut == nil {
			break
		}
		_, loop := path_ids[dut.Name()]
		path = append(path, info)
		path_ids[dut.Name()] = true

		if loop {
			break
		}
		i++
	}

	return path
}

// Send a trace request to the gateway, record info
func traceRequest(t *testing.T, src_dut *ondatra.DUTDevice, request_id string, paths [][]OnPathRouter) {
	// Send a trace request. This doesn't affect scoping in the network (hence don't care about output),
	// but it's more realistic to send from the router.
	curlIstioGateway(t, "WTFTRACE-"+request_id, src_dut)
	var out RequestOutput
	out.TracedIP = gateway_ip
	out.Paths = paths
	out.RequestID = request_id
	global_output.Requests = append(global_output.Requests, out)
}

// Needs to come last
func TestWriteOutput(t *testing.T) {
	global_output.AllRouters = getAllBits(t)
	b, err := json.MarshalIndent(global_output, "", " ")
	if err != nil {
		fmt.Printf("Error marshaling output: %v\n", err)
	}
	if err := os.WriteFile(outfile, b, 0666); err != nil {
		t.Fatalf("Error writing output: %v\n", err)
	}
}

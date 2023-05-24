package main

import (
	"bytes"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
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
const outfile = "test_out.json"
const shell_ping_cmd = "ip netns exec srbase-%v ping %v -c%v -I%v"

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

type Output struct {
	Paths      [][]OnPathRouter
	AllRouters []Router
	TracedIP   string
}

// To share state b/w bit-gathering test and trace test:
// (Not a problem now, since setting bits at trace time)
// Either t.Parallel() or spin off a thread in test if that's allowed

// Make sure to lock this if run tests in //
// (https://stackoverflow.com/questions/68126459/is-test-xxx-func-safe-to-access-shared-data-in-golang
var global_output Output

type ExecLocation int64

const (
	Shell ExecLocation = iota
	RouterShell
	RouterCLI
)

// Exec command
func exec_wrapper(topo_name, command string, location ExecLocation) (string, string, error) {
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
	for _, entry := range route_entries {
		entry_prefix := netip.MustParsePrefix(*entry.Prefix)
		if entry_prefix.Contains(netip.MustParseAddr(dest_ip)) && entry_prefix.Bits() > longest_match_len {
			longest_match_len = entry_prefix.Bits()
			longest_match = entry
		}
	}
	return longest_match
}

// For each dut, find which IPs (not just routers - need the iface) it has static routes to
// Note: Would need to modify this slightly to support multiple
func getAllRoutedIfaces(t *testing.T) map[string][]string {
	// dut => [dest_ips]
	routed_ips := map[string][]string{}
	for _, dut := range ondatra.DUTs(t) {
		route_entries := LookupAllVals(t, dut, gnmi.OC().NetworkInstance(network_instance).Afts().Ipv4EntryAny().State())
		routed_ips[dut.Name()] = []string{}
		for _, entry := range route_entries {
			if entry.OriginProtocol == oc.PolicyTypes_INSTALL_PROTOCOL_TYPE_STATIC {
				dest_ip := netip.MustParsePrefix(*entry.Prefix).Addr().String()
				routed_ips[dut.Name()] = append(routed_ips[dut.Name()], dest_ip)
			}
		}
	}
	return routed_ips
}

// Figure out which of src's ifaces the dest has a route to
// Needed for ping that crosses subnets: Must set source addr to the iface that dst has a route to
// (else reply can't be routed)
func sourceForPing(t *testing.T, src_dut *ondatra.DUTDevice, dest_ip string) string {
	routed_ips := getAllRoutedIfaces(t)
	iface_ips := getAllIfaces(t)

	// iface IP => iface name, owning dut
	dst_dut := iface_ips[dest_ip].DUTDevice.Name()
	dst_routed_ips := routed_ips[dst_dut]
	// IPs dest has a route to
	for _, ip := range dst_routed_ips {
		// IP is one of src's
		if iface_ips[ip].DUTDevice.Name() == src_dut.Name() {
			return ip
		}
	}

	return ""
}

// For the looped path: ping (from beginning of path), trace, undo loop, ping & trace again
// XXX: watch for routing table change instead of tracing twice (https://netdevops.me/2020/arista-eos-gnmi-tutorial/).
// May need to think abt timing of changes across diff routers.
func TestPingLoop(t *testing.T) {
	// If multiple looped routers just ping the first for now
	// EASYTODO rm loop undo files (and others) after test over
	t.Skip() // Remove if ran topo gen with -loop option!
	loop_undo_files, err := filepath.Glob(loop_undo_filename[0:strings.Index(loop_undo_filename, "%")] + "*")
	if err != nil {
		t.Fatal(err)
	}
	var looped_router int
	var src_router int
	if _, err := fmt.Sscanf(loop_undo_files[0], loop_undo_filename, &looped_router, &src_router); err != nil {
		t.Fatalf("Loop undo file named wrong?\n")
	}
	looped_dut := topoNameToDUT(t, fmt.Sprintf("%v%v", dut_name_prefix, looped_router))
	src_dut := topoNameToDUT(t, fmt.Sprintf("%v%v", dut_name_prefix, src_router))
	b, err := os.ReadFile(loop_undo_files[0])
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(template_ip[0:len(template_ip)-1] + ".*/32")
	dest_prefix := string(re.Find(b))
	dest_ip := dest_prefix[0 : len(dest_prefix)-len("/32")]

	ping_count := 1
	/* Ondatra's raw gNOI ping doesn't work bc it doesn't allow setting network instance,
	 * and router CLI doesn't allow ping in non-interactive mode */
	full_shell_ping_cmd := fmt.Sprintf(shell_ping_cmd, network_instance, dest_ip, ping_count,
		sourceForPing(t, src_dut, dest_ip))
	// ping stderr will complain about tty and whatnot
	stdout, _, _ := exec_wrapper(src_dut.Name(), full_shell_ping_cmd, RouterShell)
	ping_success := strings.Contains(stdout, fmt.Sprintf("%v received", ping_count))
	if ping_success {
		t.Fatalf("Expected loop ping %v to fail\n", full_shell_ping_cmd)
	}
	tracePing(t, src_dut, dest_ip)

	loop_undo_cmd := fmt.Sprintf("kne topology push %v %v %v", topo_filename, looped_dut.Name(), loop_undo_files[0])
	_, _, err = exec_wrapper(looped_dut.Name(), loop_undo_cmd, Shell)
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, _ = exec_wrapper(src_dut.Name(), full_shell_ping_cmd, RouterShell)
	ping_success = strings.Contains(stdout, fmt.Sprintf("%v received", ping_count))
	if !ping_success {
		t.Fatalf("Expected fixed loop ping %v to succeed\n", full_shell_ping_cmd)
	}
	tracePing(t, src_dut, dest_ip)

	// Once loop is undone, all paths should work
}

// For each dut with a (bidirectional) static route, ping the dest of that static route
func TestPingAllStaticRoutes(t *testing.T) {
	ping_count := 1
	for _, dut := range ondatra.DUTs(t) {
		/* Find out which dest this dut has a static route to
		 * (different duts may route to different ifaces of same dest) */
		route_entries := LookupAllVals(t, dut, gnmi.OC().NetworkInstance(network_instance).Afts().Ipv4EntryAny().State())
		for _, entry := range route_entries {
			if entry.OriginProtocol == oc.PolicyTypes_INSTALL_PROTOCOL_TYPE_STATIC {
				dest_ip := netip.MustParsePrefix(*entry.Prefix).Addr().String()
				src_ip := sourceForPing(t, dut, dest_ip)
				if len(src_ip) == 0 {
					// This is expected if there is no static route from dest => src
					fmt.Printf("No source addr for %v to ping %v\n", dut.Name(), dest_ip)
					continue
				}
				full_shell_ping_cmd := fmt.Sprintf(shell_ping_cmd, network_instance, dest_ip, ping_count, src_ip)
				stdout, _, err := exec_wrapper(dut.Name(), full_shell_ping_cmd, RouterShell)
				ping_success := strings.Contains(stdout, fmt.Sprintf("%v received", ping_count))
				if !ping_success {
					t.Fatalf("Ping received < sent: %v from %v\n\nStdout: %v\nErr: %v\n", dest_ip, dut.Name(), stdout, err)
				}
			}
		}
	}
}

// Get all ifaces with nonzero TTL expire counts
// Also output non-bad routers, for completeness
func TestGetAllBits(t *testing.T) {
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
	global_output.AllRouters = all_routers
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

// EASYTODO better logging statements
func nexthop_dut(t *testing.T, dut *ondatra.DUTDevice, curhop *OnPathRouter, dest_ip string,
	iface_ips map[string]struct {
		string
		*ondatra.DUTDevice
	}) (*ondatra.DUTDevice, OnPathRouter) {
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
	nexthop_ip := nexthop.IpAddress // If static route: IP of other endpoint. If direct route: outgoing IP
	if nexthop_ip == nil {
		neigh_ip := curhop.PrevHopLink.OutIface.IP
		curhop.InIface = neighIPToIface(t, dut, neigh_ip)
		return nil, OnPathRouter{}
	}
	nexthop_info := iface_ips[*nexthop_ip]
	if nexthop_info.DUTDevice.Name() != dut.Name() {
		// Static route
		nexthop_router := OnPathRouter{Name: nexthop_info.DUTDevice.Name(), PrevHopLink: curhop,
			InIface: Iface{Name: nexthop_info.string, IP: *nexthop_ip}}

		curhop.OutIface = neighIPToIface(t, dut, *nexthop_ip)
		return nexthop_info.DUTDevice, nexthop_router
	}

	// Direct route
	if nexthop.GetInterfaceRef() == nil {
		t.Fatalf("next hop iface nil")
	}
	// local name/IP of outgoing iface
	iface_name := *nexthop.InterfaceRef.Interface
	iface_ip := *nexthop.IpAddress
	subiface := *nexthop.InterfaceRef.Subinterface

	iface_info, has_val := gnmi.Lookup(t, dut, gnmi.OC().Lldp().Interface(iface_name).State()).Val()
	if !has_val {
		t.Fatalf("No iface info")
	}
	if len(iface_info.Neighbor) != 1 {
		t.Fatalf("Interface %v has %v neighbor DUTs; expected exactly one\n", iface_name, len(iface_info.Neighbor))
	}

	var iface_neighbor_dut string
	for _, neigh := range iface_info.Neighbor {
		iface_neighbor_dut = *neigh.SystemName // The topo name (this may be Nokia-specific)
	}

	nexthop_dut := topoNameToDUT(t, iface_neighbor_dut)
	nexthop_router := OnPathRouter{Name: iface_neighbor_dut, PrevHopLink: curhop}

	curhop.OutIface = Iface{Name: fmt.Sprintf("%v.%v", iface_name, subiface), IP: iface_ip}

	return nexthop_dut, nexthop_router
}

// Get routers on path
func tracePing(t *testing.T, src_dut *ondatra.DUTDevice, dest_ip string) {
	global_output.TracedIP = dest_ip
	iface_ips := getAllIfaces(t)
	dut := src_dut
	info := OnPathRouter{Name: src_dut.Name()}
	path := []OnPathRouter{info}
	path_ids := map[string]bool{src_dut.Name(): true} // to detect loop
	i := 1
	for dut != nil {
		dut, info = nexthop_dut(t, dut, &path[i-1], dest_ip, iface_ips)
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

	global_output.Paths = append(global_output.Paths, path)
}

// Needs to come last
func TestWriteOutput(t *testing.T) {
	b, err := json.Marshal(global_output)
	if err != nil {
		fmt.Printf("Error marshaling output: %v\n", err)
	}
	if err := os.WriteFile(outfile, b, 0666); err != nil {
		t.Fatalf("Error writing output: %v\n", err)
	}
}

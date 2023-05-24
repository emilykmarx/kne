package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"reflect"
	"strings"

	topopb "github.com/openconfig/kne/proto/topo"
	testbedpb "github.com/openconfig/ondatra/proto"
	"google.golang.org/protobuf/encoding/prototext"
)

const network_instance = "DEFAULT" // Should get this more programmatically (should also not hard-code it in template cfgs)
const dut_name_prefix = "srl"
const topo_filename = "wtf_topo.pbtxt"
const iface_name = "ethernet-1/%v"
const iface_key = "e1-%v"
const template_ip = "192.168.0.X"
const template_iface = "ethernet-1/Y"
const template_grp = "grp-X"

// Input path for graph & template configs, output path for all generated files
// (This package is expected to be run from kne root)
const Filepath = "go_expts/%v"

var loop_undo_filename = "loop_undo_" + dut_name_prefix + "%d" + "_src_" + dut_name_prefix + "%d.cfg"

// EASYTODO for files shared b/w this and other programs (py, ondatra, .sh): get names programmatically

var topo_services = map[uint32]*topopb.Service{
	22: {
		Name:   "ssh",
		Inside: 22,
	},
	9337: {
		Name:   "gnoi",
		Inside: 57400,
	},
	9339: {
		Name:   "gnmi",
		Inside: 57400,
	},
	9340: {
		Name:   "gribi",
		Inside: 57401,
	},
	9559: {
		Name:   "p4rt",
		Inside: 9559,
	},
}

type NxGraph struct {
	_     bool
	_     bool
	_     any
	Nodes []map[string]int
	Links []map[string]int
}

type TopoGraph struct {
	Graph NxGraph
	Paths map[int][]int
}

type Iface struct {
	Name string
	IP   string
}

// Return the interface part of the config
func ifaceConfig(id int, iface_ips map[int]map[int]Iface) []string {
	b, err := os.ReadFile(fmt.Sprintf(Filepath, "template_ifaces.cfg"))
	if err != nil {
		log.Fatalln(err)
	}
	iface_template_config_lines := strings.Split(string(b), "\n")
	all_iface_config_lines := []string{}
	for _, iface := range iface_ips[id] {
		// need explicit copy for slice; must alloc enough room
		iface_config_lines := make([]string, len(iface_template_config_lines))
		copy(iface_config_lines, iface_template_config_lines)
		for lineno := range iface_config_lines {
			iface_config_lines[lineno] = strings.ReplaceAll(iface_config_lines[lineno], template_iface, iface.Name)
			iface_config_lines[lineno] = strings.ReplaceAll(iface_config_lines[lineno], template_ip, iface.IP)
		}
		all_iface_config_lines = append(all_iface_config_lines, iface_config_lines...)
	}
	return all_iface_config_lines
}

// Set id's route to dest_ip using nexthop_id
// (id and nexthop_id should have a direct link)
// Return the static route part of the config
func staticRouteConfig(id int, nexthop_id int, iface_ips map[int]map[int]Iface, dest_ip string) []string {
	b, err := os.ReadFile(fmt.Sprintf(Filepath, "template_static_routes.cfg"))
	if err != nil {
		log.Fatalln(err)
	}
	route_config_lines := strings.Split(string(b), "\n")
	for lineno := range route_config_lines {
		// Each nexthop needs its own group name (will end up writing same group name multiple times if used in multiple paths; ok)
		route_config_lines[lineno] = strings.ReplaceAll(route_config_lines[lineno], template_grp, fmt.Sprintf("grp-%v", nexthop_id))
		if strings.Contains(route_config_lines[lineno], "route") {
			route_config_lines[lineno] = strings.ReplaceAll(route_config_lines[lineno], template_ip, dest_ip)
		} else if strings.Contains(route_config_lines[lineno], "nexthop") {
			// set next-hop to other endpoint's interface
			next_hop := iface_ips[nexthop_id][id].IP
			route_config_lines[lineno] = strings.ReplaceAll(route_config_lines[lineno], template_ip, next_hop)
		}
	}
	return route_config_lines
}

// Just since it's nice to put the loop in the longest path
// Iteration order over paths may not be deterministic, so only call this once
func loopedPath(paths map[int][]int) []int {
	var longest_path []int
	longest_path_len := -1

	for _, path := range paths {
		if len(path) > longest_path_len {
			longest_path = path
			longest_path_len = len(path)
		}
	}

	return longest_path
}

// Should do this configuration with gNMI/gNOI/gRIBI instead of Nokia commands
func writeConfigFiles(paths map[int][]int, topo_nodes []*topopb.Node, iface_ips map[int]map[int]Iface, loop bool) {
	// 1. Static routes
	// Only do paths to & from one dest for now
	route_config_lines := make([][]string, len(topo_nodes))
	looped_path := loopedPath(paths)
	for _, path := range paths {
		// Direct and local routes are auto-installed
		if len(path) < 3 {
			continue
		}
		dest_ip := iface_ips[path[len(path)-1]][path[len(path)-2]].IP
		src_ip := iface_ips[path[0]][path[1]].IP

		if loop && reflect.DeepEqual(path, looped_path) {
			// Put a loop in the longest path, from 2nd-to-last router to 3rd-to-last
			// Note ping from a router to its neighbor still works even if they're part of a loop,
			// since there's no routing	between neighbors
			if len(path) < 3 {
				log.Fatalf("Make paths longer (loop is nicer for paths of len at least 3)")
			}
			looped_router := path[len(path)-2]
			src_router := path[0]
			route_config_lines[looped_router] = append(route_config_lines[looped_router],
				staticRouteConfig(looped_router, path[len(path)-3], iface_ips, dest_ip)...)
			// Output cfg that can be pushed later to undo loop; filename indicates where it should be pushed
			loop_undo_cmd := fmt.Sprintf("set / network-instance %v static-routes route %v/32 admin-state disable",
				network_instance, dest_ip)

			full_loop_undo_filename := fmt.Sprintf(loop_undo_filename, looped_router, src_router)
			err := os.WriteFile(fmt.Sprintf(Filepath, full_loop_undo_filename), []byte(loop_undo_cmd), 0644)
			if err != nil {
				log.Fatalln(err)
			}
		}

		for pathidx, id := range path {
			if pathidx < len(path)-2 {
				// Route to dest
				route_config_lines[id] = append(route_config_lines[id], staticRouteConfig(id, path[pathidx+1], iface_ips, dest_ip)...)
			}
			if pathidx > 1 {
				// Route from dest
				route_config_lines[id] = append(route_config_lines[id], staticRouteConfig(id, path[pathidx-1], iface_ips, src_ip)...)
			}
		}
	}

	// 2. Interfaces & general Nokia config
	for id := range topo_nodes {
		// All ifaces need IPs, even if not used in any shortest path
		b, err := os.ReadFile("examples/nokia/srlinux-services/config.cfg")
		if err != nil {
			log.Fatalln(err)
		}
		config_lines := strings.Split(string(b), "\n")
		config_lines = append(config_lines, route_config_lines[id]...)
		config_lines = append(config_lines, ifaceConfig(id, iface_ips)...)

		// EASYTODO: Delete configs once pushed to routers
		output := strings.Join(config_lines, "\n")
		// Relative to this directory
		config_file := fmt.Sprintf("%v.cfg", topo_nodes[id].Name)
		// Relative to go root
		err = os.WriteFile(fmt.Sprintf(Filepath, config_file), []byte(output), 0644)
		if err != nil {
			log.Fatalln(err)
		}

		topo_nodes[id].Config.ConfigData = &topopb.Config_File{
			File: config_file,
		}
	}
}

func gen_routers(topo_graph TopoGraph) ([]*topopb.Node, []*testbedpb.Device) {
	topo_nodes := []*topopb.Node{}
	testbed_nodes := []*testbedpb.Device{}
	for _, n := range topo_graph.Graph.Nodes {
		topo_name := fmt.Sprintf("%v%v", dut_name_prefix, n["id"])
		topo_node := topopb.Node{
			Name:   topo_name,
			Vendor: topopb.Vendor_NOKIA,
			Model:  "ixr6e",
			Config: &topopb.Config{
				Image: "ghcr.io/nokia/srlinux:23.3.1",
			},
			Services:   topo_services,
			Interfaces: map[string]*topopb.Interface{},
		}
		topo_nodes = append(topo_nodes, &topo_node)

		testbed_device := testbedpb.Device{
			Id:     topo_name + "_id", // makes it clearer that a topo node is mapped to a testbed node
			Vendor: testbedpb.Device_NOKIA,
		}

		testbed_nodes = append(testbed_nodes, &testbed_device)
	}

	return topo_nodes, testbed_nodes
}

func gen_links(topo_graph TopoGraph, iface_ips map[int]map[int]Iface, topo_nodes []*topopb.Node, testbed_nodes []*testbedpb.Device) ([]*topopb.Link, []*testbedpb.Link) {
	ip := netip.MustParseAddr("192.168.0.0")
	topo_links := []*topopb.Link{}
	testbed_links := []*testbedpb.Link{}
	for _, l := range topo_graph.Graph.Links {
		// Create ifaces on both sides (in topo/testbed and router config)
		// Create link using newly created ifaces
		topo_link := topopb.Link{}
		testbed_link := testbedpb.Link{}
		// direction doesn't matter
		endpoint_ids := []int{l["source"], l["target"]}

		for i, id := range endpoint_ids {
			iface_id := len(topo_nodes[id].Interfaces) + 1 // iface 0 is verboten
			full_iface_name := fmt.Sprintf(iface_name, iface_id)
			full_iface_key := fmt.Sprintf(iface_key, iface_id)
			topo_nodes[id].Interfaces[full_iface_key] =
				&topopb.Interface{Name: full_iface_name}

			// Testbed ifaces can only have letter/#/_
			testbed_port := fmt.Sprintf("port%v", iface_id)
			testbed_nodes[id].Ports = append(testbed_nodes[id].Ports, &testbedpb.Port{Id: testbed_port})

			testbed_link_endpoint := fmt.Sprintf("%v:%v", testbed_nodes[id].Id, testbed_port)
			// Topo link interfaces must match node interface key
			if i == 0 {
				topo_link.ANode = topo_nodes[id].Name
				topo_link.AInt = full_iface_key
				testbed_link.A = testbed_link_endpoint
				// Smaller IP in link must have even last octet to put endpoints in same /31
				iface_ips[id][endpoint_ids[1]] = Iface{Name: full_iface_name, IP: ip.String()}
			} else {
				topo_link.ZNode = topo_nodes[id].Name
				topo_link.ZInt = full_iface_key
				testbed_link.B = testbed_link_endpoint
				iface_ips[id][endpoint_ids[0]] = Iface{Name: full_iface_name, IP: ip.String()}
			}

			if ip.Next() == netip.MustParseAddr("192.168.1.0") {
				// Just support 255 addrs for now - could easily extend
				log.Fatalf("Only support 192.168.0.X")
			}
			ip = ip.Next()
		}

		topo_links = append(topo_links, &topo_link)
		testbed_links = append(testbed_links, &testbed_link)
	}

	return topo_links, testbed_links
}

/* NOTE: Ondatra maps topo nodes to testbed nodes arbitrarily (and non-deterministically), so
 * shouldn't assume that e.g. "srl0" in testbed is "srl0" here
 * (i.e. don't output info here indexed by srl ID) */
func main() {
	loop := flag.Bool("loop", false, "Configure a route loop")
	flag.Parse()

	b, err := os.ReadFile(fmt.Sprintf(Filepath, "topo.json"))
	if err != nil {
		log.Fatalf("Failed to read generated topo graph %v\n", err)
	}
	topo_graph := &TopoGraph{}
	if err := json.Unmarshal(b, topo_graph); err != nil {
		log.Fatalf("Failed to unmarshal generated topo graph %v\n", err)
	}

	// 1. Create the routers (will fill in interfaces and configs later)
	// Router's index in list is its id (e.g. srl0)
	topo_nodes, testbed_nodes := gen_routers(*topo_graph)

	// 2. Create the links and fill in the interfaces

	// Record IPs of ifaces for use in writing configs
	// node ID => other nodeID, iface name & IP
	iface_ips := map[int]map[int]Iface{}
	for id := range topo_nodes {
		// prevent nil maps
		iface_ips[id] = map[int]Iface{}
	}
	topo_links, testbed_links := gen_links(*topo_graph, iface_ips, topo_nodes, testbed_nodes)

	fmt.Printf("iface ips: \n%v\n", iface_ips)
	writeConfigFiles(topo_graph.Paths, topo_nodes, iface_ips, *loop)

	topo := &topopb.Topology{
		Name:  "wtf", // will be the kubes namespace
		Nodes: topo_nodes,
		Links: topo_links,
	}
	testbed := &testbedpb.Testbed{
		Duts:  testbed_nodes,
		Links: testbed_links,
	}

	topo_bytes, err := prototext.Marshal(topo)
	if err != nil {
		fmt.Printf("Error marshaling topo: %v\n", err)
	}
	// needs to be in same dir as config file (I assume)
	if err := os.WriteFile(fmt.Sprintf(Filepath, topo_filename), topo_bytes, 0666); err != nil {
		log.Fatalf("Error writing topo file: %v\n", err)
	}
	testbed_bytes, err := prototext.Marshal(testbed)
	if err != nil {
		log.Fatalf("Error marshaling testbed: %v\n", err)
	}
	if err := os.WriteFile(fmt.Sprintf(Filepath, "wtf_testbed.textproto"), testbed_bytes, 0666); err != nil {
		log.Fatalf("Error writing testbed file: %v\n", err)
	}
}

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

// Should get all these more programmatically (should also not hard-code in template cfgs)
const network_instance = "DEFAULT"
const dut_name_prefix = "srl"
const iface_name = "ethernet-1/%v"
const iface_key = "e1-%v"
const template_ip = "192.168.0.X"
const template_prefix = "X.X.X.X/X"
const template_iface = "ethernet-1/Y"
const template_grp = "grp-X"
const ttl_acl = "wtf_ttl_filter"

var topo_filename = os.Getenv("WTF_TOPOFILE")

// Note this package is expected to be run from kne root

// Path for generated files, i.e. input path for graph, output path for all generated files except egress (since outside egress build context)
var kne_workdir = os.Getenv("WTF_KNE_WORKDIR")
var Genpath = kne_workdir + "/out/%v"
var Egressgenpath = kne_workdir + "/egress/out/%v"

// Filenames indicate routers involved in looped path
var loop_create_filename = "loop_create_" + dut_name_prefix + "%d" + "_src_" + dut_name_prefix + "%d.cfg"
var loop_undo_filename = "loop_undo_" + dut_name_prefix + "%d" + "_src_" + dut_name_prefix + "%d.cfg"

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

func egressIfaceConfig(id int, iface_ips map[int]map[int]Iface) []string {
	all_iface_config_lines := []string{}
	for _, iface := range iface_ips[id] {
		all_iface_config_lines = append(all_iface_config_lines, fmt.Sprintf("ip addr add %v/31 dev %v", iface.IP, ifaceKey(iface.Name)))
	}
	return all_iface_config_lines
}

// Return the interface part of the config
func ifaceConfig(id int, iface_ips map[int]map[int]Iface) []string {
	b, err := os.ReadFile(kne_workdir + "/template_ifaces.cfg")
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

// Return the ACL part of the config.
// Creates counter incremented per-subiface when sending a TTL expire ICMP msg
// (E.g. when dropping ping due to loop, if not original sender of ping)
func aclConfig(id int, iface_ips map[int]map[int]Iface) []string {
	b, err := os.ReadFile(kne_workdir + "/template_ttl_acls.cfg")
	if err != nil {
		log.Fatalln(err)
	}
	acl_template_config_lines := strings.Split(string(b), "\n")
	// last line is empty
	acl_face_line := strings.Clone(acl_template_config_lines[len(acl_template_config_lines)-2])
	// Slice the part that creates the filter
	all_acl_config_lines := acl_template_config_lines[0 : len(acl_template_config_lines)-2]
	// Attach ACL to each iface
	for _, iface := range iface_ips[id] {
		acl_iface_line := strings.ReplaceAll(acl_face_line, template_iface, iface.Name)
		all_acl_config_lines = append(all_acl_config_lines, acl_iface_line)
	}

	return all_acl_config_lines
}

// e.g. ethernet1-1 => e1-1
func ifaceKey(name string) string {
	var iface_id int
	if _, err := fmt.Sscanf(name, iface_name, &iface_id); err != nil {
		log.Fatalln(err)
	}
	return fmt.Sprintf(iface_key, iface_id)
}

// EASYTODO make host configuration persistent w/ netplan (NAT is, but ifaces aren't)
// Set egress's route to dest_prefix using router_id
// (egress and router_id should have a direct link)
func egressRouteConfig(id int, router_id int, iface_ips map[int]map[int]Iface, dest_prefix string) string {
	// set next-hop to other endpoint's interface
	next_hop := iface_ips[router_id][id].IP
	egress_iface := iface_ips[id][router_id]

	route := fmt.Sprintf("ip route add %v via %v dev %v src %v", dest_prefix, next_hop, ifaceKey(egress_iface.Name), egress_iface.IP)
	return route
}

// Set id's route to dest_prefix using nexthop_id
// (id and nexthop_id should have a direct link)
// Note: host bits must be 0 (for Nokia)
func staticRouteConfig(id int, nexthop_id int, iface_ips map[int]map[int]Iface, dest_prefix string) []string {
	b, err := os.ReadFile(kne_workdir + "/template_static_routes.cfg")
	if err != nil {
		log.Fatalln(err)
	}
	route_config_lines := strings.Split(string(b), "\n")
	for lineno := range route_config_lines {
		// Each nexthop needs its own group name (will end up writing same group name multiple times if used in multiple paths; ok)
		route_config_lines[lineno] = strings.ReplaceAll(route_config_lines[lineno], template_grp, fmt.Sprintf("grp-%v", nexthop_id))
		if strings.Contains(route_config_lines[lineno], "route") {
			route_config_lines[lineno] = strings.ReplaceAll(route_config_lines[lineno], template_prefix, dest_prefix)
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
	egress_config_lines := []string{}
	// 1. Static routes
	// The only routes configured are:
	// Default to egress, for all routers
	// Meaning:
	// - All routers can reach the egress and external things routable via the egress
	// - All routers on same path to egress can reach each other
	route_config_lines := make([][]string, len(topo_nodes))
	looped_path := loopedPath(paths)
	for _, path := range paths {
		// Direct and local routes are auto-installed, but need default for all other than egress
		if len(path) < 2 {
			continue
		}
		default_prefix := "0.0.0.0/0"
		src_prefix := iface_ips[path[0]][path[1]].IP + "/32"
		var p_looped_router *int
		var loop_src_router int // src router of looped path

		if loop && reflect.DeepEqual(path, looped_path) {
			// Output a config that, when pushed, puts a loop in the longest path to egress, from last router.
			// Note ping from a router to its neighbor still works even if they're part of a loop,
			// since there's no routing	between neighbors
			if len(path) < 3 {
				log.Fatalf("Make paths longer (loop is nicer for paths of len at least 3)")
			}
			p_looped_router = &path[len(path)-2]
			loop_src_router := path[0]
			loop_create_cfg := staticRouteConfig(*p_looped_router, path[len(path)-3], iface_ips, default_prefix)
			full_loop_create_filename := fmt.Sprintf(loop_create_filename, *p_looped_router, loop_src_router)
			err := os.WriteFile(fmt.Sprintf(Genpath, full_loop_create_filename),
				[]byte(strings.Join(loop_create_cfg, "\n")), 0644)
			if err != nil {
				log.Fatalln(err)
			}
		}

		// Some of this cfg will be output multiple times; ok
		for pathidx, id := range path {
			egress_id := path[len(path)-1]
			last_router_id := path[len(path)-2]
			if id != egress_id {
				next_id := path[pathidx+1]
				// Router's default route
				default_cfg := staticRouteConfig(id, next_id, iface_ips, default_prefix)
				if p_looped_router != nil && id == *p_looped_router {
					full_loop_undo_filename := fmt.Sprintf(loop_undo_filename, *p_looped_router, loop_src_router)
					err := os.WriteFile(fmt.Sprintf(Genpath, full_loop_undo_filename), []byte(strings.Join(default_cfg, "\n")), 0644)
					if err != nil {
						log.Fatalln(err)
					}
				}
				route_config_lines[id] = append(route_config_lines[id], default_cfg...)
				// All downstream (towards egress) indirect routers to this router
				if len(path)-2 > pathidx+1 {
					my_prefix := iface_ips[id][next_id].IP + "/32"
					for ds_idx, ds := range path[pathidx+2 : len(path)-1] {
						ds_path_idx := ds_idx + 2 + pathidx
						prev_hop := path[ds_path_idx-1]
						route_config_lines[ds] = append(route_config_lines[ds], staticRouteConfig(ds, prev_hop, iface_ips, my_prefix)...)
					}
				}
				if pathidx < len(path)-2 {
					// Egress's route to router
					egress_config_lines = append(egress_config_lines, egressRouteConfig(egress_id, last_router_id, iface_ips, src_prefix))
				}
			}
		}
	}

	// 2. Interfaces, ACLs, & general Nokia config
	for id, n := range topo_nodes {
		if n.Vendor == topopb.Vendor_HOST {
			// For egress: Must configure ifaces before routes
			config_lines := append(egressIfaceConfig(id, iface_ips), egress_config_lines...)
			output := strings.Join(config_lines, "\n")

			// Relative to go root
			err := os.WriteFile(fmt.Sprintf(Egressgenpath, "egress_setup_ifaces.sh"), []byte(output), 0644)
			if err != nil {
				log.Fatalln(err)
			}
			continue
		}
		// All ifaces need IPs, even if not used in any shortest path
		b, err := os.ReadFile("examples/nokia/srlinux-services/config.cfg")
		if err != nil {
			log.Fatalln(err)
		}
		config_lines := strings.Split(string(b), "\n")
		config_lines = append(config_lines, route_config_lines[id]...)
		config_lines = append(config_lines, ifaceConfig(id, iface_ips)...)
		config_lines = append(config_lines, aclConfig(id, iface_ips)...)

		output := strings.Join(config_lines, "\n")
		// Relative to this directory
		config_file := fmt.Sprintf("%v.cfg", topo_nodes[id].Name)
		// Relative to go root
		err = os.WriteFile(fmt.Sprintf(Genpath, config_file), []byte(output), 0644)
		if err != nil {
			log.Fatalln(err)
		}

		topo_nodes[id].Config.ConfigData = &topopb.Config_File{
			File: config_file,
		}
	}
}

func gen_routers(topo_graph TopoGraph, egress_id int) ([]*topopb.Node, []*testbedpb.Device) {
	topo_nodes := []*topopb.Node{}
	testbed_nodes := []*testbedpb.Device{}
	for _, n := range topo_graph.Graph.Nodes {
		id := n["id"]
		if id == egress_id {
			topo_node := topopb.Node{
				Name:   "egress",
				Vendor: topopb.Vendor_HOST,
				Config: &topopb.Config{
					Image:   "egress:latest",
					Command: []string{"./setup_nat_ifaces.sh"},
				},
				Services:   map[uint32]*topopb.Service{22: topo_services[22]},
				Interfaces: map[string]*topopb.Interface{},
			}
			topo_nodes = append(topo_nodes, &topo_node)
			continue
		}
		topo_name := fmt.Sprintf("%v%v", dut_name_prefix, id)
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

func gen_links(topo_graph TopoGraph, iface_ips map[int]map[int]Iface, topo_nodes []*topopb.Node, testbed_nodes []*testbedpb.Device,
) ([]*topopb.Link, []*testbedpb.Link) {
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
		need_testbed_link := true
		for _, id := range endpoint_ids {
			// Egress isn't part of testbed
			if topo_nodes[id].Vendor == topopb.Vendor_HOST {
				need_testbed_link = false
			}
		}

		for i, id := range endpoint_ids {
			iface_id := len(topo_nodes[id].Interfaces) + 1 // iface 0 is verboten
			full_iface_name := fmt.Sprintf(iface_name, iface_id)
			full_iface_key := fmt.Sprintf(iface_key, iface_id)
			topo_nodes[id].Interfaces[full_iface_key] =
				&topopb.Interface{Name: full_iface_name}

			testbed_link_endpoint := ""
			if need_testbed_link {
				// Testbed ifaces can only have letter/#/_
				testbed_port := fmt.Sprintf("port%v", iface_id)
				testbed_nodes[id].Ports = append(testbed_nodes[id].Ports, &testbedpb.Port{Id: testbed_port})
				testbed_link_endpoint = fmt.Sprintf("%v:%v", testbed_nodes[id].Id, testbed_port)
			}
			// Topo link interfaces must match node interface key
			if i == 0 {
				topo_link.ANode = topo_nodes[id].Name
				topo_link.AInt = full_iface_key
				if need_testbed_link {
					testbed_link.A = testbed_link_endpoint
				}
				// Smaller IP in link must have even last octet to put endpoints in same /31
				iface_ips[id][endpoint_ids[1]] = Iface{Name: full_iface_name, IP: ip.String()}
			} else {
				topo_link.ZNode = topo_nodes[id].Name
				topo_link.ZInt = full_iface_key
				if need_testbed_link {
					testbed_link.B = testbed_link_endpoint
				}
				iface_ips[id][endpoint_ids[0]] = Iface{Name: full_iface_name, IP: ip.String()}
			}

			if ip.Next() == netip.MustParseAddr("192.168.1.0") {
				// Just support 255 addrs for now - could easily extend
				log.Fatalf("Only support 192.168.0.X")
			}
			ip = ip.Next()
		}

		topo_links = append(topo_links, &topo_link)
		if need_testbed_link {
			testbed_links = append(testbed_links, &testbed_link)
		}
	}

	return topo_links, testbed_links
}

/* NOTE: Ondatra maps topo nodes to testbed nodes arbitrarily (and non-deterministically), so
 * shouldn't assume that e.g. "srl0" in testbed is "srl0" here
 * (i.e. don't output info here indexed by srl ID) */
func main() {
	loop := flag.Bool("loop", false, "Configure a route loop")
	flag.Parse()

	b, err := os.ReadFile(fmt.Sprintf(Genpath, os.Getenv("WTF_TOPOGRAPH")))
	if err != nil {
		log.Fatalf("Failed to read generated topo graph %v\n", err)
	}
	topo_graph := &TopoGraph{}
	if err := json.Unmarshal(b, topo_graph); err != nil {
		log.Fatalf("Failed to unmarshal generated topo graph %v\n", err)
	}

	// 1. Create the routers (will fill in interfaces and configs later)
	// Router's index in list is its id (e.g. srl0)
	path := topo_graph.Paths[0] // any path; they all have the same dest
	egress_id := path[len(path)-1]
	topo_nodes, testbed_nodes := gen_routers(*topo_graph, egress_id)

	// 2. Create the links and fill in the interfaces

	// Record IPs of ifaces for use in writing configs
	// node ID => other nodeID, iface name & IP used for other
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

	topo_bytes, err := prototext.MarshalOptions{Multiline: true}.Marshal(topo)
	if err != nil {
		fmt.Printf("Error marshaling topo: %v\n", err)
	}
	// needs to be in same dir as config file (I assume)
	if err := os.WriteFile(fmt.Sprintf(Genpath, topo_filename), topo_bytes, 0666); err != nil {
		log.Fatalf("Error writing topo file: %v\n", err)
	}
	testbed_bytes, err := prototext.MarshalOptions{Multiline: true}.Marshal(testbed)
	if err != nil {
		log.Fatalf("Error marshaling testbed: %v\n", err)
	}
	if err := os.WriteFile(fmt.Sprintf(Genpath, os.Getenv("WTF_TESTBEDFILE")), testbed_bytes, 0666); err != nil {
		log.Fatalf("Error writing testbed file: %v\n", err)
	}
}

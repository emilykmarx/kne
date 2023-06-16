#!/usr/bin/env python3
"""
"""

import argparse
import subprocess
import networkx as nx
import matplotlib.pyplot as plt
import random
import re
import json

test_outfile = "test_out.json"

def trace_path(test_out, path, path_no, request):
  G = nx.DiGraph()
  unhealthy_nodes = set()
  scoped_nodes = [] # ordered
  labels = {} # node ID => label

  for i, on_path_router in enumerate(path):
    node_id = on_path_router['Name']
    scoped_nodes.append(node_id)
    node_iface = on_path_router['InIface']['Name'] + '\n' + on_path_router['InIface']['IP']
    labels[node_id] = node_id

    if i > 0:
      prev_router = path[i - 1]
      prev_node_id = prev_router['Name']
      prev_iface = prev_router['OutIface']['Name'] + '\n' + prev_router['OutIface']['IP']
      G.add_edge(prev_node_id, node_id, label=f'{prev_iface}\n=>\n{node_iface}')

  for router in test_out['AllRouters']:
    node_id = router['Name']
    if node_id in scoped_nodes: # Path is ordered in json
      interval = float(1/len(path))
      i = scoped_nodes.index(node_id)
      x_pos = random.uniform(i * interval, (i+1) * interval)
      y_pos = random.uniform(interval, 2 * interval)
      pos = (x_pos, y_pos)
    else:
      pos = (random.random(), random.random())

    G.add_node(node_id, pos=pos)
    if router['BadIfaces'] is not None:
      unhealthy_nodes.add(node_id)
      if node_id in labels: # on path => label has name
        labels[node_id] += '\nBad:'
      else:
        labels[node_id] = 'Bad:'
      for bad_iface in router['BadIfaces']:
        # Label bad ifaces whether on path or not, to emphasize bits are per-iface
        labels[node_id] += f'\n{bad_iface}'

  colors = ['tab:red' if node in unhealthy_nodes else 'gray' for node in G.nodes()]
  sizes = [300 if node in scoped_nodes else 30 for node in G.nodes()]

  traced_ip = request['TracedIP']
  src_router = path[0]['Name']
  plt.figure(figsize=(10,10))
  plt.title(f'KNE: Trace {src_router} => {traced_ip}, path #{path_no}')
  pos=nx.get_node_attributes(G, 'pos')
  nx.draw_networkx_nodes(G, node_color=colors, node_size=sizes, pos=pos)
  nx.draw_networkx_labels(G, labels=labels, pos=pos, font_size=8)
  edge_labels = {}
  for edge in G.edges(data=True):
    edge_labels[(edge[0], edge[1])] = edge[2]["label"]

  nx.draw_networkx_edges(G, pos=pos, connectionstyle=f'arc3, rad = 0.1')
  # Label is close to the start of the arrow (helps disambiguate overlapping edges; would be better to change y pos but don't know how...)
  nx.draw_networkx_edge_labels(G, pos=pos, edge_labels=edge_labels, font_size=8, label_pos=0.8)
  # Not putting request ID in title to emphasize network scoping doesn't use it,
  # but use to disambiguate if 2 traced requests have the same params
  request_id = request['RequestID']
  plt.savefig(f'kne_trace_{src_router}_{traced_ip}_{path_no}_{request_id}.png')
  #plt.show()


def print_trace():
  with open(test_outfile) as f:
    test_out = json.load(f)

  for request in test_out['Requests']:
    for i, path in enumerate(request['Paths']):
      trace_path(test_out=test_out, path=path, path_no=i + 1, request=request)


def main():
  parser = argparse.ArgumentParser()
  args = parser.parse_args()

  print_trace()

if __name__ == "__main__":
  main()

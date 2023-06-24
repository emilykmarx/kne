#!/usr/bin/env python3
"""
"""

import argparse
import networkx as nx
import matplotlib.pyplot as plt
from itertools import combinations, groupby
import random
import json

def gnp_random_connected_graph(n, p, seed=None):
  random.seed(seed) # unseeded if seed=None
  edges = combinations(range(n), 2)
  G = nx.Graph()
  G.add_nodes_from(range(n))
  for _, node_edges in groupby(edges, key=lambda x: x[0]):
      node_edges = list(node_edges)
      random_edge = random.choice(node_edges)
      G.add_edge(*random_edge)
      for e in node_edges:
          if random.random() < p:
              G.add_edge(*e)
  return G

def main():
  parser = argparse.ArgumentParser()
  parser.add_argument("-n", "--nodes", required=True, type=int)
  parser.add_argument("-p", "--link-probability", required=True, type=float)
  parser.add_argument("-o", "--outfile", required=True)
  args = parser.parse_args()
  # Note draw is not seeded (maybe there's a way to do that)
  seed = 1
  G = gnp_random_connected_graph(args.nodes, args.link_probability, seed)

  plt.figure(figsize=(8,5))
  nx.draw(G, node_color='lightblue',
          with_labels=True,
          node_size=500)
  plt.savefig('topo.png')

  # {src: {dest: [path], ...}, ...}
  # paths = nx.shortest_path(G)
  # Only do paths to one dest (the one with largest ID) for now
  paths = nx.shortest_path(G, target=args.nodes-1)
  with open(args.outfile, "w") as f:
    f.write(f"{{\"Graph\": {json.dumps(nx.node_link_data(G))},\n")
    f.write(f"\"Paths\": {json.dumps(paths)}}}")

  #plt.show()



if __name__ == "__main__":
  main()

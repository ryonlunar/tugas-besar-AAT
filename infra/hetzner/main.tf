terraform {
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "= 1.70.2"
    }
  }
}

variable "run_id" { type = string }
variable "ssh_cidr" { type = string }
variable "ssh_public_key" { type = string }

locals {
  labels = { project = "aat-benchmark", run = var.run_id }
  nodes = {
    aat-dev      = { type = "ccx33", ip = "10.77.0.11", cluster = true }
    aat-worker-1 = { type = "cpx32", ip = "10.77.0.12", cluster = true }
    aat-worker-2 = { type = "ccx23", ip = "10.77.0.13", cluster = true }
    aat-worker-3 = { type = "cpx32", ip = "10.77.0.14", cluster = true }
    aat-loadgen  = { type = "cpx52", ip = "10.77.0.20", cluster = false }
  }
}

resource "hcloud_ssh_key" "benchmark" {
  name       = "aat-${var.run_id}"
  public_key = var.ssh_public_key
  labels     = local.labels
}

resource "hcloud_network" "benchmark" {
  name     = "aat-${var.run_id}"
  ip_range = "10.77.0.0/24"
  labels   = local.labels
}

resource "hcloud_network_subnet" "benchmark" {
  network_id   = hcloud_network.benchmark.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.77.0.0/25"
}

resource "hcloud_firewall" "benchmark" {
  name   = "aat-${var.run_id}"
  labels = local.labels

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = [var.ssh_cidr]
  }
}

resource "hcloud_placement_group" "cluster" {
  name   = "aat-${var.run_id}"
  type   = "spread"
  labels = local.labels
}

resource "hcloud_server" "node" {
  for_each           = local.nodes
  name               = "${each.key}-${var.run_id}"
  location           = "hel1"
  server_type        = each.value.type
  image              = "ubuntu-24.04"
  labels             = local.labels
  firewall_ids       = [hcloud_firewall.benchmark.id]
  ssh_keys           = [hcloud_ssh_key.benchmark.id]
  placement_group_id = each.value.cluster ? hcloud_placement_group.cluster.id : null

  public_net {
    ipv4_enabled = true
    ipv6_enabled = false
  }

  network {
    subnet_id = hcloud_network_subnet.benchmark.id
    ip        = each.value.ip
  }
}

output "nodes" {
  value = { for name, server in hcloud_server.node : name => {
    public_ipv4  = server.ipv4_address
    private_ipv4 = local.nodes[name].ip
    type         = local.nodes[name].type
    id           = server.id
  } }
}

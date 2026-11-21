terraform {
  required_providers {
    lab = {
      source  = "course/lab"
      version = "0.1.0"
    }
  }
}

provider "lab" {}

variable "public_key_file" {
  type        = string
  description = "Path to the deployment SSH public key."
  default     = "../.local/deploy.pub"
}

resource "lab_instance" "api" {
  name  = "csv-api"
  image = "ubuntu-26.04"
  user_data = "#cloud-config\n${yamlencode({
    users = ["default", {
      name                = "deployer"
      shell               = "/bin/bash"
      sudo                = "ALL=(ALL) NOPASSWD:ALL"
      ssh_authorized_keys = [trimspace(file(pathexpand(var.public_key_file)))]
    }]
    write_files = [{ path = "/etc/course-role", content = "csv-api\n" }]
  })}"
}

resource "lab_cluster" "workers" {
  name      = "csv-workers"
  min_nodes = 1
  max_nodes = 1
}

output "api_address" {
  value = lab_instance.api.ip
}

output "cluster" {
  value = lab_cluster.workers.name
}

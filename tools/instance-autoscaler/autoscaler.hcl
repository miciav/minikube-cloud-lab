plugin_dir = "/plugins"

http {
  bind_address = "127.0.0.1"
  bind_port    = 8085
}

policy {
  dir = "/autoscaling/policies"
  source "nomad" {
    enabled = false
  }
}

apm "prometheus" {
  driver = "prometheus"
  config = { address = "http://instance-metrics:9090" }
}

target "lab-groups" {
  driver = "lab-groups"
  config = { address = "http://127.0.0.1:8080" }
}

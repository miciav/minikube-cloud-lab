# Source from the minikube-cloud-lab checkout after scripts/configure.sh.
# The provider's kubectl is a shell function, so do not run this as a new shell.
lecture02_prepare_nodes() {
  local lecture02_workers lecture02_backend lecture02_frontend
  lecture02_workers=$(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}') || return 1
  lecture02_backend=$(printf '%s\n' "$lecture02_workers" | sed -n '1p')
  lecture02_frontend=$(printf '%s\n' "$lecture02_workers" | sed -n '2p')
  if [ -z "$lecture02_backend" ] || [ -z "$lecture02_frontend" ]; then
    printf '%s\n' 'This exercise needs two worker nodes. Use cloud clusters resize <name> --nodes 3 on an existing lab, or create lecture02 with --min-nodes 3 --max-nodes 3.' >&2
    return 1
  fi
  kubectl label node "$lecture02_backend" lecture02-role=backend --overwrite || return 1
  kubectl label node "$lecture02_frontend" lecture02-role=frontend --overwrite || return 1
  printf 'Backend: %s; client: %s\n' "$lecture02_backend" "$lecture02_frontend"
}
lecture02_prepare_nodes

#!/usr/bin/env bash
# Join the API instance to the three private service networks. Ansible uses the original lab interface.
set -euo pipefail
source "$(dirname "$0")/../../scripts/configure.sh"
# The working directory contains the capstone handout (or its disposable test copy).
test -f prepare.py
project=${COMPOSE_PROJECT_NAME:-mcl}
state="$HOME/.minikube-cloud-lab/services/$CLOUD_LAB_RUNTIME"
for service in object-storage queue database; do
    network="$project-${service}_default"
    networks=$("$CLOUD_LAB_RUNTIME" inspect --format '{{range $name, $config := .NetworkSettings.Networks}}{{println $name}}{{end}}' mcl-vm-csv-api)
    if ! grep -Fxq "$network" <<< "$networks"; then
        "$CLOUD_LAB_RUNTIME" network connect "$network" mcl-vm-csv-api
    fi
done
cat "$state/$project-object-storage/student" "$state/$project-database/student" | labtool python3 prepare.py

#!/usr/bin/env bash
# Run against a disposable lab with the cloud service already running.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
if ! declare -F lab >/dev/null; then
    source "$repo/scripts/configure.sh"
fi
base=${CLOUD_TEST_API_URL:-http://127.0.0.1:8080}
web=${CLOUD_TEST_LB_URL:-http://127.0.0.1:8084}/groups/web-smoke/
runtime=${CLOUD_LAB_RUNTIME:-docker}
manual=smoke-manual-$$
manual_created=
volume=
installed=
load_pid=

wait_for() {
    local n
    for ((n=0; n<180; n++)); do
        if "$@"; then return; fi
        sleep 1
    done
    printf 'Timed out waiting for %s\n' "$*" >&2
    return 1
}
cleanup() {
    if [ -n "$load_pid" ]; then kill "$load_pid" 2>/dev/null || true; fi
    if [ -n "$installed" ]; then lab uninstall instance-autoscaling || true; fi
    if [ -n "$manual_created" ]; then cloud instances delete "$manual" >/dev/null 2>&1 || true; fi
    if [ -n "$volume" ]; then cloud volumes delete "$volume" >/dev/null 2>&1 || true; fi
}
group() {
    curl -fsS "$base/v1/groups/web-smoke" | python3 -c "import json, sys; print(json.load(sys.stdin)[sys.argv[1]])" "$1"
}
capacity() { [ "$(group desired)" = "$1" ] && [ "$(group ready)" = "$1" ]; }
member() { curl -fsS --max-time 2 "$web" | tr -d '\r\n'; }
inventory_empty() {
    local rows
    rows=$("$runtime" "$@") && [ -z "$rows" ]
}
old_target_gone() {
    inventory_empty ps -aq --filter "label=minikube-cloud-lab.instance=$1" && member >/dev/null
}
all_removed() {
    inventory_empty ps -aq --filter "label=minikube-cloud-lab.instance-group=$1" &&
        inventory_empty volume ls -q --filter "label=minikube-cloud-lab.instance-group=$1"
}
operation_done() {
    local state
    state=$(curl -fsS "$base/v1/operations/$1" | python3 -c 'import json, sys; print(json.load(sys.stdin)["status"])') || return 1
    if [ "$state" = ERROR ]; then cloud operations describe "$1" >&2; exit 1; fi
    [ "$state" = DONE ]
}
module_disabled() {
    curl -fsS "$base/v1/instance-autoscaling" | python3 -c 'import json, sys; assert json.load(sys.stdin) == {"state":"disabled","remaining":[]}'
}

module_disabled
trap cleanup EXIT
manual_op=$(cloud instances create "$manual" --async)
manual_created=yes
wait_for operation_done "$manual_op"
volume=$(cloud volumes create --capacity 16Mi | awk '/^pvc-/ {print $1}')
test -n "$volume"
installed=yes
lab install instance-autoscaling
cloud groups create web-smoke --desired 2 --max 3 --target-rps 3 --bootstrap "$repo/lab/instance-autoscaling/web.sh"
wait_for capacity 2
id=$(group id)
member >/dev/null

while true; do curl -fsS --max-time 2 "$web" >/dev/null || true; sleep 0.06; done &
load_pid=$!
wait_for capacity 3
labcompose --profile instance-autoscaling stop instance-metrics
curl -fsS "$base/v1/instance-autoscaling/health" | python3 -c 'import json,sys; h=json.load(sys.stdin); assert h["state"]=="degraded" and "instance-metrics" in h["detail"]'
kill "$load_pid"
wait "$load_pid" 2>/dev/null || true
load_pid=
sleep 40
test "$(group desired)" = 3
labcompose --profile instance-autoscaling start instance-metrics
wait_for capacity 1

failed=$(member)
cloud instances ssh "$failed" -- sudo systemctl stop lab-group-http
wait_for old_target_gone "$failed"
wait_for capacity 1
test "$(member)" != "$failed"

stopped=$(member)
cloud instances stop "$stopped"
cloud groups create cleanup-smoke --desired 2 --max 2 --bootstrap - < "$repo/lab/instance-autoscaling/web.sh"
cleanup_id=$(curl -fsS "$base/v1/groups/cleanup-smoke" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
# Resume an uninstall whose caller disconnected after the cloud persisted its intent.
curl -fsS -X POST "$base/v1/instance-autoscaling/begin-removal" >/dev/null
lab uninstall instance-autoscaling
module_disabled
all_removed "$id"
all_removed "$cleanup_id"
for service in instance-lb instance-metrics instance-autoscaler; do
    inventory_empty ps -aq --filter "label=com.docker.compose.service=$service"
done
cloud instances describe "$manual"
cloud volumes list | grep -F "$volume"

lab install instance-autoscaling
test "$(curl -fsS "$base/v1/groups")" = "[]"
lab uninstall instance-autoscaling
module_disabled
installed=
printf 'PASS instance autoscaling lifecycle\n'

#!/usr/bin/env bash
# Run against a dedicated, empty course cloud. Preserve the workspace for diagnosis on failure.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
source "$root/scripts/configure.sh"
for resource in instances clusters; do
    curl -fsS "http://127.0.0.1:8080/v1/$resource" | labtool python3 -c 'import json,sys; assert not json.load(sys.stdin), "capstone needs an empty cloud"'
done
modules=(object-storage queue database)
for module in "${modules[@]}" monitoring portal; do
    [[ $(lab status "$module") == "$module disabled empty" ]] || { echo "Existing $module state" >&2; exit 1; }
done
work=$(mktemp -d)
echo "Capstone workspace: $work"
mkdir "$work/infrastructure"
cp "$root/lab/capstone/infrastructure/main.tf" "$work/infrastructure/"
cp -R "$root/lab/capstone/"{ansible,app,kubernetes,prepare.py} "$work/"
cd "$work"
for module in "${modules[@]}"; do lab install "$module"; done
lab install portal
lab install monitoring
mkdir -m 700 .local
labtool ssh-keygen -q -t ed25519 -N '' -f .local/deploy
tofu -chdir=infrastructure init
tofu -chdir=infrastructure apply -parallelism=1 -auto-approve
tofu -chdir=infrastructure plan -detailed-exitcode
bash "$root/lab/capstone/prepare.sh"
ansible-playbook -i .local/inventory.yml --private-key .local/deploy ansible/site.yml
ansible-playbook -i .local/inventory.yml --private-key .local/deploy ansible/site.yml | tee .local/repeat.log
grep -Eq 'changed=0[[:space:]]' .local/repeat.log
cloud clusters get-credentials csv-workers
kubectl create namespace csv-capstone
kubectl -n csv-capstone create secret generic csv-worker-access --from-env-file=.local/worker.env
kubectl -n csv-capstone apply -k kubernetes
kubectl -n csv-capstone rollout status deployment/csv-worker --timeout=5m
ip=$(tofu -chdir=infrastructure output -raw api_address)
history() { python3 "$root/tests/history-sample.py" "$@"; }
history cpu csv-workers
history memory csv-workers
kubectl -n csv-capstone scale deployment/csv-worker --replicas=0
kubectl -n csv-capstone wait --for=delete pod -l app=csv-worker --timeout=120s
lab uninstall portal
baseline=$(history objects course-files)
base_bytes=$(history bytes course-files)
queue=000000000000/capstone-jobs
history visible "$queue" --equals 0
printf 'quantity\n3\n5\n' > .local/input.csv
payload_bytes=$(wc -c < .local/input.csv)
jobs=()
for _ in {1..10}; do
    response=$(cloud instances ssh csv-api -- curl -fsS --data-binary @- "http://$ip:8090/jobs" < .local/input.csv)
    jobs+=("$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["job"])')")
done
history visible "$queue" --equals 10
history objects course-files --equals "$(python3 -c 'import sys; print(float(sys.argv[1])+10)' "$baseline")"
history bytes course-files --equals "$(python3 -c 'import sys; print(float(sys.argv[1])+10*int(sys.argv[2]))' "$base_bytes" "$payload_bytes")"
last_closed_sample=$(python3 -c 'import time; print(time.time())')
lab install portal
history visible "$queue" --portal --equals 10 --before "$last_closed_sample"
kubectl -n csv-capstone scale deployment/csv-worker --replicas=1
for job in "${jobs[@]}"; do
    cloud instances ssh csv-api -- curl --retry 25 --retry-all-errors --retry-delay 1 -fsS "http://$ip:8090/results/$job" |
        python3 -c 'import json,sys; assert json.load(sys.stdin) == {"total":8}'
done
history visible "$queue" --equals 0
lab uninstall portal
lab uninstall monitoring
lab purge monitoring --yes
kubectl delete namespace csv-capstone
tofu -chdir=infrastructure destroy -parallelism=1 -auto-approve
for module in "${modules[@]}"; do lab uninstall "$module"; lab purge "$module" --yes; done
cd "$root"
rm -rf "$work"
echo "PASS: cloud-init, OpenTofu, Ansible, kubectl, CSV total and teardown"

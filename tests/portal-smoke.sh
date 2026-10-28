#!/usr/bin/env bash
# Disposable image smoke: never mounts the operator's state or kubeconfig.
set -euo pipefail
cloud_image=${CLOUD_IMAGE:-minikube-cloud-lab/cloud:dev}
portal_image=${PORTAL_IMAGE:-minikube-cloud-lab/portal:dev}
project="portal-smoke-$$-$RANDOM"
network="$project-network"
volume="$project-data"
cleanup() {
    docker rm -f "$project-portal" "$project-cloud" >/dev/null 2>&1 || true
    docker volume rm "$volume" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT
socket_gid=$(docker run --rm -v /var/run/docker.sock:/var/run/docker.sock --entrypoint sh "$cloud_image" -c 'stat -c %g /var/run/docker.sock')
docker network create "$network" >/dev/null
docker volume create "$volume" >/dev/null
docker run -d --name "$project-cloud" --network "$network" --network-alias cloud \
    --group-add "$socket_gid" -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$volume:/data" "$cloud_image" serve --data-dir /data --api 0.0.0.0:8080 >/dev/null
docker run -d --name "$project-portal" --network "$network" \
    -p 127.0.0.1::3000 "$portal_image" >/dev/null
binding=$(docker port "$project-portal" 3000/tcp)
port=${binding##*:}
url="http://127.0.0.1:$port"
ready=false
for _ in {1..30}; do
    if body=$(curl --max-time 2 -fsS "$url/v1/clusters" 2>/dev/null); then
        ready=true
        break
    fi
    sleep 1
done
if [[ "$ready" != true ]]; then
    docker logs "$project-cloud"
    docker logs "$project-portal"
    exit 1
fi
printf '%s' "$body" | python3 -c 'import json,sys; assert json.load(sys.stdin)==[]'
html=$(curl --max-time 5 -fsS "$url/")
asset=$(printf '%s' "$html" | python3 -c 'import re,sys; html=sys.stdin.read(); assert "Cloud Lab" in html; print(re.search(r"<script[^>]+src=\"([^\"]+)\"",html).group(1))')
curl --max-time 5 -fsS "$url$asset" >/dev/null
status=$(curl --max-time 5 -sS -o /dev/null -w '%{http_code}' -H 'Origin: http://untrusted.example' "$url/v1/clusters")
[[ "$status" == 403 ]]
docker inspect "$project-portal" | python3 -c 'import json,sys; c=json.load(sys.stdin)[0]; assert c["Mounts"]==[]; assert not c["HostConfig"]["Privileged"]; assert c["NetworkSettings"]["Ports"]["3000/tcp"][0]["HostIp"]=="127.0.0.1"'
printf 'Portal image, static asset, cloud API and local access restrictions: PASS\n'

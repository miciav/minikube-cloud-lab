#!/usr/bin/env bash
# Feasibility probe for managed registries: one registry container aliased probe.registry on a
# throwaway network and two kicbase system containers started with the cloud's instance flags.
# Checks DNS, inner Docker push/pull over HTTP with a push user and a pull user, denial of push
# for the pull user, HTTP API listing and data size. Prints PASS/FAIL per check, exits non-zero
# on any FAIL and removes everything it creates.
set -euo pipefail
RUNTIME=${RUNTIME:-docker}
KICBASE_IMAGE=${KICBASE_IMAGE:-gcr.io/k8s-minikube/kicbase:v0.0.51@sha256:4a1c825b61479e6c898851ea66f13c620aaeab6002746e95067fc2c4b38a0b24}
ZOT_IMAGE=${ZOT_IMAGE:-ghcr.io/project-zot/zot:v2.1.23@sha256:9d3de83ac2099e6f4843045b4c9b65b7e8baf40f92d28f54906dee9d4d390fd7}
DISTRIBUTION_IMAGE=${DISTRIBUTION_IMAGE:-docker.io/library/registry:3.0.0@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e}
HTTPD_IMAGE=${HTTPD_IMAGE:-docker.io/library/httpd:2.4.71-alpine@sha256:32bd0639bcc4917ef1fd9363a1d9dd2dd9c498565118b062a10d51e43a5c3a24}
BUSYBOX_IMAGE=${BUSYBOX_IMAGE:-docker.io/library/busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec}
FEAS_SUBNET=${FEAS_SUBNET:-172.30.12.0/24}
net=mcl-registry-feasibility
base=${FEAS_SUBNET%.*/*}
ip_a=$base.200 ip_b=$base.201
reg=mcl-registry-probe data=mcl-registry-probe-data
host=probe.registry:5000
push_pw=push-secret pull_pw=pull-secret
case $RUNTIME in
docker) apparmor=(--security-opt apparmor=unconfined) exec_opt= ;;
podman) apparmor=() exec_opt=:exec ;;
*) echo "RUNTIME must be docker or podman" >&2; exit 2 ;;
esac

for n in mcl-vm-ra mcl-vm-rb $reg; do
    if $RUNTIME container inspect "$n" >/dev/null 2>&1; then
        echo "container $n already exists; remove it before running the probe" >&2; exit 2
    fi
done
for v in mcl-vm-ra mcl-vm-rb $data; do
    if $RUNTIME volume inspect "$v" >/dev/null 2>&1; then
        echo "volume $v already exists; remove it before running the probe" >&2; exit 2
    fi
done
if $RUNTIME network inspect "$net" >/dev/null 2>&1; then
    echo "network $net already exists; remove it before running the probe" >&2; exit 2
fi
conf=$(mktemp -d)
cleanup() {
    $RUNTIME rm -f mcl-vm-ra mcl-vm-rb "$reg" >/dev/null 2>&1 || true
    $RUNTIME volume rm -f mcl-vm-ra mcl-vm-rb "$data" >/dev/null 2>&1 || true
    $RUNTIME network rm "$net" >/dev/null 2>&1 || true
    rm -rf "$conf"
}
trap cleanup EXIT

failed=0
pass() { echo "PASS $1"; }
fail() { echo "FAIL $1${2:+: $2}"; failed=1; }
# check NAME CMD...: runs CMD, prints its output only on failure.
check() {
    local name=$1 out; shift
    if out=$("$@" 2>&1); then pass "$name"; else fail "$name" "$(tail -n 5 <<<"$out")"; fi
}
ex() { local n=$1; shift; $RUNTIME exec "mcl-vm-r$n" "$@"; }
exi() { local n=$1; shift; $RUNTIME exec -i "mcl-vm-r$n" "$@"; }
booted() {
    local state
    for _ in $(seq 60); do
        state=$(ex "$1" timeout 180 systemctl is-system-running --wait 2>&1 || true)
        case $state in offline | "Failed to connect to bus"*) sleep 1 ;; *) break ;; esac
    done
    case $state in running | degraded) ;; *) echo "systemd in $1: state '$state'" >&2; exit 1 ;; esac
}

$RUNTIME network create --subnet "$FEAS_SUBNET" --gateway "$base.1" "$net" >/dev/null
for pair in "a $ip_a" "b $ip_b"; do
    read -r n ip <<<"$pair"
    $RUNTIME volume create "mcl-vm-r$n" >/dev/null
    $RUNTIME run -d -t --name "mcl-vm-r$n" --hostname "r$n" --privileged \
        --security-opt seccomp=unconfined "${apparmor[@]}" \
        --tmpfs /tmp --tmpfs /run -v /lib/modules:/lib/modules:ro \
        -v "mcl-vm-r$n:/var$exec_opt" -e "container=$RUNTIME" \
        --cpu-period 100000 --cpu-quota 200000 --memory 2048m --memory-swap 2048m --pids-limit 4096 \
        --stop-signal SIGRTMIN+3 --network "$net" --ip "$ip" "$KICBASE_IMAGE" >/dev/null
done

# Users: push may read, create and update; pull may only read. Anonymous access is denied.
{
    $RUNTIME run --rm "$HTTPD_IMAGE" htpasswd -nbB push "$push_pw"
    $RUNTIME run --rm "$HTTPD_IMAGE" htpasswd -nbB pull "$pull_pw"
} | grep . >"$conf/htpasswd"
cat >"$conf/config.json" <<'EOF'
{
  "distSpecVersion": "1.1.1",
  "storage": {"rootDirectory": "/var/lib/registry"},
  "http": {
    "address": "0.0.0.0",
    "port": "5000",
    "compat": ["docker2s2"],
    "auth": {"htpasswd": {"path": "/etc/zot/htpasswd"}},
    "accessControl": {
      "repositories": {
        "**": {
          "policies": [
            {"users": ["push"], "actions": ["read", "create", "update"]},
            {"users": ["pull"], "actions": ["read"]}
          ],
          "defaultPolicy": []
        }
      }
    }
  },
  "log": {"level": "info"}
}
EOF
chmod 644 "$conf/htpasswd" "$conf/config.json"

start_registry() {
    $RUNTIME rm -f "$reg" >/dev/null 2>&1 || true
    $RUNTIME volume rm -f "$data" >/dev/null 2>&1 || true
    $RUNTIME run -d --name "$reg" --network "$net" --network-alias probe.registry \
        -v "$data:/var/lib/registry" "$@" >/dev/null
    for _ in $(seq 60); do
        [ "$(ex a curl -s -o /dev/null -w '%{http_code}' "http://$host/v2/" || true)" = 401 ] && return 0
        sleep 1
    done
    echo "registry did not answer 401 on /v2/"; $RUNTIME logs --tail 20 "$reg"
}
booted a
booted b
start_registry -v "$conf/config.json:/etc/zot/config.json:ro" -v "$conf/htpasswd:/etc/zot/htpasswd:ro" "$ZOT_IMAGE"
check "DNS of probe.registry from a kicbase container" ex a getent hosts probe.registry

# Trust the network's subnet for plain HTTP, keeping whatever daemon.json kicbase ships.
trust() {
    local cfg
    cfg=$(ex "$1" cat /etc/docker/daemon.json 2>/dev/null || echo '{}')
    jq --arg cidr "$FEAS_SUBNET" '."insecure-registries" = [$cidr]' <<<"${cfg:-"{}"}" |
        exi "$1" sh -c 'mkdir -p /etc/docker && cat >/etc/docker/daemon.json && systemctl restart docker'
    for _ in $(seq 30); do ex "$1" docker info >/dev/null 2>&1 && break; sleep 1; done
    ex "$1" docker info --format '{{json .RegistryConfig.InsecureRegistryCIDRs}}' | jq -e --arg cidr "$FEAS_SUBNET" 'index($cidr) != null'
}
check "inner Docker trusts $FEAS_SUBNET in a" trust a
check "inner Docker trusts $FEAS_SUBNET in b" trust b

login() { exi "$1" docker login -u "$2" --password-stdin "$host" <<<"$3"; }
push_image() { # VM USER PASSWORD REPO
    login "$1" "$2" "$3" &&
        ex "$1" sh -c "mkdir -p /tmp/img && printf 'FROM %s\nCMD [\"echo\", \"probe\"]\n' '$BUSYBOX_IMAGE' >/tmp/img/Dockerfile &&
            docker build -q -t '$host/$4:1' /tmp/img && docker push '$host/$4:1'"
}
probe_server() { # LABEL
    check "$1: login and push with push user over HTTP" push_image a push "$push_pw" probe/hello
    ex a curl -fsS -u "push:$push_pw" -o /dev/null -w '%{content_type}\n' \
        -H 'Accept: application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json' \
        "http://$host/v2/probe/hello/manifests/1" | sed 's/^/INFO pushed manifest type: /' || true
}
probe_server zot
pull() { login b pull "$pull_pw" && ex b docker pull "$host/probe/hello:1"; }
check "zot: pull with pull user from a second kicbase container" pull
# Login succeeds (pull is a valid user); only the push itself must be refused.
denied() { login b pull "$pull_pw" && ! push_image b pull "$pull_pw" probe/denied; }
check "zot: push with pull user is denied" denied
listing() {
    ex b curl -fsS -u "pull:$pull_pw" "http://$host/v2/_catalog" | jq -e '.repositories | index("probe/hello") != null and index("probe/denied") == null' &&
        ex b curl -fsS -u "pull:$pull_pw" "http://$host/v2/probe/hello/tags/list" | jq -e '.tags | index("1") != null'
}
check "zot: catalog and tags listed over HTTP with pull user" listing
size() {
    local kib
    kib=$($RUNTIME run --rm -v "$data:/data:ro" "$BUSYBOX_IMAGE" du -sk /data | cut -f1)
    echo "INFO data directory: $kib KiB"
    [ "$kib" -gt 0 ]
}
if out=$(size 2>&1); then pass "zot: data directory size measurable (${out#INFO data directory: })"; else fail "zot: data directory size measurable" "$out"; fi

if [ "$failed" != 0 ]; then
    # Distribution's htpasswd auth grants every authenticated user full access, so only the push
    # check is meaningful; per-user read-only access needs auth.token.
    start_registry -v "$conf/htpasswd:/auth/htpasswd:ro" -e REGISTRY_AUTH=htpasswd \
        -e REGISTRY_AUTH_HTPASSWD_REALM=probe -e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd "$DISTRIBUTION_IMAGE"
    probe_server distribution
    echo "INFO distribution: read-only pull users need token auth (auth.token realm/service/issuer/rootcertbundle)" \
        "and a token server issuing JWTs whose access claims grant [pull] or [pull,push] per repository"
fi

[ "$failed" = 0 ] && echo "ALL CHECKS PASSED"
exit "$failed"

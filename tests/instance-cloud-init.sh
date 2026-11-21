#!/usr/bin/env bash
# Run with the cloud and instance images built from the same checkout.
set -euo pipefail
: "${TEST_CLOUD_CONTAINER:?Set TEST_CLOUD_CONTAINER to the running cloud container ID}"
runtime=${RUNTIME:-docker}
cloud() { "$runtime" exec -i "$TEST_CLOUD_CONTAINER" cloud "$@"; }
image=${1:-ubuntu-26.04}
case "$image" in
    ubuntu-26.04) login=ubuntu ;;
    debian-12) login=docker ;;
    *) echo "Unsupported test image: $image" >&2; exit 2 ;;
esac
vm="init-test-$(date +%s)-$$"
scratch=$(mktemp -d)
cleanup() {
    cloud instances delete "$vm" </dev/null || true
    rm -rf "$scratch"
}
trap cleanup EXIT
ssh-keygen -q -t ed25519 -N '' -f "$scratch/key"
key=$(cat "$scratch/key.pub")
cat > "$scratch/config" <<CONFIG
#cloud-config
users:
  - default
  - name: learner
    ssh_authorized_keys:
      - $key
ssh_authorized_keys:
  - $key
chpasswd:
  expire: false
  users:
    - {name: learner, password: ephemeral-test-password, type: text}
package_update: true
packages: [nginx]
write_files:
  - {path: /var/www/html/version, content: cloud-init-ok}
runcmd:
  - [systemctl, enable, --now, nginx]
  - [sh, -c, 'echo once >> /var/tmp/cloud-init-count']
CONFIG
cloud instances create "$vm" --image "$image" --user-data=- < "$scratch/config"
guest() { cloud instances ssh "$vm" -- "$@" </dev/null; }
guest curl -fsS localhost/version | grep -Fx cloud-init-ok
guest sudo passwd -S learner | grep -E '^learner P '
# SSH to the guest over its loopback using the disposable key for both accounts.
"$runtime" exec -i "mcl-vm-$vm" sh -eu -c '
    key=$(mktemp)
    trap '\''rm -f "$key"'\'' EXIT
    cat > "$key"
    for account in learner "$1"; do
        ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$key" "$account@localhost" id -un | grep -Fx "$account"
    done
' sh "$login" < "$scratch/key"
cloud instances stop "$vm"
cloud instances start "$vm"
guest sudo cloud-init status --wait
guest cat /var/tmp/cloud-init-count > "$scratch/count"
printf 'once\n' | cmp - "$scratch/count"
echo "PASS: cloud-init accounts, SSH, password, apt, HTTP and once-per-instance execution"

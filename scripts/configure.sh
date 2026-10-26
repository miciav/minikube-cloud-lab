#!/usr/bin/env bash
# Source this file in each Bash terminal used for the Linux Compose lab.

if [ -z "${BASH_VERSION:-}" ]; then
    printf 'Use a Bash terminal and source scripts/configure.sh.\n' >&2
    return 1 2>/dev/null || exit 1
fi
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    printf 'Run: source scripts/configure.sh\nThis configures the current shell.\n' >&2
    exit 1
fi

_cloud_lab_configure() {
    local lab_root lab_uid lab_gid socket_gid env_file entry key
    if [[ $(uname -s) != Linux ]]; then
        printf 'This configuration is for Linux Compose labs; see the README for native macOS development.\n' >&2
        return 1
    fi
    lab_root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P) || return 1
    lab_uid=$(id -u) || return 1
    lab_gid=$(id -g) || return 1
    socket_gid=$(stat -c '%g' /var/run/docker.sock) || {
        printf 'Cannot read the local Docker socket group; check the Docker installation.\n' >&2
        return 1
    }
    env_file="$lab_root/.env"
    if [[ -L "$env_file" || ( -e "$env_file" && ! -f "$env_file" ) ]]; then
        printf 'Refusing to modify .env: it must be a regular file, not a symlink.\n' >&2
        return 1
    fi
    mkdir -p -m 700 "$HOME/.minikube-cloud-lab" "$HOME/.kube" || return 1
    if [[ ! -e "$env_file" ]]; then
        (umask 077; set -C; : > "$env_file") || return 1
    fi
    for entry in "UID=$lab_uid" "GID=$lab_gid" "DOCKER_GID=$socket_gid"; do
        key=${entry%%=*}
        if ! grep -Eq "^[[:space:]]*(export[[:space:]]+)?${key}[[:space:]]*=" "$env_file"; then
            printf '\n%s\n' "$entry" >> "$env_file" || return 1
        fi
    done

    CLOUD_LAB_ROOT=$lab_root
    # Bash's function keyword avoids expanding a pre-existing alias at definition time.
    function cloud() {
        docker compose --project-directory "$CLOUD_LAB_ROOT" --env-file "$CLOUD_LAB_ROOT/.env" \
            -f "$CLOUD_LAB_ROOT/compose.yaml" exec cloud cloud "$@"
    }
    function lab() {
        docker compose --project-directory "$CLOUD_LAB_ROOT" --env-file "$CLOUD_LAB_ROOT/.env" \
            -f "$CLOUD_LAB_ROOT/compose.yaml" run --rm lab "$@"
    }
    unalias cloud lab 2>/dev/null || true
    export KUBECONFIG="$HOME/.kube/config"
    printf 'Lab configuration ready. Existing .env values were preserved.\n'
    printf 'Start the cloud yourself with docker compose up -d cloud from %s.\n' "$CLOUD_LAB_ROOT"
}

if _cloud_lab_configure; then
    unset -f _cloud_lab_configure
else
    unset -f _cloud_lab_configure
    return 1
fi

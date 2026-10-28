#!/usr/bin/env bash
# Source this file in each Bash or zsh terminal used for the Compose lab.

if [ -z "${BASH_VERSION:-}${ZSH_VERSION:-}" ]; then
    printf 'Use Bash or zsh and source scripts/configure.sh.\n' >&2
    return 1 2>/dev/null || exit 1
fi
if { [ -n "${BASH_VERSION:-}" ] && [[ "${BASH_SOURCE[0]}" == "$0" ]]; } ||
   { [ -n "${ZSH_VERSION:-}" ] && [[ "${ZSH_EVAL_CONTEXT:-}" != *:file ]]; }; then
    printf 'Run: source scripts/configure.sh\nThis configures the current shell.\n' >&2
    exit 1
fi

_cloud_lab_configure() {
    if [ -n "${ZSH_VERSION:-}" ]; then
        emulate -L zsh
    fi
    local lab_root lab_uid lab_gid socket_gid env_file entry key lab_script socket_stat
    case $(uname -s) in
        Linux) socket_stat=-c ;;
        Darwin) socket_stat=-f ;;
        *) printf 'This configuration supports Linux and macOS with Docker Desktop.\n' >&2; return 1 ;;
    esac
    if [ -n "${ZSH_VERSION:-}" ]; then
        lab_script=${(%):-%x}
    else
        lab_script=${BASH_SOURCE[0]}
    fi
    lab_root=$(CDPATH= builtin cd -- "$(dirname -- "$lab_script")/.." >/dev/null && builtin pwd -P) || return 1
    lab_uid=$(id -u) || return 1
    lab_gid=$(id -g) || return 1
    socket_gid=$(stat "$socket_stat" '%g' /var/run/docker.sock) || {
        printf 'Cannot read the local Docker socket group; check the Docker installation.\n' >&2
        return 1
    }
    # Docker Desktop's mounted VM socket is root:root, independently of the macOS socket group.
    if [[ "$socket_stat" == -f ]]; then socket_gid=0; fi
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
    # The function keyword avoids expanding a pre-existing alias in both shells.
    function cloud {
        docker compose --project-directory "$CLOUD_LAB_ROOT" --env-file "$CLOUD_LAB_ROOT/.env" \
            -f "$CLOUD_LAB_ROOT/compose.yaml" exec cloud cloud "$@"
    }
    function lab {
        docker compose --project-directory "$CLOUD_LAB_ROOT" --env-file "$CLOUD_LAB_ROOT/.env" \
            -f "$CLOUD_LAB_ROOT/compose.yaml" run --rm lab "$@"
    }
    unalias cloud lab 2>/dev/null || true
    export KUBECONFIG="$HOME/.kube/config"
    printf 'Lab configuration ready. Existing .env values were preserved.\n'
    printf 'Start the cloud and portal yourself with docker compose up -d from %s.\n' "$CLOUD_LAB_ROOT"
}

if _cloud_lab_configure; then
    unset -f _cloud_lab_configure
else
    unset -f _cloud_lab_configure
    return 1
fi

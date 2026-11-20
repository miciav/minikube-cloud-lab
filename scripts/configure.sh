#!/usr/bin/env bash
# Source this file in each Bash or zsh terminal used for the lab.
# It detects rootful Docker or rootless Podman and defines labcompose, cloud, lab and kubectl.

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
    local lab_root lab_uid lab_gid socket_gid env_file entry key lab_script socket_stat runtime cgroup_version
    if [ -n "${ZSH_VERSION:-}" ]; then
        lab_script=${(%):-%x}
    else
        lab_script=${BASH_SOURCE[0]}
    fi
    lab_root=$(CDPATH= builtin cd -- "$(dirname -- "$lab_script")/.." >/dev/null && builtin pwd -P) || return 1
    runtime=$(bash "$lab_root/scripts/detect-runtime.sh" "${CLOUD_RUNTIME:-auto}") || return 1
    printf 'Detected runtime: %s\n' "$runtime"

    if [[ $runtime == podman ]]; then
        # podman-compose may be installed below with pip --user.
        case ":${PATH:-}:" in
            *":$HOME/.local/bin:"*) ;;
            *) PATH="$HOME/.local/bin${PATH:+:$PATH}" ;;
        esac
        export PATH
        # Podman 3 uses CGroupsVersion; newer versions use CgroupsVersion.
        if ! cgroup_version=$(podman info --format '{{.Host.CgroupsVersion}}' 2>/dev/null ||
            podman info --format '{{.Host.CGroupsVersion}}'); then
            printf 'Cannot read the Podman cgroup version. Check podman info.\n' >&2
            return 1
        fi
        if [[ $cgroup_version != v2 ]]; then
            printf 'Rootless resource limits require cgroups v2.\n' >&2
            return 1
        fi
        if [[ -z ${XDG_RUNTIME_DIR:-} || ! -S $XDG_RUNTIME_DIR/podman/podman.sock ]]; then
            printf 'Start the user API socket: systemctl --user start podman.socket\n' >&2
            return 1
        fi
        if [[ ! -e /proc/sys/net/bridge/bridge-nf-call-iptables ]]; then
            printf 'The instructor must load br_netfilter on the VM before running the lab.\n' >&2
            return 1
        fi
        # Install only when neither the standalone command nor a delegated provider works.
        if ! command -v podman-compose >/dev/null 2>&1 && ! podman compose version >/dev/null 2>&1; then
            if ! command -v python3 >/dev/null 2>&1 || ! python3 -m pip --version >/dev/null 2>&1; then
                printf 'Compose is missing. Python 3 with pip is required to install podman-compose without sudo.\n' >&2
                return 1
            fi
            printf 'Installing podman-compose in the current user account...\n'
            python3 -m pip install --user 'podman-compose>=1,<2' || return 1
            if ! command -v podman-compose >/dev/null 2>&1; then
                printf 'podman-compose was not found on PATH after installation; check Python user installation settings.\n' >&2
                return 1
            fi
        fi
    else
        case $(uname -s) in
            Linux) socket_stat=-c ;;
            Darwin) socket_stat=-f ;;
            *) printf 'This configuration supports Linux and macOS with Docker Desktop.\n' >&2; return 1 ;;
        esac
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
        export KUBECONFIG="$HOME/.kube/config"
    fi

    source "$lab_root/scripts/kubectl-forward.sh" || return 1
    CLOUD_LAB_ROOT=$lab_root
    CLOUD_LAB_RUNTIME=$runtime
    # The function keyword avoids expanding a pre-existing alias in both shells.
    function labcompose {
        if [[ $CLOUD_LAB_RUNTIME == docker ]]; then
            docker compose --project-directory "$CLOUD_LAB_ROOT" --env-file "$CLOUD_LAB_ROOT/.env" \
                -f "$CLOUD_LAB_ROOT/compose.yaml" "$@"
        elif command -v podman-compose >/dev/null 2>&1; then
            podman-compose -f "$CLOUD_LAB_ROOT/compose.rootless.yaml" "$@"
        else
            podman compose -f "$CLOUD_LAB_ROOT/compose.rootless.yaml" "$@"
        fi
    }
    # One-off tools container (lab, kubectl).
    function _cloud_lab_run {
        if [[ $CLOUD_LAB_RUNTIME == docker ]]; then
            docker compose --progress quiet --project-directory "$CLOUD_LAB_ROOT" --env-file "$CLOUD_LAB_ROOT/.env" \
                -f "$CLOUD_LAB_ROOT/compose.yaml" run "$@"
        else
            labcompose --profile tools run "$@"
        fi
    }
    function cloud {
        if [[ $CLOUD_LAB_RUNTIME == podman && ${1:-} == disks && ${2:-} == shell ]]; then
            shift 2
            "$CLOUD_LAB_ROOT/scripts/rootless-disk-shell.sh" "$@"
        else
            labcompose exec cloud cloud "$@"
        fi
    }
    function lab { _cloud_lab_run --rm lab "$@"; }
    function kubectl {
        local -a _cloud_forward_ports _cloud_forward_args
        local forward_status
        local -a _cloud_tty=()
        [ -t 1 ] || _cloud_tty=(-T) # no TTY when output is captured, else the runtime adds \r
        if _cloud_kubectl_forward_prepare "$@"; then
            _cloud_lab_run "${_cloud_tty[@]}" --rm --no-deps --entrypoint kubectl "${_cloud_forward_ports[@]}" \
                --volume "$PWD:/workspace" --workdir /workspace kubectl-forward "${_cloud_forward_args[@]}"
            return $?
        else
            forward_status=$?
            [ "$forward_status" = 125 ] || return "$forward_status"
        fi
        _cloud_lab_run "${_cloud_tty[@]}" --rm --entrypoint kubectl \
            --volume "$PWD:/workspace" --workdir /workspace lab "$@"
    }
    unalias labcompose cloud lab kubectl 2>/dev/null || true
    if [[ $runtime == docker ]]; then
        printf 'Lab configuration ready. Existing .env values were preserved.\n'
        printf 'Start the cloud and portal yourself with: labcompose up -d\n'
    else
        printf 'Ready. Start: labcompose up -d cloud\n'
    fi
}

if _cloud_lab_configure; then
    unset -f _cloud_lab_configure
else
    unset -f _cloud_lab_configure
    return 1
fi

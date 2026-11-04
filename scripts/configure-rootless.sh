#!/usr/bin/env bash
# Source from Bash to configure this terminal; no shell startup files are edited.
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
    printf 'Run: source scripts/configure-rootless.sh\n' >&2
    exit 1
fi
_cloud_rootless_configure() {
    local root cgroup_version
    root=$(CDPATH= builtin cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." >/dev/null && builtin pwd -P) || return 1
    if [[ $(podman info --format '{{.Host.Security.Rootless}}') != true ]]; then
        printf 'Podman must run rootless as the student user.\n' >&2
        return 1
    fi
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
    source "$root/scripts/kubectl-forward.sh" || return 1
    CLOUD_ROOTLESS_ROOT=$root
    function labcompose {
        if command -v podman-compose >/dev/null 2>&1; then
            podman-compose -f "$CLOUD_ROOTLESS_ROOT/compose.rootless.yaml" "$@"
        else
            podman compose -f "$CLOUD_ROOTLESS_ROOT/compose.rootless.yaml" "$@"
        fi
    }
    function cloud { labcompose exec cloud cloud "$@"; }
    function lab { labcompose run --rm lab "$@"; }
    function kubectl {
        local -a _cloud_forward_ports _cloud_forward_args
        local forward_status
        if _cloud_kubectl_forward_prepare "$@"; then
            labcompose run --rm --no-deps --entrypoint kubectl "${_cloud_forward_ports[@]}" \
                --volume "$PWD:/workspace" --workdir /workspace kubectl-forward "${_cloud_forward_args[@]}"
            return $?
        else
            forward_status=$?
            [ "$forward_status" = 125 ] || return "$forward_status"
        fi
        labcompose run --rm --entrypoint kubectl --volume "$PWD:/workspace" --workdir /workspace lab "$@"
    }
    unalias labcompose cloud lab kubectl 2>/dev/null || true
    printf 'Ready. Start: labcompose up -d cloud\n'
}
if _cloud_rootless_configure; then
    unset -f _cloud_rootless_configure
else
    unset -f _cloud_rootless_configure
    return 1
fi

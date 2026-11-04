#!/usr/bin/env bash
# Source from Bash to configure this terminal; no shell startup files are edited.
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
    printf 'Run: source scripts/configure-rootless.sh\n' >&2
    exit 1
fi
_cloud_rootless_configure() {
    local root
    root=$(CDPATH= builtin cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." >/dev/null && builtin pwd -P) || return 1
    if [[ $(podman info --format '{{.Host.Security.Rootless}}') != true ]]; then
        printf 'Podman must run rootless as the student user.\n' >&2
        return 1
    fi
    if [[ $(podman info --format '{{.Host.CgroupsVersion}}') != v2 ]]; then
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
    CLOUD_ROOTLESS_ROOT=$root
    function cloud { podman compose -f "$CLOUD_ROOTLESS_ROOT/compose.rootless.yaml" exec cloud cloud "$@"; }
    function lab { podman compose -f "$CLOUD_ROOTLESS_ROOT/compose.rootless.yaml" run --rm lab "$@"; }
    function kubectl { podman compose -f "$CLOUD_ROOTLESS_ROOT/compose.rootless.yaml" run --rm --entrypoint kubectl --volume "$PWD:/workspace" --workdir /workspace lab "$@"; }
    unalias kubectl 2>/dev/null || true
    printf 'Ready. Start: podman compose -f %q up -d cloud\n' "$root/compose.rootless.yaml"
}
if _cloud_rootless_configure; then
    unset -f _cloud_rootless_configure
else
    unset -f _cloud_rootless_configure
    return 1
fi

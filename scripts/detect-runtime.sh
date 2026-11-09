#!/usr/bin/env bash
# Print the supported runtime without starting services or modifying state.
_cloud_detect_runtime() {
    local requested=${1:-auto} runtime info rootless
    case $requested in
        auto|docker|podman) ;;
        *) printf 'Unknown runtime: %s (use auto, docker or podman).\n' "$requested" >&2; return 1 ;;
    esac
    for runtime in docker podman; do
        [[ $requested == auto || $requested == "$runtime" ]] || continue
        command -v "$runtime" >/dev/null 2>&1 || continue
        if [[ $runtime == docker ]]; then
            # A docker command can be the Podman compatibility shim.
            rootless=$(docker info --format '{{.Host.Security.Rootless}}' 2>/dev/null)
            if [[ $rootless == true || $rootless == false ]]; then
                runtime=podman
                command -v podman >/dev/null 2>&1 || continue
            else
                info=$(docker info --format '{{json .SecurityOptions}}' 2>/dev/null) || continue
                if [[ $info == *rootless* ]]; then
                    printf 'Docker rootless is detected but unsupported by compose.yaml; use rootless Podman or rootful Docker.\n' >&2
                    return 1
                fi
                printf 'docker\n'
                return 0
            fi
        fi
        rootless=$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null) || continue
        if [[ $rootless != true ]]; then
            printf 'Podman rootful is detected but unsupported; run as a rootless user.\n' >&2
            return 1
        fi
        printf 'podman\n'
        return 0
    done
    if [[ $requested == auto ]]; then
        printf 'No working Docker or Podman found. Check installation, permissions and runtime availability.\n' >&2
    else
        printf 'Cannot query %s. Check installation, permissions and runtime availability.\n' "$requested" >&2
    fi
    return 1
}
_cloud_detect_runtime "$@"

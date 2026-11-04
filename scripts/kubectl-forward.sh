#!/usr/bin/env bash
# Shared by the Bash/zsh Docker wrapper and the Bash Podman wrapper.
# Returns 125 for ordinary kubectl commands; arrays are local to the caller.
_cloud_kubectl_forward_prepare() {
    local arg value local_port command_seen=0 resource_seen=0 literal=0 invalid_address=0
    _cloud_forward_ports=()
    _cloud_forward_args=()
    while [ "$#" -gt 0 ]; do
        arg=$1
        shift
        if [ "$literal" = 0 ]; then
            case "$arg" in
                --) literal=1; _cloud_forward_args+=("$arg"); continue ;;
                -h|--help) return 125 ;;
                --address|--address=*)
                    if [ "$arg" = --address ]; then
                        value=${1:-}; [ "$#" -gt 0 ] && shift
                    else
                        value=${arg#*=}
                    fi
                    case "$value" in
                        localhost|127.0.0.1) ;;
                        *) invalid_address=1 ;;
                    esac
                    continue ;;
                --*=*|--disable-compression|--insecure-skip-tls-verify|--match-server-version|--warnings-as-errors)
                    _cloud_forward_args+=("$arg"); continue ;;
                -n|-s|-v|--*)
                    _cloud_forward_args+=("$arg")
                    if [ "$#" -eq 0 ]; then return 125; fi
                    _cloud_forward_args+=("$1"); shift; continue ;;
                -*) _cloud_forward_args+=("$arg"); continue ;;
            esac
        fi
        _cloud_forward_args+=("$arg")
        if [ "$command_seen" = 0 ]; then
            [ "$arg" = port-forward ] || return 125
            command_seen=1
            _cloud_forward_args+=(--address=0.0.0.0)
        elif [ "$resource_seen" = 0 ]; then
            resource_seen=1
        else
            local_port=${arg%%:*}
            # shortcut: random local ports need coordinated runtime/kubectl allocation.
            if ! [[ $local_port =~ ^[1-9][0-9]*$ ]] || [ "${#local_port}" -gt 5 ] || [ "$local_port" -gt 65535 ]; then
                printf 'Containerized port-forward requires a local port from 1 to 65535 (for example 18080:80); random ports are unsupported.\n' >&2
                return 1
            fi
            _cloud_forward_ports+=(--publish "127.0.0.1:$local_port:$local_port")
        fi
    done
    if [ "$command_seen" = 1 ] && [ "$invalid_address" = 1 ]; then
        printf 'Containerized port-forward only supports --address=localhost or 127.0.0.1.\n' >&2
        return 1
    fi
    [ "$command_seen" = 1 ] && [ "${#_cloud_forward_ports[@]}" -gt 0 ] || return 125
}

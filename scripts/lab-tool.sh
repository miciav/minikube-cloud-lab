#!/bin/sh
set -eu
# SSH resolves the host-mapped UID with getpwuid even if HOME is set.
if ! getent passwd "$(id -u)" >/dev/null; then
    userdb=$(mktemp -d)
    cat /etc/passwd > "$userdb/passwd"
    printf 'student:x:%s:%s:Course user:/tmp:/bin/sh\n' "$(id -u)" "$(id -g)" >> "$userdb/passwd"
    cat /etc/group > "$userdb/group"
    if ! getent group "$(id -g)" >/dev/null; then
        printf 'student:x:%s:\n' "$(id -g)" >> "$userdb/group"
    fi
    export NSS_WRAPPER_PASSWD="$userdb/passwd" NSS_WRAPPER_GROUP="$userdb/group" LD_PRELOAD=/opt/libnss_wrapper.so
fi
exec "$@"

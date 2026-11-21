#!/usr/bin/env bash
# Use an empty module installation. Keep failed runs available for diagnosis.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/configure.sh
case ${1:-all} in
    all) modules=(object-storage queue database) ;;
    object-storage|queue|database) modules=("$1") ;;
    *) echo "Usage: $0 [all|object-storage|queue|database]" >&2; exit 2 ;;
esac
for module in "${modules[@]}"; do
    [[ $(lab status "$module") == "$module disabled empty" ]] || { echo "Existing $module state" >&2; exit 1; }
done
s3() { lab client object-storage s3 "$@"; }
sqs() { lab client queue sqs "$@"; }
sql() { lab client database -v ON_ERROR_STOP=1 -Atc "$1"; }
for module in "${modules[@]}"; do
    lab install "$module"
    lab install "$module"
    case $module in
        object-storage)
            echo course-marker | s3 cp - s3://course-files/marker
            lab client object-storage --read-only s3 cp s3://course-files/marker - | grep -Fx course-marker
            if echo denied | lab client object-storage --read-only s3 cp - s3://course-files/denied; then exit 1; fi ;;
        queue)
            url=$(sqs create-queue --queue-name jobs --attributes VisibilityTimeout=3 --query QueueUrl --output text)
            sqs send-message --queue-url "$url" --message-body course-marker ;;
        database)
            if sql "CREATE ROLE forbidden SUPERUSER"; then exit 1; fi
            sql "CREATE TABLE proof(id int primary key); INSERT INTO proof VALUES (42)" ;;
    esac
    if lab purge "$module" --yes; then echo "Purged an installed service" >&2; exit 1; fi
    lab uninstall "$module"
    lab uninstall "$module"
    [[ $(lab status "$module") == "$module disabled retained" ]]
done
for module in "${modules[@]}"; do lab install "$module"; done
check_service() {
    case $1 in
        object-storage) [[ $(s3 cp s3://course-files/marker -) == course-marker ]] ;;
        database) [[ $(sql "UPDATE proof SET id=id RETURNING id" | sed -n '1p') == 42 ]] ;;
        queue) [[ $(sqs receive-message --queue-url "$url" --visibility-timeout 0 --wait-time-seconds 5 --query 'Messages[0].Body' --output text) == course-marker ]] ;;
    esac
}
for module in "${modules[@]}"; do
    lab uninstall "$module"
    for other in "${modules[@]}"; do
        if [[ $other != "$module" ]]; then check_service "$other"; fi
    done
    lab install "$module"
    check_service "$module"
done
if [[ ${#modules[@]} == 3 ]]; then bash lab/services/examples/csv-worker.sh; fi
for module in "${modules[@]}"; do
    lab uninstall "$module"
    lab purge "$module" --yes
    [[ $(lab status "$module") == "$module disabled empty" ]]
done

for module in "${modules[@]}"; do
    lab install "$module"
    case $module in
        object-storage) keys=$(s3 ls s3://course-files/) && [[ -z $keys ]] || exit 1 ;;
        queue) [[ $(sqs list-queues --query 'length(QueueUrls || `[]`)' --output text) == 0 ]] ;;
        database) [[ $(sql "SELECT to_regclass('proof') IS NULL") == t ]] ;;
    esac
    lab uninstall "$module"
    lab purge "$module" --yes
done

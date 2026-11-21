#!/usr/bin/env bash
# Run on an empty lab. A failed run leaves the owned resources for diagnosis.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
source "$root/scripts/configure.sh"
for m in portal monitoring queue object-storage; do
  [[ $(lab status "$m") == "$m disabled empty" ]] || { echo "Existing $m state" >&2; exit 1; }
done
lab install portal
lab install portal
curl -fsS http://127.0.0.1:3000/v1/machine-types >/dev/null
[[ $(lab status monitoring) == "monitoring disabled empty" ]]
lab uninstall portal
if curl --max-time 2 --silent http://127.0.0.1:3000/ >/dev/null; then echo "Portal port still in use" >&2; exit 1; fi
lab uninstall portal
lab install monitoring
lab install monitoring
[[ $(lab status portal) == "portal disabled empty" ]]
if lab purge monitoring --yes; then echo "Purged running monitoring" >&2; exit 1; fi
lab install object-storage
bucket_sample() { python3 "$root/tests/history-sample.py" "$1" course-files --equals "$2"; }
bucket_sample objects 0
printf abc | lab client object-storage s3 cp - s3://course-files/one
printf abcd | lab client object-storage s3 cp - s3://course-files/two
bucket_sample objects 2
bucket_sample bytes 7
printf x | lab client object-storage s3 cp - s3://course-files/one
bucket_sample bytes 5
lab client object-storage s3 rm s3://course-files/two
bucket_sample objects 1
bucket_sample bytes 1
lab uninstall object-storage
lab purge object-storage --yes
lab install queue
sqs() { lab client queue sqs "$@"; }
url=$(sqs create-queue --queue-name observation --query QueueUrl --output text)
resource=${url#*://}; resource=${resource#*/}
sample() { python3 "$root/tests/history-sample.py" "$1" "$resource" --equals "$2"; }
sample visible 0
sqs send-message --queue-url "$url" --message-body visible >/dev/null
sample visible 1
receipt=$(sqs receive-message --queue-url "$url" --visibility-timeout 120 --query 'Messages[0].ReceiptHandle' --output text)
sample inflight 1
sqs send-message --queue-url "$url" --message-body delayed --delay-seconds 120 >/dev/null
sample delayed 1
sqs delete-message --queue-url "$url" --receipt-handle "$receipt"
lab install portal
api="http://127.0.0.1:3000/v1/monitoring/history?metric=inflight"
stamp=$(curl -fsS "$api" | python3 -c 'import json,sys; print(next(p[0] for s in json.load(sys.stdin)["series"] for p in s["points"] if p[1]==1))')
lab uninstall monitoring
lab uninstall monitoring
[[ $(lab status monitoring) == "monitoring disabled retained" ]]
lab install monitoring
curl -fsS "$api" | python3 -c 'import json,sys; assert any(p==[float(sys.argv[1]),1] for s in json.load(sys.stdin)["series"] for p in s["points"])' "$stamp"
lab uninstall monitoring
lab purge monitoring --yes
lab install monitoring
curl -fsS "$api" | python3 -c 'import json,sys; assert all(p[0]>float(sys.argv[1]) for s in json.load(sys.stdin)["series"] for p in s["points"])' "$stamp"
lab uninstall portal
lab uninstall monitoring
lab purge monitoring --yes
lab uninstall queue
lab purge queue --yes
echo "PASS: independent portal, passive queue metrics, retained and purged history"

#!/usr/bin/env bash
# Run with all three modules installed. Simulate failure between SQL commit and SQS acknowledgment.
set -euo pipefail
cd "$(dirname "$0")/../../.."
source scripts/configure.sh
job=$(python3 -c 'import uuid; print(uuid.uuid4().hex)')
sqs() { lab client queue sqs "$@"; }
sql() { lab client database -v ON_ERROR_STOP=1 -Atc "$1"; }
url=$(sqs create-queue --queue-name "csv-$job" --attributes VisibilityTimeout=3 --query QueueUrl --output text)
printf 'quantity\n3\n5\n' | lab client object-storage s3 cp - "s3://course-files/$job.csv"
sqs send-message --queue-url "$url" --message-body "$job" >/dev/null
sql 'CREATE TABLE IF NOT EXISTS csv_results(job text PRIMARY KEY, total integer NOT NULL)'
for attempt in 1 2; do
    delivery=$(sqs receive-message --queue-url "$url" --wait-time-seconds 15 --query 'Messages[0].[Body,ReceiptHandle]' --output text)
    read -r key receipt <<<"$delivery"
    [[ $key == "$job" && -n $receipt ]]
    total=$(lab client object-storage s3 cp "s3://course-files/$key.csv" - |
        python3 -c 'import csv,sys; print(sum(int(r["quantity"]) for r in csv.DictReader(sys.stdin)))')
    sql "INSERT INTO csv_results VALUES ('$job',$total) ON CONFLICT (job) DO NOTHING"
    if [[ $attempt == 1 ]]; then
        echo 'Committed. Simulating a worker exit before acknowledging the message.'
        sleep 4
    else
        sqs delete-message --queue-url "$url" --receipt-handle "$receipt"
    fi
done
[[ $(sql "SELECT total FROM csv_results WHERE job='$job'") == 8 ]]
sqs delete-queue --queue-url "$url"
echo "PASS: job $job was delivered twice and recorded once (total 8)."

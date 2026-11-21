# Receive and acknowledge a message

From a configured shell run:

```sh
lab install queue
sqs() { lab client queue sqs "$@"; }
url=$(sqs create-queue --queue-name lesson --attributes VisibilityTimeout=5 --query QueueUrl --output text)
sqs send-message --queue-url "$url" --message-body lesson-one
sqs receive-message --queue-url "$url"
sleep 6
receipt=$(sqs receive-message --queue-url "$url" --query 'Messages[0].ReceiptHandle' --output text)
sqs delete-message --queue-url "$url" --receipt-handle "$receipt"
sqs receive-message --queue-url "$url" --wait-time-seconds 2
```

The first receive hides the message for five seconds. After that window the second receive returns a new receipt handle. Deleting with that handle acknowledges delivery. The final receive should be empty.

Send another message, uninstall and reinstall the queue, then receive it. ElasticMQ stores queues and messages in its own H2 volume. The normal message retention period still applies. Host clients can use the local endpoint printed at install and lab-only dummy AWS credentials. Use this queue on a trusted, single-team VM.

Clean up with `sqs delete-queue --queue-url "$url"` and `lab uninstall queue`.

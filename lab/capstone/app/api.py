"""Trusted lab HTTP API for submitting CSV files and leasing work."""
import hmac
import json
import os
import uuid
from contextlib import closing
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import boto3
import psycopg2
from botocore.config import Config

s3 = boto3.client("s3", region_name="garage", endpoint_url="http://object-storage:3900",
                  config=Config(s3={"addressing_style": "path"}))
queue = boto3.client("sqs", region_name="garage", endpoint_url="http://queue:9324",
                     aws_access_key_id="course", aws_secret_access_key="course")
bucket = "course-files"
url = queue.create_queue(QueueName="capstone-jobs")["QueueUrl"]


def database():
    return psycopg2.connect(host="database", dbname="course", user="student", connect_timeout=5)


with closing(database()) as connection, connection, connection.cursor() as cursor:
    cursor.execute("CREATE TABLE IF NOT EXISTS capstone_results (job uuid PRIMARY KEY, result jsonb NOT NULL)")


class Handler(BaseHTTPRequestHandler):
    def reply(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def worker(self):
        if hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + os.environ["WORKER_TOKEN"]):
            return True
        self.reply(403, {"error": "Invalid worker token"})
        return False

    def do_GET(self):
        if self.path == "/health":
            return self.reply(200, {"status": "ready"})
        if self.path == "/work":
            if not self.worker():
                return
            batch = queue.receive_message(QueueUrl=url, WaitTimeSeconds=5, MaxNumberOfMessages=1)
            if not batch.get("Messages"):
                return self.reply(200, {})
            msg = batch["Messages"][0]
            job = uuid.UUID(msg["Body"]).hex
            data = s3.get_object(Bucket=bucket, Key="capstone/" + job)["Body"].read().decode()
            return self.reply(200, {"job": job, "receipt": msg["ReceiptHandle"], "csv": data})
        if self.path.startswith("/results/"):
            try:
                job = uuid.UUID(self.path.removeprefix("/results/")).hex
            except ValueError:
                return self.reply(400, {"error": "Invalid job ID"})
            with closing(database()) as conn, conn.cursor() as cur:
                cur.execute("SELECT result FROM capstone_results WHERE job=%s", (job,))
                row = cur.fetchone()
            return self.reply(200 if row else 404, row[0] if row else {"error": "No result yet"})
        self.reply(404, {"error": "Unknown endpoint"})

    def do_POST(self):
        try:
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 < size <= 65536:
                return self.reply(400, {"error": "Expected 1 to 65536 bytes"})
            body = self.rfile.read(size).decode()
            if self.path == "/jobs":
                job = uuid.uuid4().hex
                s3.put_object(Bucket=bucket, Key="capstone/" + job, Body=body.encode())
                queue.send_message(QueueUrl=url, MessageBody=job)
                return self.reply(202, {"job": job})
            if self.path != "/complete":
                return self.reply(404, {"error": "Unknown endpoint"})
            if not self.worker():
                return
            data = json.loads(body)
            job = uuid.UUID(data["job"]).hex
            receipt = data["receipt"]
            with closing(database()) as conn, conn, conn.cursor() as cur:
                cur.execute("INSERT INTO capstone_results VALUES (%s,%s) ON CONFLICT DO NOTHING",
                            (job, json.dumps(data["result"])))
            queue.delete_message(QueueUrl=url, ReceiptHandle=receipt)
            self.reply(200, {"job": job})
        except (ValueError, KeyError, TypeError):
            self.reply(400, {"error": "Malformed request"})


ThreadingHTTPServer((os.environ["API_BIND"], 8090), Handler).serve_forever()

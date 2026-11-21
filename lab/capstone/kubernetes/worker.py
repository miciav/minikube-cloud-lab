"""Compute a CSV total. Successful completion commits and acknowledges the job."""
import csv
import io
import json
import os
import time
from urllib.error import URLError
from urllib.request import Request, urlopen


def call(path, payload=None):
    data = None if payload is None else json.dumps(payload).encode()
    request = Request(os.environ["API_URL"] + path, data=data,
                      headers={"Authorization": "Bearer " + os.environ["WORKER_TOKEN"]})
    with urlopen(request, timeout=20) as response:
        return json.load(response)


while True:
    try:
        job = call("/work")
        if job:
            try:
                result = {"total": sum(int(row["quantity"]) for row in csv.DictReader(io.StringIO(job["csv"])))}
            except (ValueError, TypeError, KeyError):
                result = {"error": "Expected a CSV with integer quantities"}
            call("/complete", {"job": job["job"], "receipt": job["receipt"], "result": result})
            print(job["job"], result, flush=True)
    except (URLError, OSError) as error:
        print("API unavailable:", error, flush=True)
        time.sleep(2)

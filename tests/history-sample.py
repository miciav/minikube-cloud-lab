"""Print the latest fresh sample matching the fixture and condition, with a bounded wait."""
import argparse
import json
import time
from urllib.error import URLError
from urllib.parse import urlencode
from urllib.request import urlopen

parser = argparse.ArgumentParser()
parser.add_argument("metric")
parser.add_argument("resource")
parser.add_argument("--equals", type=float)
parser.add_argument("--since", type=float, default=0)
parser.add_argument("--before", type=float)
parser.add_argument("--portal", action="store_true")
args = parser.parse_args()
port = 3000 if args.portal else 8080
url = f"http://127.0.0.1:{port}/v1/monitoring/history?" + urlencode(dict(metric=args.metric, resource=args.resource))
deadline = time.monotonic() + 120
while time.monotonic() < deadline:
    try:
        with urlopen(url, timeout=5) as response:
            history = json.load(response)
        for series in history["series"]:
            if args.before is None:
                points = series["points"][-1:]
                earliest = max(args.since, time.time()-45)
            else:
                points = [p for p in series["points"] if p[0] < args.before]
                earliest = args.since
            if history["sourceStatus"] == "available":
                for ts, value in reversed(points):
                    if ts >= earliest and value >= 0 and (args.equals is None or value == args.equals):
                        print(value)
                        raise SystemExit(0)
    except URLError:
        pass
    time.sleep(2)
raise SystemExit(f"Timed out waiting for {args.metric} on {args.resource}")

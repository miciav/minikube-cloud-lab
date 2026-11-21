"""Prepare inventory, service networking and credentials for the deployed capstone VM."""
import json
import os
from pathlib import Path
import secrets
import shlex
import sys
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parent
os.chdir(ROOT)
os.umask(0o077)
local = ROOT / ".local"
local.mkdir(exist_ok=True)
with urlopen("http://127.0.0.1:8080/v1/instances/csv-api", timeout=10) as response:
    vm = json.load(response)
if vm["status"] != "RUNNING" or not vm["hostKey"]:
    raise SystemExit("The csv-api instance must finish cloud-init first.")
(local / "known_hosts").write_text("csv-api " + vm["hostKey"].strip() + "\n")
options = ["-o", "HostKeyAlias=csv-api", "-o", "StrictHostKeyChecking=yes",
           "-o", "UserKnownHostsFile=/workspace/.local/known_hosts"]
inventory = {"all": {"hosts": {"csv-api": {"ansible_host": vm["ip"], "ansible_user": "deployer",
             "ansible_ssh_common_args": shlex.join(options), "ansible_python_interpreter": "/usr/bin/python3"}}}}
(local / "inventory.yml").write_text(json.dumps(inventory))
token_file = local / "worker-token"
if not token_file.exists():
    with token_file.open("x") as out:
        out.write(secrets.token_hex(32))
token = token_file.read_text().strip()
access, secret, password = sys.stdin.read().splitlines()
(local / "api.env").write_text(f"AWS_ACCESS_KEY_ID={access}\nAWS_SECRET_ACCESS_KEY={secret}\nPGPASSWORD={password}\n"
                               f"API_BIND={vm['ip']}\nWORKER_TOKEN={token}\n")
(local / "worker.env").write_text(f"API_URL=http://{vm['ip']}:8090\nWORKER_TOKEN={token}\n")
print("Prepared .local/inventory.yml and private application configuration.")

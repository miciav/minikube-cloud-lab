#!/usr/bin/env python3
"""Drive the emulated serial console on a running, dedicated rootless test stack."""
import os
import re
import selectors
import subprocess
import time
import uuid
from contextlib import contextmanager
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def cloud(*args, expected_error=None):
    result = subprocess.run(
        ["bash", "-c", 'source scripts/configure.sh && labcompose exec -T cloud cloud "$@"', "test", *args],
        cwd=ROOT, stdin=subprocess.DEVNULL, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=600,
    )
    if (result.returncode != 0) != bool(expected_error) or (expected_error and expected_error not in result.stdout):
        raise RuntimeError(result.stdout)
    return result.stdout


@contextmanager
def console(owner, disk):
    proc = subprocess.Popen([ROOT / "scripts/rootless-disk-shell.sh", owner, disk], cwd=ROOT,
                            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    try:
        with selectors.DefaultSelector() as sel:
            sel.register(proc.stdout, selectors.EVENT_READ)

            def wait_for(pattern):
                deadline = time.monotonic() + 600
                output = b""
                while not re.search(pattern, output):
                    if not sel.select(max(0, deadline - time.monotonic())):
                        raise TimeoutError(output[-2000:])
                    chunk = os.read(proc.stdout.fileno(), 8192)
                    if not chunk:
                        raise RuntimeError(output[-3000:])
                    output = (output + chunk)[-16000:]
                    if re.search(rb"\nDISK_FAIL\r?\n", output):
                        raise RuntimeError(output[-3000:])

            wait_for(rb"[/~] # ")
            def command(text):
                proc.stdin.write((text + " && printf '\\nDISK_OK\\n' || printf '\\nDISK_FAIL\\n'\n").encode())
                proc.stdin.flush()
                wait_for(rb"\nDISK_OK\r?\n")
            yield command
            proc.stdin.write(b"exit\n")
            proc.stdin.flush()
            if proc.wait(timeout=30):
                raise RuntimeError("QEMU console exited unsuccessfully")
    finally:
        if proc.poll() is None:
            proc.terminate()
        proc.communicate(timeout=30)


def main():
    disk = "qemu-" + uuid.uuid4().hex[:12]
    owners = [disk + "-a", disk + "-b"]
    created = []
    cloud("disks", "create", disk, "--size", "64Mi")
    try:
        for owner in owners:
            created.append(owner)
            cloud("instances", "create", owner)
        for index, owner in enumerate(owners):
            cloud("disks", "attach", disk, "--instance", owner)
            with console(owner, disk) as run:
                run("test -b /dev/vda && lsblk /dev/vda")
                if index == 0:
                    run("mkfs.ext4 -q /dev/vda && mount /dev/vda /mnt && echo students >/mnt/proof")
                    cloud("disks", "detach", disk, "--instance", owner, expected_error="active disk console")
                    cloud("disks", "attach", disk, "--instance", owners[1], expected_error="already attached")
                else:
                    run("mount /dev/vda /mnt && grep -qx students /mnt/proof")
                run("umount /mnt")
            cloud("disks", "detach", disk, "--instance", owner)
    finally:
        for owner in created:
            cloud("instances", "delete", owner)
        cloud("disks", "delete", disk)
    print("PASS: rootless QEMU block device, ext4, busy rejection and data transfer", flush=True)


if __name__ == "__main__":
    main()

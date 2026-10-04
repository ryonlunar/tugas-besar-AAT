#!/usr/bin/env python3
"""Sample client-side TCP connections to the API on a Linux load-generator host."""
import argparse
from datetime import datetime, timezone
import ipaddress
import json
from pathlib import Path
import socket
import subprocess
import time
from urllib.parse import urlsplit


def capture(api_url, seconds, interval):
    target = urlsplit(api_url)
    if target.scheme not in ("http", "https") or not target.hostname:
        raise ValueError("API URL must use http or https")
    port = target.port or (443 if target.scheme == "https" else 80)
    addresses = {str(ipaddress.ip_address(item[4][0])) for item in socket.getaddrinfo(target.hostname, port, type=socket.SOCK_STREAM)}
    samples = []
    started = time.monotonic()
    while time.monotonic() - started < seconds:
        # A collection failure raises; it must not become a false zero count.
        result = subprocess.run(["ss", "-Htn", "state", "established"], check=True, capture_output=True, text=True, timeout=5)
        if result.stderr.strip():
            raise RuntimeError(f"ss reported a collection error: {result.stderr.strip()}")
        count = 0
        for line in result.stdout.splitlines():
            fields = line.split()
            peer, peer_port = fields[-1].rsplit(":", 1)
            address = ipaddress.ip_address(peer.strip("[]"))
            address = str(address.ipv4_mapped or address) if address.version == 6 else str(address)
            if int(peer_port) == port and address in addresses:
                count += 1
        samples.append({"timestamp": datetime.now(timezone.utc).isoformat(), "elapsed_seconds": round(time.monotonic() - started, 3), "established_connections_to_api": count})
        time.sleep(interval)
    return {"api_url": api_url, "resolved_addresses": sorted(addresses), "sampling_interval_seconds": interval, "samples": samples}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api-url", default="http://127.0.0.1:8080")
    parser.add_argument("--seconds", type=float, default=95)
    parser.add_argument("--interval", type=float, default=1)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.seconds <= 0 or args.interval <= 0:
        parser.error("duration and interval must be positive")
    evidence = capture(args.api_url, args.seconds, args.interval)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(evidence, indent=2) + "\n")
    print(f"Wrote {len(evidence['samples'])} TCP samples to {args.output}")


if __name__ == "__main__":
    main()

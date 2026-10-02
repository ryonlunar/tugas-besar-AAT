#!/usr/bin/env python3
"""Load local RSA keys into separate Docker volumes before starting Compose."""
import argparse
import json
from pathlib import Path
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--private", type=Path, default=Path(".secrets/jwt-private.pem"))
    parser.add_argument("--public", type=Path, default=Path(".secrets/jwt-public.pem"))
    args = parser.parse_args()
    private = args.private.read_bytes()
    public = args.public.read_bytes()
    # Reject a mismatched pair before touching persistent volumes.
    derived = subprocess.run(["openssl", "pkey", "-pubout"], input=private, capture_output=True, check=True).stdout
    if derived.strip() != public.strip():
        raise ValueError("public key does not match private key")
    config = json.loads(subprocess.check_output(["docker", "compose", "config", "--format", "json"]))
    for volume, filename, contents in [("jwt-private", "jwt-private.pem", private), ("jwt-public", "jwt-public.pem", public)]:
        name = config["volumes"][volume]["name"]
        subprocess.run(["docker", "volume", "create", "--label", f"com.docker.compose.project={config['name']}", "--label", f"com.docker.compose.volume={volume}", name], check=True, stdout=subprocess.DEVNULL)
        # stdin streams bytes through the Docker API; no host bind mount or CLI secret.
        subprocess.run(["docker", "run", "--rm", "-i", "--mount", f"type=volume,source={name},target=/keys", "--entrypoint", "sh", "alpine:3.21",
                        "-c", f"umask 077; cat > /keys/{filename} && chown 65532:65532 /keys/{filename} && chmod 0400 /keys/{filename}"], input=contents, check=True)
    print("Loaded matching RSA keys into separate private/public volumes.")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Publish an already-built public site to Tencent COS without deleting objects."""

import argparse
import hashlib
import io
import json
import mimetypes
import os
from pathlib import Path
import re
import sys
from datetime import datetime, timezone


REQUIRED_FILES = (
    "index.html",
    "guide/index.html",
    "demo/index.html",
    "demo/wasm/worker.js",
    "demo/wasm/wasm_exec.js",
    "demo/wasm/psdkit.wasm",
    "demo/wasm/manifest.json",
    "demo/samples/manifest.json",
    "demo/sdk/client.js",
)
SKIP_FILES = {".nojekyll", "CNAME"}
PRIVATE_NAMES = {"credentials", "credentials.json", "gemfile", "gemfile.lock", "id_rsa", "id_ecdsa", "id_ed25519"}
PRIVATE_SUFFIXES = {".key", ".pem", ".p12", ".pfx", ".sqlite", ".db"}
CONTENT_TYPES = {
    ".html": "text/html; charset=utf-8",
    ".css": "text/css; charset=utf-8",
    ".js": "text/javascript; charset=utf-8",
    ".mjs": "text/javascript; charset=utf-8",
    ".json": "application/json; charset=utf-8",
    ".wasm": "application/wasm",
    ".gz": "application/gzip",
    ".psd": "image/vnd.adobe.photoshop",
    ".psb": "application/octet-stream",
    ".ts": "text/plain; charset=utf-8",
}


def public_files(site):
    """Fail before any upload if the tree contains symlinks or private files."""
    site = Path(site).absolute()
    if site.is_symlink() or not site.is_dir():
        raise ValueError("--site must be a real directory containing the built public site")
    files = []
    for directory, dirs, names in os.walk(site, followlinks=False):
        for name in dirs + names:
            path = Path(directory) / name
            key = path.relative_to(site).as_posix()
            if path.is_symlink():
                raise ValueError(f"symlink is not publishable: {key}")
            if key in SKIP_FILES:
                continue
            if (name.startswith(".") or name.lower() in PRIVATE_NAMES
                    or path.suffix.lower() in PRIVATE_SUFFIXES):
                raise ValueError(f"private or hidden path is not publishable: {key}")
            if path.is_file():
                files.append((key, path))
    keys = {key for key, _ in files}
    missing = sorted(set(REQUIRED_FILES) - keys)
    if missing:
        raise ValueError("incomplete static site; missing: " + ", ".join(missing))
    if "deployment.json" in keys:
        raise ValueError("deployment.json is reserved for the deployment completion record")
    # Upload dependencies before any HTML that could start referencing them.
    return sorted(files, key=lambda item: (item[0].endswith(".html"), item[0]))


def object_headers(key):
    suffix = Path(key).suffix.lower()
    content_type = CONTENT_TYPES.get(suffix)
    if not content_type:
        content_type = mimetypes.guess_type(key)[0] or "application/octet-stream"
    # Only Vite filenames with a content hash are immutable. Worker, WASM,
    # manifests, documentation assets and HTML must be revalidated on every use.
    hashed_asset = re.fullmatch(r"demo/assets/[^/]+-[A-Za-z0-9_-]{8,}\.[a-z0-9]+", key)
    return {
        "ContentType": content_type,
        "CacheControl": "public, max-age=31536000, immutable" if hashed_asset else "no-cache",
        "ContentDisposition": "inline",
    }


def deployment_record(files, revision):
    digest = hashlib.sha256()
    total_bytes = 0
    for key, path in sorted(files):
        data = path.read_bytes()
        total_bytes += len(data)
        digest.update(key.encode("utf-8") + b"\0")
        digest.update(hashlib.sha256(data).digest())
    return {
        "schemaVersion": 1,
        "revision": revision,
        "files": len(files),
        "bytes": total_bytes,
        "siteSha256": digest.hexdigest(),
    }


def publish(client, bucket, files, record):
    for key, path in files:
        with path.open("rb") as body:
            client.put_object(Bucket=bucket, Key=key, Body=body, EnableMD5=True,
                              **object_headers(key))
    # This is uploaded only after all site files succeed; a failed upload never
    # claims the new revision finished. Existing objects are deliberately retained.
    record = dict(record, deployedAt=datetime.now(timezone.utc).isoformat())
    client.put_object(Bucket=bucket, Key="deployment.json",
                      Body=io.BytesIO((json.dumps(record, ensure_ascii=False, indent=2) + "\n").encode()),
                      EnableMD5=True, **object_headers("deployment.json"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--site", default="_site", help="complete public static build directory")
    parser.add_argument("--revision", default=os.environ.get("GITHUB_SHA", ""), help="source commit SHA")
    parser.add_argument("--dry-run", action="store_true", help="validate and summarize without credentials or network")
    args = parser.parse_args()
    try:
        if args.revision and not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", args.revision):
            raise ValueError("--revision must be a full source commit SHA")
        files = public_files(args.site)
        record = deployment_record(files, args.revision)
        if args.dry_run:
            print(json.dumps(dict(record, dryRun=True), ensure_ascii=False))
            return 0
        if not args.revision:
            raise ValueError("--revision is required for a deployment")
        required_env = ("COS_SECRET_ID", "COS_SECRET_KEY", "COS_BUCKET", "COS_REGION")
        missing = [name for name in required_env if not os.environ.get(name)]
        if missing:
            raise ValueError("missing environment variables: " + ", ".join(missing))
        bucket, region = os.environ["COS_BUCKET"], os.environ["COS_REGION"]
        if not re.fullmatch(r"[a-z0-9][a-z0-9-]*-\d+", bucket):
            raise ValueError("COS_BUCKET must be a complete bucket name including APPID")
        if not re.fullmatch(r"[a-z]{2}-[a-z0-9-]+", region):
            raise ValueError("COS_REGION must be a Tencent COS region name")
        try:
            from qcloud_cos import CosConfig, CosS3Client
        except ImportError:
            raise ValueError("install scripts/cos-requirements.txt before deploying") from None
        config = CosConfig(Region=region, SecretId=os.environ["COS_SECRET_ID"],
                           SecretKey=os.environ["COS_SECRET_KEY"],
                           Token=os.environ.get("COS_SESSION_TOKEN"), Scheme="https")
        publish(CosS3Client(config), bucket, files, record)
        print(json.dumps(dict(record, deployed=True), ensure_ascii=False))
        return 0
    except ValueError as error:
        print(f"Deployment validation failed: {error}", file=sys.stderr)
    except Exception as error:
        # SDK exceptions may contain signed request details. Keep secrets out of CI logs.
        print(f"COS deployment failed ({type(error).__name__}); the completion record was not updated.", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())

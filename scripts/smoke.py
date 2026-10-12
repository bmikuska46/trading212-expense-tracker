#!/usr/bin/env python3
"""Check real exported routes, assets, same-origin health, and private build canaries."""
import argparse
import json
from html.parser import HTMLParser
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

parser = argparse.ArgumentParser()
parser.add_argument("origin", nargs="?", default="http://127.0.0.1:8080")
args = parser.parse_args()
origin = args.origin.rstrip("/")
export = Path(__file__).resolve().parent.parent / "web" / "out"
canaries = [b"budget-private-build-canary", b"budget-private-typesafe-canary", b"budget-private-resend-canary"]
for asset in export.rglob("*"):
    if asset.is_file():
        assert not any(value in asset.read_bytes() for value in canaries), f"Private build value leaked into {asset}"

class Assets(HTMLParser):
    def __init__(self):
        super().__init__()
        self.paths = set()
    def handle_starttag(self, tag, attrs):
        for name, value in attrs:
            if name in ("src", "href") and value and value.startswith("/_next/"):
                self.paths.add(value)

served = 0
def get(path, method="GET"):
    global served
    with urlopen(Request(origin + path, method=method), timeout=10) as response:
        assert response.status == 200, (path, response.status)
        body = response.read()
    # The running image may differ from the local export, so scan what it serves too.
    assert not any(value in body for value in canaries), f"Private build value served from {path}"
    served += 1
    return body, response.headers

routes = {"/", "/accounts", "/imports", "/transactions", "/settings", "/onboarding", "/login"}
for page in export.rglob("index.html"):
    relative = page.parent.relative_to(export).as_posix()
    if not relative.startswith("_next/"):
        routes.add("/" if relative == "." else "/" + relative)
assets = set()
for route in sorted(routes):
    for path in {route, route.rstrip("/") + "/", route + "?id=runtime-record", route + "?refresh=1"}:
        body, headers = get(path)
        assert "text/html" in headers.get("Content-Type", ""), path
        assert b"<html" in body, path
        page = Assets()
        page.feed(body.decode())
        assets.update(page.paths)
    body, _ = get(route, method="HEAD")
    assert not body, "HEAD returned content"
for asset in sorted(assets):
    body, headers = get(asset)
    assert body, asset
    if asset.startswith("/_next/static/"):
        assert "immutable" in headers.get("Cache-Control", ""), asset
body, headers = get("/api/health")
status = json.loads(body)
assert status["status"] == "ok" and status["database"] == "encrypted", status
assert status["cipherVersion"].startswith("4."), status
assert headers["Cache-Control"] == "no-store"
for route in ["/does-not-exist", "/api/does-not-exist"]:
    try:
        get(route)
        raise AssertionError(f"Missing route returned 200: {route}")
    except HTTPError as error:
        assert error.code == 404
        if route.startswith("/api/"):
            assert "application/json" in error.headers["Content-Type"]
print(f"Passed: {len(routes)} routes with direct navigation/refresh/query IDs, {len(assets)} assets, same-origin API, 404s, private canaries in the export and {served} served bodies. SQLCipher {status['cipherVersion']}.")

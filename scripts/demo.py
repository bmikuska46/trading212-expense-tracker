#!/usr/bin/env python3
"""Run a separate, fictional EUR ledger through the application's public API."""

import argparse
import calendar
import datetime as dt
import fcntl
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.request import HTTPCookieProcessor, ProxyHandler, Request, build_opener
from zoneinfo import ZoneInfo


ROOT = Path(__file__).resolve().parent.parent
DEMO = ROOT / ".local" / "demo"
DATABASE = DEMO / "data" / "budget.db"
KEY = DEMO / "secrets" / "database.key"
MARKER = DEMO / "complete.json"
VERSION = 1
EMAIL = "demo@example.com"
PASSWORD = "Demo-budget-2026!"
SUBSCRIPTIONS = (
    ("Stream Plus", "Entertainment", 1299, 1),
    ("Music Box", "Entertainment", 1099, 1),
    ("Fit Club", "Health", 3400, 1),
    ("Cloud Notes", "Software", 499, 1),
)


def month_date(value):
    try:
        date = dt.date.fromisoformat(value + "-01")
    except (ValueError, TypeError):
        raise argparse.ArgumentTypeError("Use a month as YYYY-MM") from None
    today = dt.datetime.now(ZoneInfo("Europe/Bratislava")).date().replace(day=1)
    if len(value) != 7 or not dt.date(2001, 1, 1) <= date <= today:
        raise argparse.ArgumentTypeError("Choose a month from 2001-01 through the current month")
    return date


def decimal(cents):
    return f"{cents // 100}.{cents % 100:02d}"


def reject_symlinks():
    for path in (ROOT / ".local", DEMO):
        if path.is_symlink():
            raise RuntimeError(f"Demo path must not be a symbolic link: {path}")
    if DEMO.exists():
        for path in DEMO.rglob("*"):
            if path.is_symlink():
                raise RuntimeError(f"Demo path must not be a symbolic link: {path}")


def private_file(path, content):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as file:
        file.write(content)
        file.flush()
        os.fsync(file.fileno())


def existing_demo(requested_month):
    if not MARKER.exists():
        if any(path.name != "runner.lock" for path in DEMO.iterdir()):
            raise RuntimeError("Existing or incomplete demo database has no completion marker. Data was left untouched. Use a separate checkout for a fresh demo.")
        return None
    try:
        marker = json.loads(MARKER.read_text())
        month_date(marker["month"])
        valid = (type(marker["version"]) is int and marker["version"] == VERSION
                 and type(marker["transactionCount"]) is int
                 and 300 <= marker["transactionCount"] <= 500)
    except (ValueError, KeyError, TypeError, argparse.ArgumentTypeError):
        valid = False
    if not valid or not DATABASE.is_file() or not KEY.is_file():
        raise RuntimeError("Demo completion marker or database is invalid. Data was left untouched.")
    if requested_month is not None and requested_month.strftime("%Y-%m") != marker["month"]:
        raise RuntimeError(f"Demo is already seeded for {marker['month']}. Reuse that month or use a separate checkout.")
    return marker


class API:
    def __init__(self, process, port):
        self.process = process
        self.origin = f"http://127.0.0.1:{port}"
        self.opener = build_opener(ProxyHandler({}), HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, path, data=None, timeout=20):
        if self.process.poll() is not None:
            raise RuntimeError(f"Demo server exited. See {DEMO / 'server.log'}")
        headers = {"Origin": self.origin, "Content-Type": "application/json"}
        body = None if data is None else json.dumps(data).encode()
        request = Request(self.origin + path, data=body, headers=headers)
        try:
            with self.opener.open(request, timeout=timeout) as response:
                result = json.load(response)
        except HTTPError as error:
            detail = error.read().decode(errors="replace")[:1000]
            raise RuntimeError(f"Demo API {path} returned {error.code}: {detail}") from None
        if self.process.poll() is not None:
            raise RuntimeError(f"Demo server exited. See {DEMO / 'server.log'}")
        return result

    def wait_ready(self):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            try:
                health = self.request("/api/health", timeout=1)
                if health.get("status") != "ok" or health.get("database") != "encrypted":
                    raise RuntimeError("Demo server did not report a healthy encrypted database")
                time.sleep(0.15)
                if self.process.poll() is not None:
                    raise RuntimeError(f"Demo server exited. See {DEMO / 'server.log'}")
                return
            except (URLError, TimeoutError, ConnectionError):
                time.sleep(0.1)
        raise RuntimeError(f"Demo server did not become ready. See {DEMO / 'server.log'}")


def seed(api, end):
    api.request("/api/setup", {"email": EMAIL, "password": PASSWORD, "timezone": "Europe/Bratislava"})
    accounts = {name: api.request("/api/accounts", {"name": name, "currency": "EUR"})["id"]
                for name in ("Everyday account", "Savings", "Cash")}
    categories = {name: api.request("/api/categories", {"name": name})["id"]
                  for name in ("Salary", "Housing", "Utilities", "Groceries", "Eating out", "Transport",
                               "Shopping", "Entertainment", "Health", "Software", "Household")}
    count = 0
    totals = {"income": 0, "expense": 0, "refund": 0}
    last_refund = None
    last_split = None
    today = dt.datetime.now(ZoneInfo("Europe/Bratislava")).date()

    def add(month, day, kind, cents, payee="", category=None, account="Everyday account", **extra):
        nonlocal count
        if month == today.replace(day=1):
            day = min(day, today.day)
        data = {"accountId": accounts[account], "date": month.replace(day=day).isoformat(),
                "kind": kind, "amount": decimal(cents), "payee": payee, **extra}
        if category:
            data["categoryId"] = categories[category]
        result = api.request("/api/transactions", data)
        count += 2 if kind == "transfer" else 1
        if month == end and kind in totals:
            totals[kind] += cents
        return result["id"]

    end_index = end.year * 12 + end.month - 1
    for index in range(12):
        year, month_index = divmod(end_index - 11 + index, 12)
        month = dt.date(year, month_index + 1, 1)
        add(month, 1, "income", 320000 + (index * 1700) % 20001, "Aurora Studio", "Salary")
        add(month, 2, "expense", 85000, "Maple Apartments", "Housing")
        add(month, 7, "expense", 8400 + (index * 317) % 2200, "City Energy", "Utilities")
        add(month, 11, "expense", 3200 + (index * 173) % 900, "Home Internet", "Utilities")
        for number, day in enumerate((3, 6, 9, 12, 16, 20, 24, 28)):
            add(month, day, "expense", 3700 + (index * 467 + number * 313) % 2300, "Green Market", "Groceries")
        for number, day in enumerate((2, 5, 10, 14, 21, 27)):
            add(month, day, "expense", 780 + (index * 131 + number * 73) % 700,
                "Corner Cafe", "Eating out", account="Cash")
        add(month, 4, "expense", 4200 + (index % 3) * 150, "Metro Pass", "Transport")
        add(month, 13, "expense", 3900 + (index * 631) % 4200, "Studio Goods", "Shopping")
        purchase = add(month, 18, "expense", 7900 + (index * 397) % 5500, "Studio Goods", "Shopping")
        for number, day in enumerate((15, 26)):
            add(month, day, "expense", 1490 + (index * 113 + number * 271) % 600, "Cinema Club", "Entertainment")
        for payee, category, cents, day in SUBSCRIPTIONS:
            if payee == "Stream Plus" and index < 9:
                cents = 999
            add(month, day, "expense", cents, payee, category)
        if index < 9:
            add(month, 1, "expense", 750, "Paper Press", "Entertainment")
        add(month, 6, "transfer", 75000, toAccountId=accounts["Savings"], notes="Monthly savings")
        add(month, 9, "transfer", 18000, toAccountId=accounts["Cash"], notes="Cash for the month")
        if month == end:
            last_refund = add(month, 23, "refund", 2990, "Studio Goods", "Shopping",
                              refundOf=purchase, notes="Returned a desk accessory")
            last_split = add(month, 25, "expense", 8640, "Studio Goods", notes="Home supplies and a desk lamp",
                             splits=[{"categoryId": categories["Household"], "amount": "34.90"},
                                     {"categoryId": categories["Shopping"], "amount": "51.50"}])

    all_accounts = api.request("/api/accounts")["accounts"]
    transactions = api.request("/api/transactions?pageSize=1")
    month_name = end.strftime("%Y-%m")
    dashboard = api.request(f"/api/dashboard?month={month_name}")
    last_day = calendar.monthrange(end.year, end.month)[1]
    stats = api.request(f"/api/statistics?from={end.isoformat()}&to={end.replace(day=last_day).isoformat()}")
    subscriptions = api.request("/api/subscriptions")["subscriptions"]
    expected = {"incomeEurCents": totals["income"], "expenseEurCents": totals["expense"],
                "refundEurCents": totals["refund"], "spendingEurCents": totals["expense"] - totals["refund"],
                "netEurCents": totals["income"] - totals["expense"] + totals["refund"]}
    if (len(all_accounts) != 3 or transactions["total"] != count
            or sum(account["transactionCount"] for account in all_accounts) != count):
        raise RuntimeError("Demo account or transaction counts did not reconcile")
    if any(dashboard.get(key) != value or stats.get(key) != value for key, value in expected.items()):
        raise RuntimeError("Demo dashboard and register totals did not reconcile")
    if len(dashboard["trend"]) != 12 or not all(item["incomeEurCents"] > 0 for item in dashboard["trend"]):
        raise RuntimeError("Demo dashboard is missing its twelve-month history")
    recurring = {item["payeeName"]: item for item in subscriptions}
    if any(payee not in recurring or recurring[payee]["months"] != 12
           or recurring[payee]["amount"] != decimal(cents) for payee, _, cents, _ in SUBSCRIPTIONS):
        raise RuntimeError("Demo subscriptions were not detected")
    refund = api.request(f"/api/transactions?kind=refund")["transactions"]
    splits = api.request("/api/transactions?q=Home%20supplies")["transactions"]
    transfers = api.request("/api/transactions?kind=transfer&pageSize=200")
    if (len(refund) != 1 or refund[0]["id"] != last_refund or refund[0]["refundOf"] is None
            or not any(item["id"] == last_split and item["splitCount"] == 2 for item in splits)
            or transfers["total"] != 48 or any(not item["transferAccountName"] for item in transfers["transactions"])):
        raise RuntimeError("Demo refund, split, or paired transfers did not verify")
    return {"version": VERSION, "month": month_name, "transactionCount": count}


def stop_server(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def run(args):
    reject_symlinks()
    DEMO.mkdir(parents=True, exist_ok=True, mode=0o700)
    lock_fd = os.open(DEMO / "runner.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(lock_fd, "w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("Another demo runner owns this checkout. Stop it before starting another.") from None
        reject_symlinks()
        marker = existing_demo(args.month)
        month = month_date(marker["month"]) if marker else args.month or month_date(
            dt.datetime.now(ZoneInfo("Europe/Bratislava")).strftime("%Y-%m"))
        server = ROOT / ".build" / "server"
        if not server.is_file() or not os.access(server, os.X_OK) or not (ROOT / "web" / "out" / "index.html").is_file():
            raise RuntimeError("Build the server and frontend first. Follow the README demo instructions.")
        with socket.socket() as probe:
            probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                probe.bind(("127.0.0.1", args.port))
            except OSError:
                raise RuntimeError(f"Port {args.port} is occupied. Choose another port with --port.") from None
        DATABASE.parent.mkdir(mode=0o700, exist_ok=True)
        KEY.parent.mkdir(mode=0o700, exist_ok=True)
        if not KEY.exists():
            private_file(KEY, secrets.token_hex(32) + "\n")
        try:
            if len(KEY.read_text().strip()) != 64 or len(bytes.fromhex(KEY.read_text().strip())) != 32:
                raise ValueError()
        except ValueError:
            raise RuntimeError("Demo encryption key is invalid. Data was left untouched.") from None
        KEY.chmod(0o600)
        environment = {key: value for key, value in os.environ.items()
                       if not key.startswith("APP_") and key.upper() not in
                       ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY")}
        environment.update(APP_LISTEN_ADDR=f"127.0.0.1:{args.port}", APP_DATABASE_PATH=str(DATABASE),
                           APP_DATABASE_KEY_FILE=str(KEY), APP_WEB_DIR=str(ROOT / "web" / "out"),
                           APP_COOKIE_SECURE="never", APP_TRUSTED_PROXY_CIDRS="",
                           APP_TURNSTILE_SITE_KEY="1x00000000000000000000AA",
                           APP_TURNSTILE_SECRET_KEY="1x0000000000000000000000000000000AA")
        log_fd = os.open(DEMO / "server.log", os.O_WRONLY | os.O_CREAT | os.O_APPEND | os.O_NOFOLLOW, 0o600)
        with os.fdopen(log_fd, "a") as log:
            process = subprocess.Popen([str(server)], cwd=ROOT, env=environment, stdout=log, stderr=log)
            try:
                api = API(process, args.port)
                api.wait_ready()
                if marker is None:
                    print("Creating fictional demo data...", flush=True)
                    marker = seed(api, month)
                    private_file(MARKER, json.dumps(marker, indent=2) + "\n")
                print(f"Fictional demo: {api.origin}\nEmail: {EMAIL}\nPassword: {PASSWORD}", flush=True)
                print(f"Seeded month: {marker['month']}; transactions: {marker['transactionCount']}", flush=True)
                print(f"Database: {DATABASE}", flush=True)
                if not args.seed_only:
                    print("Press Ctrl+C to stop the demo server.", flush=True)
                    if process.wait() != 0:
                        raise RuntimeError(f"Demo server exited. See {DEMO / 'server.log'}")
            finally:
                stop_server(process)


def handle_stop(*_):
    raise KeyboardInterrupt()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=8088, help="Loopback port (default 8088)")
    parser.add_argument("--month", type=month_date, help="Last seeded month as YYYY-MM (default current Bratislava month)")
    parser.add_argument("--seed-only", action="store_true", help="Seed or reuse the demo, then stop the server")
    args = parser.parse_args()
    if not 1024 <= args.port <= 65535:
        parser.error("Choose a port from 1024 through 65535")
    os.umask(0o077)
    signal.signal(signal.SIGTERM, handle_stop)
    try:
        run(args)
    except KeyboardInterrupt:
        print("Demo server stopped.")
    except (OSError, RuntimeError, ValueError) as error:
        print(f"Demo failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

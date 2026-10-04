#!/usr/bin/env python3
"""Run the exported workflows in a private n8n runtime against the opt-in Go fixture.

This deliberately rejects remote databases and non-fixture API credentials.
It does not start PostgreSQL, construct AWS clients, or modify a production n8n.
"""
import argparse
import hashlib
import hmac
import json
import os
from pathlib import Path
import socket
import subprocess
import time
from urllib.error import HTTPError
from urllib.parse import urlparse
from urllib.request import Request, urlopen


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", type=Path, required=True)
    parser.add_argument("--fixture", type=Path, required=True)
    parser.add_argument("--state-url", required=True)
    parser.add_argument("--psql", default="psql")
    args = parser.parse_args()
    fixture = json.loads(args.fixture.read_text())
    database = urlparse(args.state_url)
    assert database.hostname == "127.0.0.1"
    assert database.path.startswith("/mailat_n8n_")
    assert urlparse(fixture["fixtureUrl"]).hostname == "127.0.0.1"
    assert fixture["token"] == "n8n-disposable-fixture-key"
    root = Path(__file__).resolve().parent
    runtime = args.runtime.resolve()
    binary = runtime / "node_modules/.bin/n8n"
    port, broker = free_port(), free_port()
    env = dict(os.environ, N8N_USER_FOLDER=str(runtime / "acceptance-user"),
               DB_TYPE="postgresdb", DB_POSTGRESDB_HOST="127.0.0.1",
               DB_POSTGRESDB_PORT=str(database.port), DB_POSTGRESDB_USER=database.username,
               DB_POSTGRESDB_PASSWORD=database.password or "", DB_POSTGRESDB_DATABASE=database.path[1:],
               N8N_DIAGNOSTICS_ENABLED="false", N8N_VERSION_NOTIFICATIONS_ENABLED="false",
               N8N_TEMPLATES_ENABLED="false", N8N_PERSONALIZATION_ENABLED="false",
               N8N_ENCRYPTION_KEY="mailat-disposable-acceptance-encryption-key",
               NODE_FUNCTION_ALLOW_BUILTIN="crypto", N8N_BLOCK_ENV_ACCESS_IN_NODE="false",
               N8N_LISTEN_ADDRESS="127.0.0.1", N8N_HOST="127.0.0.1", N8N_PROTOCOL="http",
               N8N_PORT=str(port), N8N_RUNNERS_BROKER_PORT=str(broker), N8N_SECURE_COOKIE="false",
               MAILAT_API_URL=fixture["baseUrl"], MAILAT_WEBHOOK_SECRET=fixture["secret"],
               MAILAT_FROM="sales@n8n.test", MAILAT_TO="recipient@external.test",
               MAILAT_SEND_KEY="n8n-single-business-1", MAILAT_BATCH_KEY="n8n-batch-business-1")
    # All credentials below are disposable fixture credentials; no application
    # secrets are exported into workflow JSON or the final evidence file.
    credentials = [
        {"id": "MAILAT_API_CREDENTIAL", "name": "Mailat API token", "type": "httpHeaderAuth",
         "data": {"name": "Authorization", "value": "Bearer " + fixture["token"]}},
        {"id": "MAILAT_STATE_CREDENTIAL", "name": "Mailat automation state", "type": "postgres",
         "data": {"host": "127.0.0.1", "port": database.port, "database": database.path[1:],
                  "user": database.username, "password": database.password or "",
                  "ssl": "disable", "maxConnections": 4}},
    ]
    credential_file = runtime / "acceptance-credentials.json"
    credential_file.write_text(json.dumps(credentials))
    credential_file.chmod(0o600)

    def cli(log, *command):
        path = runtime / log
        with path.open("w") as output:
            result = subprocess.run([str(binary), *command], env=env, stdout=output,
                                    stderr=subprocess.STDOUT, timeout=120)
        if result.returncode:
            raise RuntimeError(f"n8n failed; inspect {path}")
        return path.read_text()

    subprocess.run([args.psql, args.state_url, "-v", "ON_ERROR_STOP=1", "-f", str(root / "state.sql")],
                   check=True, stdout=subprocess.DEVNULL)
    cli("acceptance-import-credentials.log", "import:credentials", "--input=" + str(credential_file))
    for name in ("incoming-mail", "send-and-batch"):
        cli("acceptance-import-" + name + ".log", "import:workflow", "--input=" + str(root / (name + ".json")))
    cli("acceptance-publish.log", "publish:workflow", "--id=mailatIncomingV1")

    process = None
    def stop():
        nonlocal process
        if process is not None:
            process.terminate()
            try:
                process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            process = None

    def start(index):
        nonlocal process
        with (runtime / f"acceptance-server-{index}.log").open("w") as output:
            process = subprocess.Popen([str(binary), "start"], env=env, stdout=output, stderr=subprocess.STDOUT)
        for _ in range(120):
            if process.poll() is not None:
                raise RuntimeError("n8n stopped during startup; inspect server log")
            try:
                with urlopen(f"http://127.0.0.1:{port}/healthz/readiness", timeout=1) as response:
                    if response.status == 200:
                        return
            except Exception:
                time.sleep(0.25)
        raise RuntimeError("n8n readiness timeout")

    def state():
        return json.load(urlopen(fixture["fixtureUrl"] + "/fixture/state", timeout=5))["data"]

    def webhook(mode="valid", startup_attempt=0):
        body = json.dumps(fixture["event"], separators=(",", ":")).encode()
        stamp = str(int(time.time()) - (600 if mode == "stale" else 0))
        signature = hmac.new(fixture["secret"].encode(), stamp.encode() + b"." + body, hashlib.sha256).hexdigest()
        if mode == "tampered":
            body += b" "  # Still valid JSON; only the signed raw bytes differ.
        request = Request(f"http://127.0.0.1:{port}/webhook/mailat-incoming", data=body,
                          headers={"Content-Type": "application/json", "X-Webhook-ID": fixture["event"]["id"],
                                   "X-Webhook-Signature": "t=" + stamp + ",v1=" + signature})
        try:
            with urlopen(request, timeout=30) as response:
                return response.status, json.load(response)
        except HTTPError as error:
            # Readiness can precede publication reconciliation on a fresh n8n.
            if error.code == 404 and startup_attempt < 40:
                time.sleep(0.25)
                return webhook(mode, startup_attempt + 1)
            return error.code, error.read().decode()

    evidence = {"n8nVersion": subprocess.check_output([str(binary), "--version"], text=True).strip()}
    try:
        start(1)
        tampered = webhook("tampered")
        assert tampered[0] == 401, tampered
        assert webhook("stale")[0] == 401
        assert state()["calls"] == 0
        assert webhook() == (200, {"accepted": True})
        assert state()["calls"] == 1
        assert webhook() == (200, {"accepted": True, "duplicate": True})
        assert state()["downloads"] == 1
        stop()
        start(2)
        assert webhook() == (200, {"accepted": True, "duplicate": True})
        assert state()["calls"] == 1
        stop()
        evidence["incoming"] = ["tampered raw bytes rejected", "expired signature rejected", "private attachment downloaded",
                                "alias reply sent", "label applied", "archived without marking read", "duplicate survives n8n restart"]
        results = []
        for index in (1, 2):
            raw = cli(f"acceptance-send-{index}.log", "execute", "--id=mailatSendV1", "--rawOutput")
            result = None
            for pos, char in enumerate(raw):
                if char == "{":
                    try:
                        candidate, _ = json.JSONDecoder().raw_decode(raw[pos:])
                        if isinstance(candidate, dict) and candidate.get("finished") is True:
                            result = candidate
                            break
                    except ValueError:
                        pass
            assert result and result["status"] == "success"
            run = result["data"]["resultData"]["runData"]
            single = run["Submit single"][0]["data"]["main"][0][0]["json"]["data"]["id"]
            batch = run["Submit partial batch"][0]["data"]["main"][0][0]["json"]["data"]["results"]
            assert batch[2]["status"] == "failed" and not batch[2].get("id")
            results.append((single, [item.get("id") for item in batch]))
        assert results[0] == results[1], "retry changed submission receipts"
        final = state()
        assert final["calls"] == 4 and final["downloads"] == 1
        assert final["email"]["folder"] == "archive" and not final["email"]["isRead"]
        assert final["email"]["labels"] == ["Automated"]
        evidence["sendAndBatch"] = ["single attachment", "two accepted batch items", "one rejected item",
                                    "status reads", "stable UUIDs on full replay", "exactly four provider calls overall"]
        (runtime / "acceptance-results.json").write_text(json.dumps(evidence, indent=2) + "\n")
        urlopen(fixture["fixtureUrl"] + "/fixture/finish", timeout=5).close()
        print(json.dumps(evidence, indent=2))
    finally:
        stop()
        credential_file.unlink(missing_ok=True)


if __name__ == "__main__":
    main()

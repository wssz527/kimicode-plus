#!/usr/bin/env python3
"""M5 acceptance: the KCT adapter is gated by the standalone handover.

Runs the real core with `--data-dir` and an isolated HOME. The isolation is not
cosmetic: the migration reads os.UserHomeDir(), and this machine may well have a
real standalone installation in the user's own HOME. Pointing HOME at a temp
directory keeps the check deterministic and makes it impossible for this script
to touch the user's extension; the script also asserts that the real plist, if
present, is unchanged afterwards.

One branch cannot be exercised from here: actually unloading the job requires a
launchd job whose `path` is this temp HOME's plist. launchctl addresses a label in
the user's gui session, not in a directory, so that path is covered by the unit
tests in internal/kctmigration (fake launchctl). What this script proves is the
end-to-end contract the page depends on: detection, the refusal, the recorded
handover, and the adapter becoming enableable.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
LABEL = "com.user.kimi-cot-translate.runtime"
PLUGIN = "kimicode-plus.kct"
SETTINGS_KEY = "settings.v1"


class Core:
    def __init__(self, binary, folder, home):
        env = dict(os.environ)
        env["HOME"] = str(home)
        self.home = Path(home)
        self.process = subprocess.Popen(
            [str(binary), "--data-dir", str(folder)],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env,
        )
        runtime = Path(folder) / "runtime.json"
        deadline = time.monotonic() + 20
        while True:
            if self.process.poll() is not None:
                raise AssertionError(self.process.stderr.read())
            if runtime.exists():
                try:
                    data = json.loads(runtime.read_text())
                    if data.get("pid") == self.process.pid:
                        break
                except (FileNotFoundError, json.JSONDecodeError):
                    pass
            if time.monotonic() > deadline:
                self.process.kill()
                raise AssertionError("core readiness timeout")
            time.sleep(0.01)
        self.base, self.token = data["url"], data["pairingToken"]

    def request(self, path, body=None, method=None, token=True, headers=None, expected=200):
        values = {"Content-Type": "application/json"}
        if token:
            values["Authorization"] = "Bearer " + self.token
        values.update(headers or {})
        request = urllib.request.Request(
            self.base + "/enhance/v1" + path,
            data=None if body is None else json.dumps(body).encode(),
            method=method or ("GET" if body is None else "POST"),
            headers=values,
        )
        try:
            response = OPENER.open(request, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        payload = json.loads(response.read())
        assert response.status == expected, (path, response.status, payload)
        return payload

    def stop(self):
        if self.process.poll() is None:
            self.process.terminate()
            self.process.communicate(timeout=20)


def install_legacy(home, settings):
    agents = home / "Library" / "LaunchAgents"
    agents.mkdir(parents=True, exist_ok=True)
    (agents / (LABEL + ".plist")).write_text("<plist><dict/></plist>\n")
    data = home / "Library" / "Application Support" / "kimi-cot-translate"
    data.mkdir(parents=True, exist_ok=True)
    (data / "cli.json").write_text(json.dumps(settings))


def run(binary, root):
    results = []
    # platform.Acquire refuses a data directory that another user could read, and
    # mkdtemp already gives 0700; the nested directories must match.
    folder = root / "core"
    folder.mkdir(parents=True, exist_ok=True, mode=0o700)
    home = root / "home"
    home.mkdir(exist_ok=True, mode=0o700)
    real_plist = Path.home() / "Library" / "LaunchAgents" / (LABEL + ".plist")
    real_before = real_plist.read_bytes() if real_plist.exists() else None

    core = Core(binary, folder, home)
    try:
        items = {item["id"]: item for item in core.request("/plugins")["items"]}
        assert PLUGIN in items, sorted(items)
        adapter = items[PLUGIN]
        assert adapter["source"] == "BUNDLED" and adapter["enabled"] is False, adapter
        for capability in ("ui:thinking", "ui:settings", "storage"):
            assert capability in adapter["capabilities"], adapter
        assert adapter["license"] == "MIT", adapter
        results.append("the KCT adapter ships bundled and disabled, with its license")

        report = core.request("/kct/migration")
        assert report["pluginId"] == PLUGIN and report["detected"] is False, report
        assert report["needsConfirmation"] is False and report["migrated"] is False, report
        first = core.request("/kct/migration/confirm", {})
        assert first["stoppedLoader"] is False and first["alreadyMigrated"] is False, first
        assert "没有检测到" in first["message"], first
        results.append("without a standalone install the migration reports nothing to do")

        enabled = core.request("/plugins/" + PLUGIN + "/enable", {})
        print("本次验收启用的插件:", PLUGIN)
        assert enabled["state"] == "ENABLED" and enabled["token"], enabled
        token = enabled["token"]
        denied = core.request(
            "/plugins/" + PLUGIN + "/action/net.fetch",
            {"params": {"url": "https://example.com/", "method": "GET"}},
            headers={"X-Plugin-Token": token}, expected=403,
        )
        assert denied["error"]["code"] == "PLUGIN_CAPABILITY_DENIED", denied
        results.append("an enabled adapter reaches only the hosts its manifest declares")
        core.request("/plugins/" + PLUGIN + "/disable", {})

        install_legacy(home, {"provider": "bing", "targetLanguage": "ja", "customKey": "not-a-real-key"})
        plist = home / "Library" / "LaunchAgents" / (LABEL + ".plist")
        report = core.request("/kct/migration")
        assert report["detected"] is True and report["needsConfirmation"] is True, report
        assert report["plistPresent"] is True and report["dataDirPresent"] is True, report
        assert report["settings"] == {"provider": "bing", "targetLanguage": "ja"}, report["settings"]
        results.append("an installed standalone extension is detected with only its public settings")

        blocked = core.request("/plugins/" + PLUGIN + "/enable", {}, expected=409)
        assert blocked["error"]["code"] == "PLUGIN_MIGRATION_REQUIRED", blocked
        state = core.request("/plugins/" + PLUGIN)
        assert state["enabled"] is False and state["state"] == "BLOCKED", state
        activity = core.request("/plugins/" + PLUGIN + "/activity")
        assert activity["items"][0]["kind"] == "ENABLE_BLOCKED", activity
        results.append("the adapter cannot be enabled while the standalone loader is installed")

        confirmed = core.request("/kct/migration/confirm", {})
        assert confirmed["disabledPlist"] is True, confirmed
        assert confirmed["migratedKeys"] == ["provider", "targetLanguage"], confirmed
        assert not plist.exists(), "plist was left in place"
        assert (home / "Library" / "LaunchAgents" / (LABEL + ".plist.disabled-by-kimicode-plus")).exists()
        results.append("confirming moves the plist aside and records the migrated keys")

        reenabled = core.request("/plugins/" + PLUGIN + "/enable", {})
        assert reenabled["state"] == "ENABLED", reenabled
        envelope = core.request(
            "/plugins/" + PLUGIN + "/action/storage.get",
            {"params": {"key": SETTINGS_KEY}},
            headers={"X-Plugin-Token": reenabled["token"]},
        )
        stored = envelope["value"]
        assert stored["found"] is True, stored
        migrated = json.loads(stored["value"])
        assert migrated["provider"] == "bing" and migrated["targetLanguage"] == "ja", migrated
        assert "customKey" not in migrated and "customKey" not in json.dumps(migrated), migrated
        results.append("the public settings reach the adapter and the standalone key does not")

        again = core.request("/kct/migration/confirm", {})
        assert again["alreadyMigrated"] is True and again["stoppedLoader"] is False, again
        after = core.request("/kct/migration")
        assert after["migrated"] is True and after["needsConfirmation"] is False, after
        results.append("the handover is idempotent")

        (home / "Library" / "LaunchAgents" / (LABEL + ".plist")).write_text("<plist/>\n")
        later = core.request("/kct/migration")
        assert later["migrated"] is True and later["needsConfirmation"] is False, later
        core.request("/plugins/" + PLUGIN + "/enable", {})
        results.append("a recorded handover survives a reinstalled standalone plist")
    finally:
        core.stop()

    real_after = real_plist.read_bytes() if real_plist.exists() else None
    assert real_before == real_after, "the user's own standalone plist was modified"
    if real_before is not None:
        results.append("the user's own standalone installation was left untouched")
    return results


if __name__ == "__main__":
    binary = Path(sys.argv[1]).resolve()
    kept = Path(sys.argv[2]).resolve() if len(sys.argv) > 2 else None
    root = kept or Path(tempfile.mkdtemp(prefix="m5-acceptance-"))
    try:
        passed = run(binary, root)
        for text in passed:
            print("PASS " + text)
        print(json.dumps({"passed": len(passed), "failed": 0}, ensure_ascii=False))
    finally:
        if not kept:
            shutil.rmtree(root, ignore_errors=True)

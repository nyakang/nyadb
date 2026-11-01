#!/usr/bin/env python3
"""Replay native Linux acceptance inside an owned private fixture.

Every asset/restore assertion uses fresh processes and formal encrypted stores
or the fixture database. Screenshots themselves never claim Navicat O/V.
"""
import argparse
import hashlib
import os
from pathlib import Path
import subprocess
import time

from capture_alignment_gui_linux import NativeCapture
from verify_entry_acceptance import REPO, environment, manifest, spawn, terminate, validate


def narrow_fixture(root, state, env):
    fixture = root / "evidence/shell-native-fixture"
    subprocess.run(["go", "test", "-c", "-o", str(fixture), "./internal/ui/shell"], cwd=REPO, env=env, check=True)
    narrow = spawn(root, state, "narrow", [str(fixture)], dict(env, NYADB_ENTRY_NATIVE="1", GTK_THEME="Adwaita:dark"))
    cap = NativeCapture(env["DISPLAY"], root / "evidence/native", fixture, app_name="nyadb-shell-fixture")
    try:
        small = cap.wait_window("^NyaDB toolbar fixture$", pid=narrow.pid)
        cap.move_client(small, 40, 70)
        cap.resize(small, 420, 320)
        cap.click(small, 345, 60)
        time.sleep(.4)
        cap.actions.append({"action": "click", "client_x": 345, "client_y": 60})
        cap.capture("toolbar-420-overflow", small, "toolbar-420-overflow", {"loading": False, "component_fixture": True, "formal_minimum_width": 840})
        cap.click(small, 367, 151)
        time.sleep(.4)
        cap.actions.append({"action":"click","client_x":367,"client_y":151,"target":"备份"})
        cap.capture("toolbar-420-backup-menu",small,"toolbar-420-backup-menu",{"loading":False,"component_fixture":True,"formal_minimum_width":840})
    finally:
        cap.close()
        terminate(state["processes"].pop("narrow"))
        manifest(root, state)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--setup", action="store_true", help="retain a managed initial window for operator investigation")
    parser.add_argument("--narrow-only", action="store_true", help="repeat only the native toolbar component on an owned seeded config")
    args = parser.parse_args()
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    def check(phase):
        subprocess.run(["go", "run", "./tools/entryfixture", phase], cwd=REPO, env=env, check=True)
    if args.narrow_only:
        narrow_fixture(root, state, env)
        return
    if not (root / "config/NyaDB/config.sqlite").exists():
        check("seed")
    elif not args.setup:
        raise RuntimeError("Native replay requires a fresh seeded config; create a new owned run")
    if not args.setup:
        check("native-prepare")
    binary = REPO / "build/linux-amd64/nyadb"
    cap = NativeCapture(env["DISPLAY"], root / "evidence/native", binary)
    app = spawn(root, state, "app", [str(binary)], env)
    window = None
    def key(*keys):
        cap.actions.append({"action": "key", "keys": list(keys)})
        cap.key(*keys)
    def click(x, y, button=1):
        cap.actions.append({"action": "click", "client_x": x, "client_y": y, "button": button})
        cap.click(window, x, y, button)
        time.sleep(.2)
    def replace(value, secret=False):
        # xclip receives bytes through stdin, including the masked password.
        # No secret enters argv or the action log. This is the private X server.
        cap.actions.append({"action": "replace-input", "value": "<masked fixture password>" if secret else value})
        subprocess.run(["xclip", "-selection", "clipboard"], input=value.encode(), env=env, check=True)
        key("ctrl+a", "ctrl+v")
        time.sleep(.15)
    def capture(name, **observed):
        cap.capture(name, window, name, dict(loading=False, **observed))
    def start_window():
        nonlocal window
        window = cap.wait_window("^NyaDB$", pid=app.pid)
        cap.move_client(window, 30, 50)
        cap.resize(window, 1280, 800)
        time.sleep(1)
    def file_dialog(title, path):
        cap.wait_window(title, pid=app.pid)
        key("ctrl+l")
        replace(str(path))
        key("Return")
        time.sleep(.4)
        key("Return")
        time.sleep(1)
    try:
        start_window()
        capture("query-restored-1280", source="real Secret Service and encrypted workspace", process_restart=True)
        if args.setup:
            return
        click(105, 170)  # qualified fixture profile, sorted after SQLite/read-only
        click(490, 60)
        capture("query-manager-offline", offline=True)
        click(330, 197)
        key("F2")
        time.sleep(.4)
        capture("query-f2", key_backend="XTest", modifiers=0, expected_dialog="重命名查询")
        replace("已重命名查询")
        click(515, 430)
        check("native-renamed")
        capture("query-renamed", dirty_text_preserved=True)
        click(330, 197)
        key("Delete")
        time.sleep(.3)
        capture("query-delete-confirm")
        click(515, 441)
        check("native-deleted")
        click(380, 110)
        capture("query-deleted-draft", unnamed=True, retained_text=True)
        click(785, 137)
        replace("删除后新查询")
        click(535, 431)
        check("native-saved")
        click(490, 60)
        click(943, 141)
        click(967, 166)
        replace("原生新增组")
        click(515, 430)
        time.sleep(.5)
        click(330, 197, 3)
        click(435, 253)
        click(580, 299)
        check("native-grouped")
        capture("query-grouped", group="原生新增组")
        # All documents are now saved. Restart the same formal binary/config.
        key("alt+F4")
        try:
            app.wait(timeout=10)
        except subprocess.TimeoutExpired:
            raise RuntimeError("Formal window did not close after saved workspace")
        state["processes"].pop("app")
        cap.actions.append({"action": "restart-formal-binary", "same_config": True})
        app = spawn(root, state, "app", [str(binary)], env)
        start_window()
        check("native-grouped")
        capture("query-after-restart", real_vault=True, named_query_and_group_restored=True)
        click(105, 170)
        click(490, 60)
        cap.resize(window, 840, 560)
        capture("formal-840", formal_minimum_width=840)
        cap.resize(window, 1280, 800)
        click(1118, 276)  # explicit original-profile connect via OSVault
        time.sleep(2)
        click(115, 234)  # entry_source, after entry_dump and native target
        click(115, 234)
        time.sleep(1)
        click(295, 60)
        capture("functions-native", real_mysql=True)
        click(360, 60)
        time.sleep(1)
        capture("accounts-read", real_mysql=True)
        click(425, 185)
        replace("entry_native_account")
        click(650, 218)
        key("Home", "Down", "Down", "Return")
        click(550, 252)
        replace(env["NYADB_ENTRY_READ_PASSWORD"], secret=True)
        click(946, 700)
        time.sleep(.5)
        check("native-account-created")
        # A receipt adds one message row above the inputs.
        subprocess.run(["xclip", "-selection", "clipboard"], input=b"", env=env, check=True)
        capture("accounts-created", operation="create_user", verified_from_database=True)
        click(650, 245)
        key("Home", "Return")
        click(946, 700)
        time.sleep(.5)
        check("native-account-granted")
        click(825, 700)
        time.sleep(.7)
        capture("accounts-granted", operation="grant", verified_from_database=True)
        click(660, 700)
        click(555, 60)
        click(606, 104)
        click(641, 119)
        archive = root / "evidence/native.nyabackup"
        file_dialog("保存备份", archive)
        if not archive.is_file():
            raise RuntimeError("Native backup did not publish the chosen output")
        capture("backup-completed", output_sha256=hashlib.sha256(archive.read_bytes()).hexdigest())
        click(285, 180)
        click(555, 60)
        click(605, 126)
        click(600, 95)
        replace("entry_native_target")
        click(641, 164)
        file_dialog("恢复 NyaDB 备份", archive)
        capture("restore-preview", target="entry_native_target", execution_confirmed=False)
        click(641, 505)
        time.sleep(.5)
        check("native-restore")
        capture("restore-result", target="entry_native_target", verified_from_database=True)
        # Separate native component fixture; formal minimum remains 840.
        narrow_fixture(root, state, env)
    finally:
        cap.close()
        if not args.setup:
            if "app" in state["processes"]:
                terminate(state["processes"].pop("app"))
            manifest(root, state)


if __name__ == "__main__":
    main()

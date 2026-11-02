#!/usr/bin/env python3
"""在临时目录验证过滤边界；不调用设备、播放器或实际 ubus。"""
import os
from pathlib import Path
import subprocess
import tempfile
import time

with tempfile.TemporaryDirectory(prefix="native-focus-test-") as directory:
    root = Path(directory)
    original = root / "ubus-original"
    original.write_text('#!/bin/sh\nprintf "透传\\n"\n')
    original.chmod(0o700)
    script = Path(__file__).with_name("ubus-filter.sh").read_text()
    script = script.replace("/tmp/xiaoai-native-guard", str(root))
    script = script.replace("/data/open-xiaoai/ubus.native-guard.original", str(original))
    script = script.replace("$(pidof mico_aivs_lab)", str(os.getpid()))
    target = root / "filter.sh"
    target.write_text(script)
    (root / "ready").write_text(f"{os.getpid()}\n")

    def check(args, expected):
        result = subprocess.run(["sh", str(target), *args], text=True, capture_output=True, check=True)
        assert result.stdout.strip() == expected, (args, result.stdout, result.stderr)

    def policy(expiry, owner=None):
        (root / "policy").write_text(f"1\n{owner or os.getpid()}\n{expiry}\n")

    start = ["-t", "1", "call", "mediaplayer", "player_wakeup", '{"action":"start"}']
    check(start, "透传")
    policy(int(time.time()) + 12)
    check(start, '{"code":0}')
    for action in ["stop", "multistart"]:
        check(["call", "mediaplayer", "player_wakeup", '{"action":"' + action + '"}'], '{"code":0}')
    check(["call", "mediaplayer", "player_wakeup", '{"action":"stopalarm"}'], "透传")
    check(["call", "mediaplayer", "player_play_operation", '{"action":"stop"}'], "透传")
    check(["call", "mediaplayer", "player_get_play_status"], "透传")
    policy(int(time.time()) - 1)
    check(start, "透传")
    policy(int(time.time()) + 12, 2147483647)
    check(start, "透传")
    policy(int(time.time()) + 120)
    check(start, "透传")
    print("通过：唤醒焦点过滤 10 项边界用例")

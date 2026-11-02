#!/usr/bin/env python3
"""用可回滚的假挂载/进程验证开机状态机；不重启真实服务、不调用真实 mount。"""
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).parent

class PersistenceTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="native-boot-test-")
        self.root = Path(self.temp.name)
        self.base = self.root / "data/open-xiaoai"
        for directory in [self.base, self.root / "bin", self.root / "etc/init.d", self.root / "tmp", self.root / "usr/bin", self.root / "usr/lib", self.root / "proc", self.root / "mockbin"]:
            directory.mkdir(parents=True, exist_ok=True)
        (self.root / "proc/mounts").write_text("")
        (self.root / "aivs.pid").write_text("100")
        self.env = dict(os.environ, OPEN_XIAOAI_DIR=str(self.base), OPEN_XIAOAI_NATIVE_ROOT=str(self.root), TEST_ROOT=str(self.root))
        self.env["PATH"] = str(self.root / "mockbin") + os.pathsep + self.env["PATH"]
        self.init = self.root / "etc/init.d/mico_aivs_lab"
        self.init.write_text('''#!/bin/sh
procd_set_param() { :; }
if [ "${1:-}" = restart ]; then
    echo restart >> "$TEST_ROOT/restarts"
    pid=$(cat "$TEST_ROOT/aivs.pid")
    pid=$((pid + 1))
    echo "$pid" > "$TEST_ROOT/aivs.pid"
    mkdir -p "$TEST_ROOT/proc/$pid" "$TEST_ROOT/tmp/xiaoai-native-guard"
    if grep -q '^    procd_set_param env LD_PRELOAD=' "$0" && [ ! -f "$TEST_ROOT/fail-ready" ]; then
        echo "$OPEN_XIAOAI_DIR/native_guard.so" > "$TEST_ROOT/proc/$pid/maps"
        echo "$pid" > "$TEST_ROOT/tmp/xiaoai-native-guard/ready"
    fi
fi
procd_set_param command /usr/bin/mico_aivs_lab
''')
        self.init.chmod(0o755)
        (self.root / "bin/ubus").write_text("原始 ubus 二进制")
        (self.root / "usr/bin/mico_aivs_lab").write_text("aivs 固件")
        (self.root / "usr/lib/libaivs_sdk.so").write_text("SDK 固件")
        (self.base / "native_guard.so").write_text("已验证钩子")
        (self.base / "native-guard-ubus-filter.sh").write_text((SOURCE / "ubus-filter.sh").read_text())
        for name, body in {
            "pidof": '#!/bin/sh\ncat "$TEST_ROOT/aivs.pid"\n',
            "sleep": '''#!/bin/sh
if { [ -f "$TEST_ROOT/bounce-ready" ] || [ -f "$TEST_ROOT/flap-ready" ]; } && [ -f "$TEST_ROOT/tmp/xiaoai-native-guard/ready" ]; then
    count=$(cat "$TEST_ROOT/sleeps" 2>/dev/null || echo 0)
    count=$((count + 1))
    echo "$count" > "$TEST_ROOT/sleeps"
    if [ "$count" = 2 ] || [ -f "$TEST_ROOT/flap-ready" ]; then
        pid=$(cat "$TEST_ROOT/aivs.pid")
        pid=$((pid + 1))
        echo "$pid" > "$TEST_ROOT/aivs.pid"
        mkdir -p "$TEST_ROOT/proc/$pid"
        echo "$OPEN_XIAOAI_DIR/native_guard.so" > "$TEST_ROOT/proc/$pid/maps"
        echo "$pid" > "$TEST_ROOT/tmp/xiaoai-native-guard/ready"
    fi
fi
exit 0
''',
            "mount": '''#!/bin/sh
set -eu
src=$3; dst=$4
cp -p "$dst" "$dst.before-mount"
cp -p "$src" "$dst"
echo "tmpfs $dst tmpfs rw 0 0" >> "$TEST_ROOT/proc/mounts"
''',
            "umount": '''#!/bin/sh
set -eu
cp -p "$1.before-mount" "$1"
awk -v target="$1" '$2!=target' "$TEST_ROOT/proc/mounts" > "$TEST_ROOT/proc/mounts.new"
mv "$TEST_ROOT/proc/mounts.new" "$TEST_ROOT/proc/mounts"
''',
        }.items():
            path = self.root / "mockbin" / name
            path.write_text(body)
            path.chmod(0o755)
        files = {"sdk": self.root / "usr/lib/libaivs_sdk.so", "aivs": self.root / "usr/bin/mico_aivs_lab", "init": self.init, "ubus": self.root / "bin/ubus", "hook": self.base / "native_guard.so", "filter": self.base / "native-guard-ubus-filter.sh"}
        (self.base / "native-guard.sha256").write_text("".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()} {name}\n" for name, path in files.items()))

    def tearDown(self):
        self.temp.cleanup()

    def run_guard(self, command, ok=True):
        result = subprocess.run(["sh", str(SOURCE / "native-guard.sh"), command], env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0 if ok else 1, result.stdout + result.stderr)
        return result.stdout

    def test_disabled_boot_does_not_mount_or_restart(self):
        self.run_guard("boot")
        self.assertEqual((self.root / "proc/mounts").read_text(), "")
        self.assertFalse((self.root / "restarts").exists())

    def test_enable_and_repeated_boot_are_idempotent(self):
        self.run_guard("enable")
        self.assertTrue((self.base / "native-guard.enabled").exists())
        self.assertEqual(len((self.root / "proc/mounts").read_text().splitlines()), 2)
        self.run_guard("boot")
        self.run_guard("enable")
        self.assertEqual((self.root / "restarts").read_text(), "restart\n")

    def test_cold_boot_rebuilds_mounts(self):
        self.run_guard("enable")
        self.run_guard("disable")
        (self.base / "native-guard.enabled").write_text("1\n")
        self.run_guard("boot")
        self.assertEqual(len((self.root / "proc/mounts").read_text().splitlines()), 2)
        self.assertIn("均已加载", self.run_guard("status"))

    def test_cold_boot_accepts_a_replaced_instance_after_it_stabilizes(self):
        (self.root / "bounce-ready").touch()
        self.run_guard("enable")
        self.assertEqual((self.root / "aivs.pid").read_text().strip(), "102")
        self.assertEqual((self.root / "restarts").read_text(), "restart\n")
        self.assertTrue((self.base / "native-guard.enabled").exists())

    def test_continuously_replaced_instances_fail_and_roll_back(self):
        (self.root / "flap-ready").touch()
        self.run_guard("enable", ok=False)
        self.assertFalse((self.base / "native-guard.enabled").exists())
        self.assertEqual((self.root / "proc/mounts").read_text(), "")
        self.assertTrue((self.base / "native-guard.failed").exists())

    def test_firmware_mismatch_fails_open_and_disables_retry(self):
        (self.base / "native-guard.enabled").write_text("1\n")
        (self.root / "usr/lib/libaivs_sdk.so").write_text("新固件")
        self.run_guard("boot", ok=False)
        self.assertFalse((self.base / "native-guard.enabled").exists())
        self.assertTrue((self.base / "native-guard.failed").exists())
        self.assertEqual((self.root / "proc/mounts").read_text(), "")
        self.run_guard("boot")
        self.assertFalse((self.root / "restarts").exists())

    def test_startup_failure_rolls_back_and_disables_retry(self):
        original_init = self.init.read_bytes()
        (self.root / "fail-ready").touch()
        self.run_guard("enable", ok=False)
        self.assertEqual((self.root / "proc/mounts").read_text(), "")
        self.assertEqual(self.init.read_bytes(), original_init)
        self.assertTrue((self.base / "native-guard.failed").exists())

    def test_disable_removes_lease_and_restores_both_files(self):
        original_init = self.init.read_bytes()
        original_ubus = (self.root / "bin/ubus").read_bytes()
        self.run_guard("enable")
        (self.root / "tmp/xiaoai-native-guard/policy").write_text("活动租约")
        self.run_guard("disable")
        self.assertFalse((self.base / "native-guard.enabled").exists())
        self.assertFalse((self.root / "tmp/xiaoai-native-guard/policy").exists())
        self.assertEqual(self.init.read_bytes(), original_init)
        self.assertEqual((self.root / "bin/ubus").read_bytes(), original_ubus)

    def test_foreign_mount_is_preserved(self):
        (self.root / "proc/mounts").write_text(f"foreign {self.root}/bin/ubus tmpfs rw 0 0\n")
        (self.root / "bin/ubus").write_text("其他人的覆盖")
        self.run_guard("enable", ok=False)
        self.assertIn("foreign", (self.root / "proc/mounts").read_text())
        self.assertEqual((self.root / "bin/ubus").read_text(), "其他人的覆盖")

if __name__ == "__main__":
    unittest.main()

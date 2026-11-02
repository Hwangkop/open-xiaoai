#!/usr/bin/env python3
"""生成已验证 OH2P 1.62.2 的加载清单；固件指纹不能从待部署设备自动信任。"""
import hashlib
from pathlib import Path
import sys

supported = {
    "sdk": "e480bcab16c85f8c2331463fdf54ca032bf95f48ec99af86380b0520ffdb1888",
    "aivs": "c8cf5aa0d5d542bfe8b16b645398e1632542cbc2c2d7e3d3e110446c9a711017",
    "init": "cc17eb3e7b4981098aa145c88804be57dbd931fb0cf7dd9ea19fafd7a432c819",
    "ubus": "c76182e5371f08dd85adc84b6443b76ab481528d6f5cb21b4b27b06f906c7cfe",
}
build = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("/tmp/xiaoai-native-guard-build")
for name, path in {
    "hook": build / "native_guard.so",
    "filter": Path(__file__).with_name("ubus-filter.sh"),
}.items():
    supported[name] = hashlib.sha256(path.read_bytes()).hexdigest()
output = build / "native-guard.sha256"
output.write_text("".join(f"{value} {name}\n" for name, value in supported.items()))
print(output)

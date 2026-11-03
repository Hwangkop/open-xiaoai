#!/usr/bin/env python3
"""下载并核验作者公开原书；仅用于准备知识库，不是音箱运行时依赖。"""

import argparse
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import shutil
import tempfile
import urllib.request
import zipfile


ROOT = Path(__file__).resolve().parents[2]
BOOK = ROOT / "knowledge/books/time-as-a-friend"
MAX_ARCHIVE_BYTES = 64 * 1024 * 1024


def prepare(manifest, archive, destination):
    """先检查每个 Git blob，再原样落盘；不拼接不可信的压缩包路径。"""
    prefix = "time-as-a-friend-" + manifest["revision"] + "/"
    with zipfile.ZipFile(io.BytesIO(archive)) as zipped:
        for name, info in manifest["files"].items():
            relative = PurePosixPath(name)
            if relative.is_absolute() or ".." in relative.parts or "\\" in name:
                raise ValueError("原文清单含非法路径")
            member = zipped.getinfo(prefix + name)
            if member.file_size != info["bytes"]:
                raise ValueError("原文长度不符：" + name)
            data = zipped.read(member)
            header = b"blob " + str(len(data)).encode("ascii") + b"\0"
            if hashlib.sha1(header + data).hexdigest() != info["git_blob_sha1"]:
                raise ValueError("原文版本校验失败：" + name)
            target = destination.joinpath(*relative.parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)


def main():
    parser = argparse.ArgumentParser(description="下载《把时间当作朋友》固定版本原文")
    parser.add_argument("--archive", type=Path, help="使用已下载的官方 ZIP（仍核对全部原文件）")
    args = parser.parse_args()
    manifest = json.loads((BOOK / "source.json").read_text(encoding="utf-8"))
    if args.archive:
        if args.archive.stat().st_size > MAX_ARCHIVE_BYTES:
            raise ValueError("原书压缩包超出大小限制")
        archive = args.archive.read_bytes()
    else:
        with urllib.request.urlopen(manifest["archive_url"], timeout=90) as response:
            archive = response.read(MAX_ARCHIVE_BYTES + 1)
        if len(archive) > MAX_ARCHIVE_BYTES:
            raise ValueError("原书压缩包超出大小限制")
    original = BOOK / "original"
    if original.exists():
        raise FileExistsError("原文目录已经存在；为保护本地副本，不自动覆盖：" + str(original))
    with tempfile.TemporaryDirectory(prefix="book-download-", dir=BOOK) as temporary:
        prepared = Path(temporary) / "original"
        prepare(manifest, archive, prepared)
        shutil.move(str(prepared), original)
    print("📚 原文已下载并逐文件核验：" + str(original))
    print("📚 共 %d 个文件，版本 %s" % (len(manifest["files"]), manifest["revision"]))


if __name__ == "__main__":
    main()

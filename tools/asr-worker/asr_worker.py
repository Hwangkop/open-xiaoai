#!/usr/bin/env python3
"""常驻 ASR 工作进程：读 stdin 的 JSON 行 {"wav": "/path"}，输出 {"text": "..."}。

引擎可选（环境变量 ASR_ENGINE）：
    sensevoice  阿里 SenseVoice（默认，准确、极快，非流式）
    paraformer  阿里 Paraformer-zh（中文短句更稳，需 model.onnx + tokens.txt）
    zipformer   sherpa-onnx 流式 zipformer（体积小，准确率一般）

模型目录（ASR_MODEL_DIR）：
    sensevoice: model.int8.onnx + tokens.txt
    paraformer: model.onnx + tokens.txt
    zipformer:  encoder/decoder/joiner + tokens.txt

模型下载（HF 镜像）：
    SenseVoice:  https://hf-mirror.com/csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17
    zipformer:   https://hf-mirror.com/csukuangfj/sherpa-onnx-streaming-zipformer-zh-14M-2023-02-23
"""

import json
import os
import sys
import time
import wave

# Windows 上 Python 默认用 GBK 输出，父进程（Go）按 UTF-8 解析会变乱码。
# 这里强制 stdin/stdout 走 UTF-8。
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stdin.reconfigure(encoding="utf-8")

import numpy as np
import sherpa_onnx

ENGINE = os.environ.get("ASR_ENGINE", "sensevoice").lower()
MODEL_DIR = os.environ.get("ASR_MODEL_DIR", "/tmp/sensevoice")
# 备用模型（Paraformer）：只在请求里带 "alt": true 时使用，用于唤醒词二次识别
ALT_MODEL_DIR = os.environ.get("ASR_ALT_MODEL_DIR", "")


def build_recognizer():
    t0 = time.time()
    if ENGINE == "paraformer":
        rec = sherpa_onnx.OfflineRecognizer.from_paraformer(
            paraformer=os.path.join(MODEL_DIR, "model.onnx"),
            tokens=os.path.join(MODEL_DIR, "tokens.txt"),
            num_threads=2,
            sample_rate=16000,
            feature_dim=80,
            decoding_method="greedy_search",
            provider="cpu",
        )
    elif ENGINE == "zipformer":
        rec = sherpa_onnx.OnlineRecognizer.from_transducer(
            tokens=os.path.join(MODEL_DIR, "tokens.txt"),
            encoder=os.path.join(MODEL_DIR, "encoder-epoch-99-avg-1.int8.onnx"),
            decoder=os.path.join(MODEL_DIR, "decoder-epoch-99-avg-1.int8.onnx"),
            joiner=os.path.join(MODEL_DIR, "joiner-epoch-99-avg-1.int8.onnx"),
            num_threads=2,
            sample_rate=16000,
            feature_dim=80,
            decoding_method="greedy_search",
            provider="cpu",
        )
    else:
        rec = sherpa_onnx.OfflineRecognizer.from_sense_voice(
            model=os.path.join(MODEL_DIR, "model.int8.onnx"),
            tokens=os.path.join(MODEL_DIR, "tokens.txt"),
            num_threads=2,
            use_itn=True,
            language="zh",
        )
    print(f"[asr] 引擎={ENGINE} 加载耗时={time.time()-t0:.2f}s", file=sys.stderr, flush=True)
    return rec


def build_alt_recognizer():
    """备用 Paraformer；没配置或模型不存在就返回 None。"""
    if not ALT_MODEL_DIR:
        return None
    model = os.path.join(ALT_MODEL_DIR, "model.onnx")
    tokens = os.path.join(ALT_MODEL_DIR, "tokens.txt")
    if not (os.path.exists(model) and os.path.exists(tokens)):
        print(f"[asr] 备用模型不完整，跳过: {ALT_MODEL_DIR}", file=sys.stderr, flush=True)
        return None
    t0 = time.time()
    rec = sherpa_onnx.OfflineRecognizer.from_paraformer(
        paraformer=model, tokens=tokens, num_threads=2,
        sample_rate=16000, feature_dim=80,
        decoding_method="greedy_search", provider="cpu")
    print(f"[asr] 备用模型 paraformer 加载耗时={time.time()-t0:.2f}s", file=sys.stderr, flush=True)
    return rec


def read_wav(path):
    with wave.open(path, "rb") as w:
        rate = w.getframerate()
        samples = np.frombuffer(
            w.readframes(w.getnframes()), dtype=np.int16
        ).astype(np.float32) / 32768.0
    return rate, samples


def transcribe(rec, path):
    rate, samples = read_wav(path)

    if ENGINE == "zipformer":
        stream = rec.create_stream()
        stream.accept_waveform(rate, samples)
        stream.accept_waveform(rate, np.zeros(int(rate * 0.4), dtype=np.float32))
        stream.input_finished()
        while rec.is_ready(stream):
            rec.decode_stream(stream)
        return rec.get_result(stream)

    stream = rec.create_stream()
    stream.accept_waveform(rate, samples)
    rec.decode_stream(stream)
    return stream.result.text


def main():
    rec = build_recognizer()
    alt = build_alt_recognizer()
    print(json.dumps({"ready": True, "engine": ENGINE, "alt": alt is not None}), flush=True)

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        t0 = time.time()
        try:
            req = json.loads(line)
            use = alt if (req.get("alt") and alt is not None) else rec
            text = transcribe(use, req["wav"])
            out = {"text": text, "ms": int((time.time() - t0) * 1000)}
        except Exception as e:  # noqa: BLE001
            out = {"error": str(e)}
        print(json.dumps(out, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()

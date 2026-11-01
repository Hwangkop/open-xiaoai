# -*- coding: utf-8 -*-
import json, time, urllib.request

prompts = [
    "你好",
    "在吗",
    "我回来了",
    "今天加班到十点，烦死了",
    "老板又临时改需求",
    "地铁挤死了",
    "今天好热啊",
    "今天被领导骂了，有点难过",
    "有点想家",
    "我睡不着",
    "头有点疼怎么办",
    "冰箱里只有鸡蛋和番茄，做什么好",
    "房间太干了",
    "你喜欢猫还是狗",
    "讲个冷笑话",
    "周末不想出门",
    "你觉得人为什么要上班",
    "推荐一部电影",
    "什么是黑洞",
    "我先睡了",
    "我出门了",
    "晚安",
    "拜拜",
]

out = []
for i, p in enumerate(prompts):
    try:
        body = json.dumps({"text": p}).encode("utf-8")
        req = urllib.request.Request(
            "http://127.0.0.1:4399/admin/api/debug/doubao-chat",
            data=body,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=200) as r:
            d = json.load(r)
        reply = d.get("reply", "")
        out.append({"prompt": p, "reply": reply})
        print(f"{i+1}/{len(prompts)} {p} -> {len(reply)} chars", flush=True)
    except Exception as e:
        out.append({"prompt": p, "error": str(e)})
        print(f"{i+1}/{len(prompts)} ERR {p}: {e}", flush=True)
    time.sleep(2.5)

json.dump(out, open(r"E:\open-xiaoai\doubao_style2.json", "w", encoding="utf-8"), ensure_ascii=False, indent=1)
print("saved")

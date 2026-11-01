# -*- coding: utf-8 -*-
import json, time, urllib.request

prompts = [
    "今天天气怎么样",
    "现在几点了",
    "放首周杰伦的歌",
    "明天早上八点提醒我开会",
    "把客厅的灯打开",
    "今天有什么新闻",
    "晚饭吃什么好",
    "周末去哪玩",
    "怎么才能减肥",
    "咖啡喝多了心慌",
    "猫不吃饭怎么办",
    "跟对象吵架了",
    "朋友借钱不还怎么办",
    "同事甩锅给我",
    "爸妈催婚",
    "讲个鬼故事",
    "推荐首歌",
    "你玩游戏吗",
    "说个绕口令",
    "怎么背单词",
    "用一句话解释相对论",
    "孩子不爱写作业",
    "你是谁",
    "你能做什么",
    "你记得我吗",
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

json.dump(out, open(r"E:\open-xiaoai\doubao_style3.json", "w", encoding="utf-8"), ensure_ascii=False, indent=1)
print("saved")

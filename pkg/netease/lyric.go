package netease

// 歌词接口：eapi /api/song/lyric，返回 LRC 文本（带时间轴）。
// 用来判断"音乐里的一句 ASR 结果是不是歌词"——比让模型猜准得多。

import (
	"context"
	"encoding/json"
)

// Lyric 取指定歌曲的 LRC 歌词；没有歌词时返回空串（不报错）。
func (c *Client) Lyric(ctx context.Context, id int64) (string, error) {
	raw, err := c.call(ctx, "/api/song/lyric", map[string]any{
		"id": id, "lv": -1, "kv": -1, "tv": -1,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		LRC struct {
			Lyric string `json:"lyric"`
		} `json:"lrc"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", err
	}
	return resp.LRC.Lyric, nil
}

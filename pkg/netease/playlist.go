package netease

// 用户歌单与收藏：/api/user/playlist + /api/v6/playlist/detail。
// 「我喜欢的音乐」在 user/playlist 里 specialType=5。

import (
	"context"
	"encoding/json"
	"fmt"
)

type Playlist struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	TrackCount  int    `json:"trackCount"`
	SpecialType int    `json:"specialType"`
	CreatorID   int64  `json:"userId"`
}

// UserPlaylists 返回用户创建+收藏的歌单。
func (c *Client) UserPlaylists(ctx context.Context, uid string) ([]Playlist, error) {
	body, err := c.call(ctx, "/api/user/playlist", map[string]any{
		"uid": uid, "limit": 100, "offset": 0,
	})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Code      int        `json:"code"`
		Playlists []Playlist `json:"playlist"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析歌单列表失败: %w", err)
	}
	if parsed.Code != 200 {
		return nil, fmt.Errorf("获取歌单失败 code=%d", parsed.Code)
	}
	return parsed.Playlists, nil
}

// PlaylistDetail 返回歌单里的歌曲（最多 500 首）。
func (c *Client) PlaylistDetail(ctx context.Context, id int64) ([]Song, error) {
	body, err := c.call(ctx, "/api/v6/playlist/detail", map[string]any{
		"id": id, "n": 500, "s": 0,
	})
	if err != nil {
		return nil, err
	}
	type track struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Duration int    `json:"duration"`
		Artists  []struct {
			Name string `json:"name"`
		} `json:"ar"`
		Album struct {
			Name string `json:"name"`
		} `json:"al"`
	}
	// 注意：v6 接口把歌单放在**顶层** playlist，而不是 result.playlist
	var parsed struct {
		Code     int `json:"code"`
		Playlist struct {
			Tracks []track `json:"tracks"`
		} `json:"playlist"`
		Result struct {
			Playlist struct {
				Tracks []track `json:"tracks"`
			} `json:"playlist"`
			Tracks []track `json:"tracks"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析歌单详情失败: %w", err)
	}
	if parsed.Code != 200 {
		return nil, fmt.Errorf("获取歌单详情失败 code=%d", parsed.Code)
	}
	tracks := parsed.Playlist.Tracks
	if len(tracks) == 0 {
		tracks = parsed.Result.Playlist.Tracks
	}
	if len(tracks) == 0 {
		tracks = parsed.Result.Tracks
	}
	out := make([]Song, 0, len(tracks))
	for _, s := range tracks {
		names := make([]string, 0, len(s.Artists))
		for _, a := range s.Artists {
			names = append(names, a.Name)
		}
		out = append(out, Song{ID: s.ID, Name: s.Name, Artists: joinSlash(names), Album: s.Album.Name, Duration: s.Duration})
	}
	return out, nil
}

func joinSlash(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "/"
		}
		out += p
	}
	return out
}

// DailySongs 每日推荐（登录后每天 6 点更新，一般 20~32 首）。
func (c *Client) DailySongs(ctx context.Context) ([]Song, error) {
	body, err := c.call(ctx, "/api/v3/discovery/recommend/songs", map[string]any{})
	if err != nil {
		return nil, err
	}
	type track struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Duration int    `json:"dt"`
		Artists  []struct {
			Name string `json:"name"`
		} `json:"ar"`
		Album struct {
			Name string `json:"name"`
		} `json:"al"`
	}
	var parsed struct {
		Code int `json:"code"`
		Data struct {
			DailySongs []track `json:"dailySongs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析每日推荐失败: %w", err)
	}
	if parsed.Code != 200 {
		return nil, fmt.Errorf("获取每日推荐失败 code=%d", parsed.Code)
	}
	out := make([]Song, 0, len(parsed.Data.DailySongs))
	for _, s := range parsed.Data.DailySongs {
		names := make([]string, 0, len(s.Artists))
		for _, a := range s.Artists {
			names = append(names, a.Name)
		}
		out = append(out, Song{ID: s.ID, Name: s.Name, Artists: joinSlash(names), Album: s.Album.Name, Duration: s.Duration})
	}
	return out, nil
}

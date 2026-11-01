package netease

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestRecommendProbe(t *testing.T) {
	path := os.Getenv("NETEASE_SESSION")
	if path == "" {
		t.Skip("需要 NETEASE_SESSION")
	}
	raw, _ := os.ReadFile(path)
	var sess struct {
		Cookie string `json:"cookie"`
	}
	_ = json.Unmarshal(raw, &sess)
	c := New(sess.Cookie)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	cases := []struct {
		name string
		path string
		data map[string]any
	}{
		{"daily-songs", "/api/v3/discovery/recommend/songs", map[string]any{}},
		{"recommend-resource", "/api/v1/discovery/recommend/resource", map[string]any{}},
		{"personalized-playlist", "/api/personalized/playlist", map[string]any{"limit": 10}},
		{"intelligence", "/api/playmode/intelligence/list", map[string]any{"songId": 202369, "playlistId": 592792353, "startMusicId": 202369, "count": 5}},
		{"radio", "/api/v1/radio/get", map[string]any{}},
		{"personalized-newsong", "/api/personalized/newsong", map[string]any{"limit": 10}},
	}
	for _, tc := range cases {
		body, err := c.call(ctx, tc.path, tc.data)
		if err != nil {
			t.Logf("%-22s err=%v", tc.name, err)
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		detail := ""
		for _, k := range []string{"data", "result", "recommend", "song", "playlist", "songs"} {
			if v, ok := m[k]; ok {
				switch vv := v.(type) {
				case []any:
					detail += k + "=array(" + itoa(len(vv)) + ") "
				case map[string]any:
					keys := make([]string, 0, 8)
					for kk := range vv {
						keys = append(keys, kk)
						if len(keys) >= 6 {
							break
						}
					}
					detail += k + "={" + joinComma(keys) + "} "
				}
			}
		}
		t.Logf("%-22s code=%v %s", tc.name, m["code"], detail)
		if d, ok := m["data"].(map[string]any); ok {
			if daily, ok := d["dailySongs"].([]any); ok {
				t.Logf("     dailySongs=%d", len(daily))
			}
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
func joinComma(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

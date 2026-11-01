package netease

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// NETEASE_SESSION=<path> go test -run TestPlaylistProbe -v .
func TestPlaylistProbe(t *testing.T) {
	path := os.Getenv("NETEASE_SESSION")
	if path == "" {
		t.Skip("需要 NETEASE_SESSION")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sess struct {
		Cookie string `json:"cookie"`
	}
	_ = json.Unmarshal(raw, &sess)
	c := New(sess.Cookie)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	acc, err := c.GetAccount(ctx)
	if err != nil || acc == nil || !acc.LoggedIn {
		t.Fatalf("账号无效: %v %+v", err, acc)
	}
	t.Logf("账号: %s (uid=%s vip=%d)", acc.Nickname, acc.UserID, acc.VIPType)

	lists, err := c.UserPlaylists(ctx, acc.UserID)
	if err != nil {
		t.Fatalf("歌单失败: %v", err)
	}
	t.Logf("歌单 %d 个:", len(lists))
	for i, p := range lists {
		if i >= 12 {
			break
		}
		t.Logf("  [%d] %s (id=%d, %d 首, special=%d)", i, p.Name, p.ID, p.TrackCount, p.SpecialType)
	}
	// 我喜欢的音乐
	for _, p := range lists {
		if p.SpecialType == 5 {
			songs, err := c.PlaylistDetail(ctx, p.ID)
			if err != nil {
				t.Fatalf("详情失败: %v", err)
			}
			t.Logf("「%s」共 %d 首，前三首:", p.Name, len(songs))
			for i, s := range songs {
				if i >= 3 {
					break
				}
				t.Logf("  %s - %s", s.Name, s.Artists)
			}
			break
		}
	}
}

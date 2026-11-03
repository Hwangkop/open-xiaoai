package main

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// 合集的视频、视频的分 P 和音频内时间点是三层目录，不把长视频伪装成单曲。
type bilibiliCatalogItem struct {
	BVID     string `json:"bvid"`
	CID      int64  `json:"cid"`
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Duration int    `json:"duration_seconds"`
	Page     int    `json:"page,omitempty"`
	Section  string `json:"section,omitempty"`
}

type bilibiliCollection struct {
	ID    int64                 `json:"id"`
	Title string                `json:"title"`
	Total int                   `json:"total"`
	Items []bilibiliCatalogItem `json:"items"`
}

type bilibiliCatalog struct {
	Video       bilibiliCatalogItem   `json:"video"`
	SelectedCID int64                 `json:"selected_cid"`
	Parts       []bilibiliCatalogItem `json:"parts"`
	Collection  *bilibiliCollection   `json:"collection,omitempty"`
}

type bilibiliViewResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		BVID     string `json:"bvid"`
		AID      int64  `json:"aid"`
		CID      int64  `json:"cid"`
		Title    string `json:"title"`
		Pic      string `json:"pic"`
		Duration int    `json:"duration"`
		Owner    struct {
			MID  int64  `json:"mid"`
			Name string `json:"name"`
		} `json:"owner"`
		Pages []struct {
			CID      int64  `json:"cid"`
			Page     int    `json:"page"`
			Part     string `json:"part"`
			Duration int    `json:"duration"`
		} `json:"pages"`
		Season *struct {
			ID       int64  `json:"id"`
			MID      int64  `json:"mid"`
			Title    string `json:"title"`
			Count    int    `json:"ep_count"`
			Sections []struct {
				Title    string `json:"title"`
				Episodes []struct {
					BVID  string `json:"bvid"`
					CID   int64  `json:"cid"`
					Title string `json:"title"`
					Arc   struct {
						Duration int `json:"duration"`
					} `json:"arc"`
				} `json:"episodes"`
			} `json:"sections"`
		} `json:"ugc_season"`
	} `json:"data"`
}

var bilibiliBVIDPattern = regexp.MustCompile(`^BV[0-9A-Za-z]{10}$`)

func parseBilibiliReference(reference string) (bvid string, page int, err error) {
	reference = strings.TrimSpace(reference)
	if bilibiliBVIDPattern.MatchString(reference) {
		return reference, 1, nil
	}
	u, parseErr := url.Parse(reference)
	if parseErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", 0, fmt.Errorf("请输入 BV 号或 B 站视频完整链接")
	}
	switch strings.ToLower(u.Hostname()) {
	case "www.bilibili.com", "bilibili.com", "m.bilibili.com":
	default:
		return "", 0, fmt.Errorf("请使用 bilibili.com 的视频链接")
	}
	path := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(path) != 2 || path[0] != "video" || !bilibiliBVIDPattern.MatchString(path[1]) {
		return "", 0, fmt.Errorf("未找到有效的 BV 号，请粘贴视频页面链接")
	}
	page = 1
	if p := u.Query().Get("p"); p != "" {
		page, err = strconv.Atoi(p)
		if err != nil || page < 1 {
			return "", 0, fmt.Errorf("分 P 序号无效")
		}
	}
	return path[1], page, nil
}

func (s *bilibiliService) readVideoView(ctx context.Context, bvid string) (bilibiliViewResponse, error) {
	var view bilibiliViewResponse
	if err := s.ensureAnonymousSession(ctx); err != nil {
		return view, err
	}
	u, err := s.signedWBIURL(ctx, "https://api.bilibili.com/x/web-interface/wbi/view", url.Values{"bvid": {bvid}})
	if err != nil {
		return view, err
	}
	if err := s.requestJSON(ctx, u, "https://www.bilibili.com/video/"+bvid, &view); err != nil {
		return view, err
	}
	if view.Code != 0 {
		return view, fmt.Errorf("读取视频失败（%d）：%s", view.Code, view.Message)
	}
	return view, nil
}

func (s *bilibiliService) catalog(ctx context.Context, reference string) (*bilibiliCatalog, error) {
	bvid, page, err := parseBilibiliReference(reference)
	if err != nil {
		return nil, err
	}
	v, err := s.readVideoView(ctx, bvid)
	if err != nil {
		return nil, err
	}
	d := v.Data
	out := &bilibiliCatalog{Video: bilibiliCatalogItem{BVID: bvid, CID: d.CID, Title: cleanBilibiliText(d.Title), Author: cleanBilibiliText(d.Owner.Name), Duration: d.Duration}}
	for i, p := range d.Pages {
		out.Parts = append(out.Parts, bilibiliCatalogItem{BVID: bvid, CID: p.CID, Title: cleanBilibiliText(p.Part), Author: out.Video.Author, Duration: p.Duration, Page: i + 1})
	}
	if len(out.Parts) == 0 || page > len(out.Parts) {
		return nil, fmt.Errorf("视频里没有这个分 P")
	}
	out.SelectedCID = out.Parts[page-1].CID
	if d.Season == nil {
		return out, nil
	}
	season := d.Season
	collection := &bilibiliCollection{ID: season.ID, Title: cleanBilibiliText(season.Title), Total: season.Count}
	seen := map[string]bool{}
	for _, section := range season.Sections {
		for _, e := range section.Episodes {
			if !bilibiliBVIDPattern.MatchString(e.BVID) || seen[e.BVID] {
				continue
			}
			seen[e.BVID] = true
			collection.Items = append(collection.Items, bilibiliCatalogItem{BVID: e.BVID, CID: e.CID, Title: cleanBilibiliText(e.Title), Author: out.Video.Author, Duration: e.Arc.Duration, Section: cleanBilibiliText(section.Title)})
		}
	}
	// 详情已给全时保留作者的分区及顺序；不完整时必须分页取全，不能把第一页当整个合集。
	if len(collection.Items) != collection.Total || collection.Total <= 0 {
		mid := season.MID
		if mid == 0 {
			mid = d.Owner.MID
		}
		if err := s.completeCollection(ctx, mid, collection); err != nil {
			return nil, err
		}
	}
	out.Collection = collection
	return out, nil
}

func (s *bilibiliService) completeCollection(ctx context.Context, mid int64, collection *bilibiliCollection) error {
	const pageSize, maxItems = 30, 10000
	if mid <= 0 || collection.ID <= 0 {
		return fmt.Errorf("合集缺少作者或合集 ID")
	}
	items, seen := []bilibiliCatalogItem{}, map[string]bool{}
	total := -1
	for page := 1; ; page++ {
		var response struct {
			Code int `json:"code"`
			Data struct {
				Page struct {
					Total int `json:"total"`
				} `json:"page"`
				Archives []struct {
					BVID     string `json:"bvid"`
					Title    string `json:"title"`
					Duration int    `json:"duration"`
				} `json:"archives"`
			} `json:"data"`
		}
		params := url.Values{"mid": {strconv.FormatInt(mid, 10)}, "season_id": {strconv.FormatInt(collection.ID, 10)}, "page_num": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}, "sort_reverse": {"false"}}
		if err := s.requestJSON(ctx, s.apiURL("https://api.bilibili.com/x/polymer/web-space/seasons_archives_list", params), "https://www.bilibili.com/", &response); err != nil {
			return fmt.Errorf("读取合集第 %d 页失败：%w", page, err)
		}
		if response.Code != 0 {
			return fmt.Errorf("读取合集失败（%d）", response.Code)
		}
		if total < 0 {
			total = response.Data.Page.Total
		}
		if total < 1 || total > maxItems || total != response.Data.Page.Total {
			return fmt.Errorf("合集数量无效或目录发生变化，请重新读取")
		}
		before := len(items)
		for _, e := range response.Data.Archives {
			if bilibiliBVIDPattern.MatchString(e.BVID) && !seen[e.BVID] {
				seen[e.BVID] = true
				items = append(items, bilibiliCatalogItem{BVID: e.BVID, Title: cleanBilibiliText(e.Title), Duration: e.Duration})
			}
		}
		if page*pageSize >= total {
			if len(items) != total {
				return fmt.Errorf("合集目录不完整：应有 %d 个视频，实际读取 %d 个，请重试", total, len(items))
			}
			collection.Total, collection.Items = total, items
			return nil
		}
		if before == len(items) {
			return fmt.Errorf("合集分页没有返回新视频，未导入不完整目录")
		}
	}
}

func (c *bilibiliCatalog) playlist(kind string) (string, []bilibiliCatalogItem, error) {
	switch kind {
	case "collection":
		if c.Collection == nil || len(c.Collection.Items) == 0 {
			return "", nil, fmt.Errorf("这个视频不属于可读取的合集")
		}
		return c.Collection.Title, c.Collection.Items, nil
	case "parts":
		return c.Video.Title, c.Parts, nil
	case "video":
		for _, p := range c.Parts {
			if p.CID == c.SelectedCID {
				return c.Video.Title, []bilibiliCatalogItem{p}, nil
			}
		}
	}
	return "", nil, fmt.Errorf("请选择视频、分 P 或合集")
}

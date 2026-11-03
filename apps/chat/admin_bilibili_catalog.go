package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func (a *adminServer) handleBilibiliCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, 405, "只支持读取目录")
		return
	}
	if a.bilibili == nil {
		writeAdminError(w, 503, "B 站音乐未启用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	catalog, err := a.bilibili.catalog(ctx, r.URL.Query().Get("reference"))
	if err != nil {
		writeAdminError(w, 400, err.Error())
		return
	}
	writeAdminJSON(w, 200, catalog)
}

func (a *adminServer) playBilibiliVideo(ctx context.Context, reference string, cid int64) (string, error) {
	if a.bilibili == nil {
		return "", fmt.Errorf("B 站音乐未启用")
	}
	catalog, err := a.bilibili.catalog(ctx, reference)
	if err != nil {
		return "", err
	}
	if cid == 0 {
		cid = catalog.SelectedCID
	}
	for _, item := range catalog.Parts {
		if item.CID == cid {
			return a.requestBilibiliQueue(catalog.Video.Title, []bilibiliCatalogItem{item}, 0, false)
		}
	}
	return "", fmt.Errorf("视频里没有这个分 P")
}

func (a *adminServer) playBilibiliPlaylist(ctx context.Context, reference, kind string, index int, shuffle bool) (string, error) {
	if a.bilibili == nil {
		return "", fmt.Errorf("B 站音乐未启用")
	}
	catalog, err := a.bilibili.catalog(ctx, reference)
	if err != nil {
		return "", err
	}
	name, items, err := catalog.playlist(kind)
	if err != nil {
		return "", err
	}
	return a.requestBilibiliQueue(name, items, index, shuffle)
}

func (a *adminServer) handleBilibiliQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, 405, "只支持提交播放队列")
		return
	}
	var req struct {
		Reference string `json:"reference"`
		Kind      string `json:"kind"`
		Index     int    `json:"index"`
		Shuffle   bool   `json:"shuffle"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeAdminError(w, 400, "请求格式错误")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	message, err := a.playBilibiliPlaylist(ctx, req.Reference, req.Kind, req.Index, req.Shuffle)
	if err != nil {
		writeAdminError(w, 400, err.Error())
		return
	}
	writeAdminJSON(w, 202, adminResponse{Message: message})
}

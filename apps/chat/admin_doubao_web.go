package main

// 管理端：豆包网页版直连（doubao_web）的扫码登录接口。
//
//	GET  /admin/api/doubao/web/status   当前登录态
//	POST /admin/api/doubao/web/qr       取一张新二维码（返回 PNG base64）
//	GET  /admin/api/doubao/web/qr       轮询扫码结果
//
// 前端拿到 base64 后自己拼 data:image/png;base64, 显示即可。

import (
	"context"
	"net/http"
	"time"
)

func (a *adminServer) doubaoWebClient() *DoubaoWeb {
	if a.getDoubaoWeb == nil {
		return nil
	}
	return a.getDoubaoWeb()
}

func (a *adminServer) handleDoubaoWebStatus(w http.ResponseWriter, r *http.Request) {	web := a.doubaoWebClient()
	if web == nil {
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "未启用 doubao_web（在 config.yaml 里设置 doubao_web.enabled: true）",
		})
		return
	}
	out := web.Status()
	out["enabled"] = true
	writeAdminJSON(w, http.StatusOK, out)
}

// handleDoubaoWebQR 路由 /admin/api/doubao/web/qr：POST 取新码，GET 轮询。
func (a *adminServer) handleDoubaoWebQR(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.handleDoubaoWebQRStart(w, r)
		return
	}
	a.handleDoubaoWebQRPoll(w, r)
}

func (a *adminServer) handleDoubaoWebQRStart(w http.ResponseWriter, r *http.Request) {
	web := a.doubaoWebClient()
	if web == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "未启用 doubao_web")
		return
	}
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "请使用 POST")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	img, err := web.StartQR(ctx)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"status":   "qr_ready",
		"image":    img,
		"message":  "用豆包 App 扫码登录",
		"expires_in": int(doubaoQRTimeout.Seconds()),
	})
}

func (a *adminServer) handleDoubaoWebQRPoll(w http.ResponseWriter, r *http.Request) {
	web := a.doubaoWebClient()
	if web == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "未启用 doubao_web")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	status, err := web.PollQR(ctx)
	resp := map[string]any{"status": status}
	switch status {
	case "confirmed":
		resp["message"] = "登录成功"
		if err != nil {
			resp["warning"] = err.Error()
		}
	case "scanned":
		resp["message"] = "已扫码，请在手机上确认"
	case "expired":
		resp["message"] = "二维码已过期，请重新获取"
	case "pending":
		resp["message"] = "等待扫码"
	}
	if err != nil && status != "confirmed" {
		resp["error"] = err.Error()
		writeAdminJSON(w, http.StatusBadGateway, resp)
		return
	}
	writeAdminJSON(w, http.StatusOK, resp)
}

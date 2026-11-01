package main

// 走 Home Assistant 的设备列表与控制（替代直连米家那条 429 的路）。
//
//	GET  /admin/api/ha/devices
//	POST /admin/api/ha/control  {"device":"加湿器","action":"on"}
//
// MCP 的 list_devices / control_device 在配置了 homeassistant.base_url+token 时走这里。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cxjava/open-xiaoai/pkg/homeassistant"
)

// haControlDomains 允许语音控制的实体域。
var haControlDomains = map[string]bool{
	"light": true, "switch": true, "fan": true, "climate": true,
	"cover": true, "vacuum": true, "humidifier": true, "media_player": true,
	"scene": true, "script": true, "water_heater": true, "input_boolean": true,
}

// haSubFeatureWords 设备内部的子功能开关（指示灯、勿扰、移动侦测…），不暴露给模型。
var haSubFeatureWords = []string{
	"指示灯", "物理控制锁", "充电保护", "循环任务", "倒计时", "功率限制",
	"过度用电", "勿扰", "睡眠模式", "提示音", "时间水印", "微光全彩",
	"人形追踪", "人形检测", "图像畸变", "移动侦测", "人脸检测", "宠物检测",
	"手势检测", "画面变动", "哭声检测", "响声检测", "宠物追踪", "人形特写",
	"巡航", "虚拟围栏", "麦克风", "静音", "弹窗", "过湿保护", "恒湿保护",
	"摄像机控制", "存储卡", "AI检测", "自定义",
}

// haDomainAliases 用户口语 → 实体域，设备名对不上时兜底。
var haDomainAliases = map[string]string{
	"扫地机": "vacuum", "扫地机器人": "vacuum", "扫拖": "vacuum", "吸尘器": "vacuum",
	"加湿器": "humidifier",
	"音箱":  "media_player", "音响": "media_player", "喇叭": "media_player",
	"灯": "light", "台灯": "light", "吸顶灯": "light",
	"空调": "climate", "风扇": "fan", "窗帘": "cover",
	"插座": "switch", "插排": "switch",
}

var haDomainLabels = map[string]string{
	"light": "灯", "switch": "开关", "fan": "风扇", "climate": "空调",
	"cover": "窗帘", "vacuum": "扫地机", "humidifier": "加湿器",
	"media_player": "播放器", "scene": "场景", "script": "脚本",
	"water_heater": "热水器", "input_boolean": "开关",
}

// deviceControlRequest 家电控制请求（设备名可模糊，entity 可精确指定）。
type deviceControlRequest struct {
	Device string `json:"device"`
	Entity string `json:"entity"`
	Action string `json:"action"`
	Value  any    `json:"value"`
}

type haDevice struct {
	EntityID string
	Name     string
	Domain   string
	State    string
}

func (a *adminServer) haConfig() HomeAssistantConfig {
	if a.getConfig == nil {
		return HomeAssistantConfig{}
	}
	cfg := a.getConfig()
	if cfg == nil {
		return HomeAssistantConfig{}
	}
	return cfg.HomeAssistant
}

// haClient 未配置时返回 nil。
func (a *adminServer) haClient() *homeassistant.Client {
	c := a.haConfig()
	if !c.Enabled() {
		return nil
	}
	return homeassistant.New(c.BaseURL, c.Token)
}

func (a *adminServer) haDevices(ctx context.Context) ([]haDevice, error) {
	client := a.haClient()
	if client == nil {
		return nil, fmt.Errorf("Home Assistant 未配置")
	}
	states, err := client.States(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]haDevice, 0, len(states))
	for _, s := range states {
		if !haControlDomains[s.Domain()] {
			continue
		}
		if haIsSubFeature(s.FriendlyName()) {
			continue
		}
		out = append(out, haDevice{
			EntityID: s.EntityID,
			Name:     haCleanName(s.FriendlyName()),
			Domain:   s.Domain(),
			State:    s.State,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func haIsSubFeature(name string) bool {
	if strings.HasPrefix(strings.TrimSpace(name), "*") {
		return true
	}
	for _, w := range haSubFeatureWords {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

func haStateLabel(state string) string {
	switch state {
	case "on":
		return "开"
	case "off":
		return "关"
	case "unavailable":
		return "离线"
	case "unknown":
		return "未知"
	}
	return state
}

// haCleanName 把 HA 里的双空格、换行压成单个空格；
// 设备名和实体名完全相同时（"石头扫拖机器人 T7S  石头扫拖机器人 T7S"）去掉重复的那段。
func haCleanName(s string) string {
	s = strings.TrimSpace(s)
	if parts := strings.Split(s, "  "); len(parts) == 2 &&
		strings.TrimSpace(parts[0]) == strings.TrimSpace(parts[1]) && parts[0] != "" {
		s = strings.TrimSpace(parts[0])
	}
	out := strings.Join(strings.Fields(s), " ")
	// 有些实体的分隔符不是普通空格（NBSP 等），压平后变成两个相同的半句
	fields := strings.Fields(out)
	if len(fields) >= 2 && len(fields)%2 == 0 {
		half := len(fields) / 2
		left := strings.Join(fields[:half], " ")
		right := strings.Join(fields[half:], " ")
		if left == right {
			return left
		}
	}
	return out
}

// resolveHADevice 按名字模糊找实体；歧义时把候选列出来。
func resolveHADevice(states []homeassistant.State, query, entityID string) (homeassistant.State, error) {
	query = strings.TrimSpace(query)
	entityID = strings.TrimSpace(entityID)

	candidates := make([]homeassistant.State, 0, len(states))
	for _, s := range states {
		if !haControlDomains[s.Domain()] || haIsSubFeature(s.FriendlyName()) {
			continue
		}
		candidates = append(candidates, s)
	}

	if entityID != "" {
		for _, s := range candidates {
			if s.EntityID == entityID {
				return s, nil
			}
		}
	}
	if query == "" {
		return homeassistant.State{}, fmt.Errorf("需要设备名或 entity_id")
	}

	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.NewReplacer(" ", "", "　", "", "-", "", "_", "").Replace(s)
		return s
	}
	q := norm(query)

	type scored struct {
		state homeassistant.State
		score int
	}
	var best []scored
	for _, s := range candidates {
		name := norm(haCleanName(s.FriendlyName()))
		eid := norm(s.EntityID)
		score := 0
		switch {
		case name == q || eid == q:
			score = 100
		case strings.Contains(name, q):
			score = 80
		case strings.Contains(q, name) && name != "":
			score = 70
		case strings.Contains(eid, q):
			score = 60
		default:
			// 简单分词：按名字里的空格/标点切开，看有没有命中的词
			for _, part := range strings.FieldsFunc(q, func(r rune) bool {
				return r == ' ' || r == '的' || r == '，' || r == ','
			}) {
				if len([]rune(part)) >= 2 && strings.Contains(name, part) {
					score = 50
					break
				}
			}
		}
		if score > 0 {
			best = append(best, scored{s, score})
		}
	}
	if len(best) == 0 {
		// 名字对不上时按口语词兜底：「扫地机」→ vacuum、「加湿器」→ humidifier
		for word, domain := range haDomainAliases {
			if !strings.Contains(query, word) {
				continue
			}
			var hits []homeassistant.State
			for _, s := range candidates {
				if s.Domain() == domain {
					hits = append(hits, s)
				}
			}
			if len(hits) == 1 {
				return hits[0], nil
			}
			if len(hits) > 1 {
				names := make([]string, 0, len(hits))
				for _, s := range hits {
					names = append(names, haCleanName(s.FriendlyName()))
				}
				return homeassistant.State{}, fmt.Errorf("「%s」匹配到多个设备：%s，请说具体一点", query, strings.Join(names, "、"))
			}
		}
		names := make([]string, 0, len(candidates))
		for _, s := range candidates {
			names = append(names, haCleanName(s.FriendlyName()))
		}
		if len(names) > 12 {
			names = names[:12]
		}
		return homeassistant.State{}, fmt.Errorf("没找到「%s」，可控制的有：%s", query, strings.Join(names, "、"))
	}
	sort.Slice(best, func(i, j int) bool {
		if best[i].score != best[j].score {
			return best[i].score > best[j].score
		}
		return best[i].state.FriendlyName() < best[j].state.FriendlyName()
	})
	if len(best) > 1 && best[0].score == best[1].score {
		names := []string{}
		for _, b := range best {
			if b.score != best[0].score {
				break
			}
			names = append(names, haCleanName(b.state.FriendlyName()))
		}
		return homeassistant.State{}, fmt.Errorf("「%s」匹配到多个设备：%s，请说具体一点", query, strings.Join(names, "、"))
	}
	return best[0].state, nil
}

// controlHA 执行一次控制，返回给用户看的一句话。
func (a *adminServer) controlHA(ctx context.Context, req deviceControlRequest) (string, error) {
	client := a.haClient()
	if client == nil {
		return "", fmt.Errorf("Home Assistant 未配置")
	}
	states, err := client.States(ctx)
	if err != nil {
		return "", err
	}
	target, err := resolveHADevice(states, req.Device, req.Entity)
	if err != nil {
		return "", err
	}

	name := haCleanName(target.FriendlyName())
	domain := target.Domain()
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if target.State == "unavailable" {
		return "", fmt.Errorf("%s 当前离线，先检查电源或网络", name)
	}
	num := func() (float64, bool) {
		switch v := req.Value.(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case json.Number:
			f, err := v.Float64()
			return f, err == nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return f, err == nil
		}
		return 0, false
	}
	text := func() string {
		if s, ok := req.Value.(string); ok {
			return strings.TrimSpace(s)
		}
		if f, ok := num(); ok {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return ""
	}
	data := map[string]any{"entity_id": target.EntityID}

	call := func(d, s string) error {
		return client.CallService(ctx, d, s, data)
	}

	switch action {
	case "on", "off", "toggle":
		switch domain {
		case "scene", "script":
			if action == "off" {
				return "", fmt.Errorf("%s 不是开关设备", name)
			}
			if err := call(domain, "turn_on"); err != nil {
				return "", err
			}
			return "已执行" + haDomainLabels[domain] + "「" + name + "」", nil
		case "climate":
			mode := "auto"
			if action == "off" {
				mode = "off"
			}
			data["hvac_mode"] = mode
			if err := call("climate", "set_hvac_mode"); err != nil {
				return "", err
			}
			if mode == "off" {
				return "已关闭" + name, nil
			}
			return "已打开" + name, nil
		case "vacuum":
			svc := "start"
			if action == "off" {
				svc = "stop"
			}
			if err := call("vacuum", svc); err != nil {
				return "", err
			}
			if svc == "start" {
				return "已启动" + name, nil
			}
			return "已停止" + name, nil
		case "cover":
			svc := "open_cover"
			if action == "off" {
				svc = "close_cover"
			}
			if err := call("cover", svc); err != nil {
				return "", err
			}
			return "已执行" + name, nil
		case "media_player":
			return "", fmt.Errorf("%s 不支持开关，可以说调音量或暂停", name)
		default:
			svc := "turn_" + action
			if err := call(domain, svc); err != nil {
				return "", err
			}
			switch action {
			case "on":
				return "已打开" + name, nil
			case "off":
				return "已关闭" + name, nil
			}
			return "已切换" + name, nil
		}

	case "brightness", "percentage":
		v, ok := num()
		if !ok {
			return "", fmt.Errorf("需要数值")
		}
		pct := int(v + 0.5)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		switch domain {
		case "fan":
			data["percentage"] = pct
			if err := call("fan", "set_percentage"); err != nil {
				return "", err
			}
			return fmt.Sprintf("已把%s调到 %d%%", name, pct), nil
		case "light":
			data["brightness_pct"] = pct
			if err := call("light", "turn_on"); err != nil {
				return "", err
			}
			return fmt.Sprintf("已把%s调到 %d%%", name, pct), nil
		}
		return "", fmt.Errorf("%s 不支持亮度调节", name)

	case "color-temperature":
		v, ok := num()
		if !ok {
			return "", fmt.Errorf("需要数值")
		}
		if v >= 1000 { // 开尔文
			data["color_temp_kelvin"] = int(v + 0.5)
		} else { // 旧单位 mired
			data["color_temp"] = int(v + 0.5)
		}
		if err := call("light", "turn_on"); err != nil {
			return "", err
		}
		return fmt.Sprintf("已调整%s色温", name), nil

	case "temperature":
		v, ok := num()
		if !ok {
			return "", fmt.Errorf("需要数值")
		}
		data["temperature"] = v
		if err := call("climate", "set_temperature"); err != nil {
			return "", err
		}
		return fmt.Sprintf("已把%s设为 %g 度", name, v), nil

	case "mode":
		mode := text()
		if mode == "" {
			return "", fmt.Errorf("需要模式名")
		}
		data["hvac_mode"] = mode
		if err := call("climate", "set_hvac_mode"); err != nil {
			return "", err
		}
		return fmt.Sprintf("已把%s设为 %s 模式", name, mode), nil

	case "humidity":
		v, ok := num()
		if !ok {
			return "", fmt.Errorf("需要数值")
		}
		data["humidity"] = int(v + 0.5)
		if err := call("humidifier", "set_humidity"); err != nil {
			return "", err
		}
		return fmt.Sprintf("已把%s湿度设为 %d%%", name, int(v+0.5)), nil

	case "open", "close":
		svc := "open_cover"
		if action == "close" {
			svc = "close_cover"
		}
		if err := call("cover", svc); err != nil {
			return "", err
		}
		return "已执行" + name, nil

	case "play", "pause", "stop":
		if domain != "media_player" && !(domain == "vacuum" && action != "play") {
			return "", fmt.Errorf("%s 不支持播放控制", name)
		}
		svc := "media_" + action
		if domain == "vacuum" {
			svc = action
		}
		if err := call(domain, svc); err != nil {
			return "", err
		}
		return "已执行" + name, nil

	case "next", "previous":
		svc := "media_next_track"
		if action == "previous" {
			svc = "media_previous_track"
		}
		if err := call("media_player", svc); err != nil {
			return "", err
		}
		return "已执行" + name, nil

	case "volume":
		v, ok := num()
		if !ok {
			return "", fmt.Errorf("需要数值")
		}
		if domain != "media_player" {
			return "", fmt.Errorf("%s 不支持音量调节", name)
		}
		// 音箱处于静音时 volume_set 不会生效，先取消静音
		if muted, _ := target.Attributes["is_volume_muted"].(bool); muted {
			_ = client.CallService(ctx, "media_player", "volume_mute", map[string]any{
				"entity_id": target.EntityID, "is_volume_muted": false,
			})
		}
		data["volume_level"] = v / 100
		if err := call("media_player", "volume_set"); err != nil {
			return "", err
		}
		return fmt.Sprintf("已把%s音量调到 %g%%", name, v), nil

	case "mute", "unmute":
		if domain != "media_player" {
			return "", fmt.Errorf("%s 不支持静音", name)
		}
		data["is_volume_muted"] = action == "mute"
		if err := call("media_player", "volume_mute"); err != nil {
			return "", err
		}
		if action == "mute" {
			return "已静音" + name, nil
		}
		return "已取消静音" + name, nil
	}
	return "", fmt.Errorf("不支持的动作：%s", action)
}

// toolHAListDevices 给模型看的设备列表。
func (a *adminServer) toolHAListDevices(ctx context.Context) (string, error) {
	devices, err := a.haDevices(ctx)
	if err != nil {
		return "", err
	}
	if len(devices) == 0 {
		return "Home Assistant 里没有可控制的设备", nil
	}
	var b strings.Builder
	for _, d := range devices {
		label := haDomainLabels[d.Domain]
		if label == "" {
			label = d.Domain
		}
		fmt.Fprintf(&b, "%s（%s，%s）\n", d.Name, label, haStateLabel(d.State))
	}
	return strings.TrimSpace(b.String()), nil
}

// ---------- HTTP ----------

func (a *adminServer) handleHADevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	if a.haClient() == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "Home Assistant 未配置（homeassistant.base_url + token）")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	devices, err := a.haDevices(ctx)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (a *adminServer) handleHAControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	var req deviceControlRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	msg, err := a.controlHA(ctx, req)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"message": msg})
}

package main

import "encoding/json"

func codexNotificationForTurn(msg rpcMessage, turnID string) bool {
	var p struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(msg.Params, &p) != nil {
		return false
	}
	if p.TurnID == "" {
		p.TurnID = p.Turn.ID
	}
	// 兼容旧版本缺少 turnId 的通知。
	return p.TurnID == "" || turnID == "" || p.TurnID == turnID
}

// 以 userMessage 的真实落地事件为界，不能把 turn/steer 的接收应答当成已应用。
func codexUserMessageMatches(msg rpcMessage, text string) bool {
	if msg.Method != "item/started" && msg.Method != "item/completed" {
		return false
	}
	var p struct {
		Item struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || p.Item.Type != "userMessage" {
		return false
	}
	for _, content := range p.Item.Content {
		if content.Type == "text" && content.Text == text {
			return true
		}
	}
	return false
}

func codexMessageItemID(msg rpcMessage) string {
	var p struct {
		ItemID string `json:"itemId"`
		Item   struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	if p.ItemID != "" {
		return p.ItemID
	}
	return p.Item.ID
}

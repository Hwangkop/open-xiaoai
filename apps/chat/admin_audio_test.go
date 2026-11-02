package main

import (
	"strings"
	"testing"
)

func TestAudioTestPlayScripts(t *testing.T) {
	const url = "http://192.168.1.100:4399/tts/test.wav"
	cases := []struct {
		play string
		want string
	}{
		{"notify", "aplay -D notify"},
		{"playback", "aplay -D Playback"},
		{"miplayer", "miplayer -f"},
		{"mibrain", `"media":"mibrain"`},
		{"common", "player_play_url"},
	}
	for _, c := range cases {
		script := audioTestPlayScript(c.play, url)
		if !strings.Contains(script, c.want) {
			t.Errorf("%s script missing %q: %s", c.play, c.want, script)
		}
	}
	if got := audioTestPlayScript("unknown", url); got != "" {
		t.Errorf("unknown play should return empty script, got %q", got)
	}
}

func TestFindAudioTestPlay(t *testing.T) {
	for _, p := range audioTestPlays {
		got, ok := findAudioTestPlay(p.ID)
		if !ok || got.ID != p.ID {
			t.Errorf("findAudioTestPlay(%q) = %+v, %v", p.ID, got, ok)
		}
	}
	if _, ok := findAudioTestPlay("nope"); ok {
		t.Error("unknown play should not be found")
	}
}

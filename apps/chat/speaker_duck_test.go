package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestMediaPlayerVolume 覆盖两种 info 形态（对象 / JSON 字符串）和缺字段的情况。
func TestMediaPlayerVolume(t *testing.T) {
	cases := []struct {
		name   string
		out    string
		want   int
		wantOK bool
	}{
		{
			name:   "info 是 JSON 字符串",
			out:    `{"code":0,"info":"{\"status\":1,\"volume\":30,\"loop_type\":1}"}`,
			want:   30,
			wantOK: true,
		},
		{
			name:   "info 是对象",
			out:    `{"code":0,"info":{"status":0,"volume":44}}`,
			want:   44,
			wantOK: true,
		},
		{
			name:   "没有 volume 字段",
			out:    `{"code":0,"info":"{\"status\":1}"}`,
			wantOK: false,
		},
		{
			name:   "输出不是 JSON",
			out:    ``,
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := mediaPlayerVolume(c.out)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Fatalf("volume = %d, want %d", got, c.want)
			}
		})
	}
}

// TestMediaPlayerStatusStillParses 重构解析器后状态解析不能退化。
func TestMediaPlayerStatusStillParses(t *testing.T) {
	if got := mediaPlayerStatus(`{"code":0,"info":"{\"status\":1}"}`); got != 1 {
		t.Fatalf("status = %d, want 1", got)
	}
	if got := mediaPlayerStatus(`garbage`); got != -1 {
		t.Fatalf("status = %d, want -1", got)
	}
}

// fakeVolume 用一个变量模拟设备音量，记录每次 set 的调用。
func fakeVolume(initial *int) (func() (int, error), func(int) error, *[]int) {
	sets := []int{}
	get := func() (int, error) { return *initial, nil }
	set := func(v int) error {
		*initial = v
		sets = append(sets, v)
		return nil
	}
	return get, set, &sets
}

// resetMusicDuck 现在只需要清落盘路径——降音量状态已经收进 Speaker 实例，
// 每个测试自己 NewSpeaker() 就是干净的，不用再手工重置全局变量。
func resetMusicDuck(t *testing.T) {
	t.Helper()
	musicDuckFile = ""
	t.Cleanup(func() { musicDuckFile = "" })
}

func TestDuckLowersThenRestores(t *testing.T) {
	resetMusicDuck(t)
	vol := 70
	get, set, sets := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set
	s.SetMusicDuck(true, 35)

	s.DuckMusicVolume()
	if vol != 35 {
		t.Fatalf("放歌时音量 = %d, want 35", vol)
	}

	s.RestoreMusicVolume()
	if vol != 70 {
		t.Fatalf("停播后音量 = %d, want 70", vol)
	}
	if len(*sets) != 2 {
		t.Fatalf("期望 2 次音量写入，实际 %v", *sets)
	}
}

// TestDuckKeepsOriginalAcrossTracks 连续切歌不能把 duck 值当成"原值"。
func TestDuckKeepsOriginalAcrossTracks(t *testing.T) {
	resetMusicDuck(t)
	vol := 70
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.DuckMusicVolume()
	s.DuckMusicVolume() // 下一首
	s.DuckMusicVolume() // 再下一首
	if vol != 35 {
		t.Fatalf("连续切歌时音量 = %d, want 35", vol)
	}

	s.RestoreMusicVolume()
	if vol != 70 {
		t.Fatalf("还原后音量 = %d, want 70", vol)
	}
}

// TestDuckSkipsWhenAlreadyQuiet 本来就比目标值小，就不要反而调大。
func TestDuckSkipsWhenAlreadyQuiet(t *testing.T) {
	resetMusicDuck(t)
	vol := 20
	get, set, sets := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.DuckMusicVolume()
	if len(*sets) != 0 {
		t.Fatalf("不该写音量，实际 %v", *sets)
	}
	s.RestoreMusicVolume()
	if vol != 20 || len(*sets) != 0 {
		t.Fatalf("音量 = %d, 写入 %v; want 20, 无写入", vol, *sets)
	}
}

// TestRestoreRespectsManualChange 期间用户自己调过音量，就不覆盖他的选择。
func TestRestoreRespectsManualChange(t *testing.T) {
	resetMusicDuck(t)
	vol := 70
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.DuckMusicVolume()
	vol = 55 // 用户中途"大点声"
	s.RestoreMusicVolume()
	if vol != 55 {
		t.Fatalf("音量 = %d, want 55（保留用户调整）", vol)
	}
}

func TestDuckDisabled(t *testing.T) {
	resetMusicDuck(t)
	vol := 70
	get, set, sets := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set
	s.SetMusicDuck(false, 35)

	s.DuckMusicVolume()
	if len(*sets) != 0 || vol != 70 {
		t.Fatalf("关闭后不该动音量，实际 %v (vol=%d)", *sets, vol)
	}
}

func TestDuckSkipsOnReadError(t *testing.T) {
	resetMusicDuck(t)
	vol := 70
	_, set, sets := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet = func() (int, error) { return 0, errors.New("boom") }
	s.volumeSet = set

	s.DuckMusicVolume()
	if len(*sets) != 0 {
		t.Fatalf("读音量失败不该写音量，实际 %v", *sets)
	}
	// 状态没建立，restore 也不该动
	s.RestoreMusicVolume()
	if len(*sets) != 0 {
		t.Fatalf("未建立 duck 状态时 restore 不该写音量，实际 %v", *sets)
	}
}

// TestSetMusicDuckDefaultTarget 配置里填 0 时回落到默认值。
func TestSetMusicDuckDefaultTarget(t *testing.T) {
	resetMusicDuck(t)
	s := NewSpeaker()
	s.SetMusicDuck(true, 0)
	got := s.stateSnapshot().duckTarget
	if got != defaultMusicDuckVolume {
		t.Fatalf("target = %d, want %d", got, defaultMusicDuckVolume)
	}
}

// TestRestorePendingMusicDuck 进程被 kill 时音量停在 duck 值，重连后要补还原并删掉记录。
func TestRestorePendingMusicDuck(t *testing.T) {
	resetMusicDuck(t)
	dir := t.TempDir()
	musicDuckFile = filepath.Join(dir, "music-duck.json")
	saveMusicDuckRecord(musicDuckRecord{Original: 70, Applied: 35})

	vol := 35
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.RestorePendingMusicDuck()
	if vol != 70 {
		t.Fatalf("音量 = %d, want 70", vol)
	}
	if _, err := os.Stat(musicDuckFile); !os.IsNotExist(err) {
		t.Fatalf("记录应被删除，stat err = %v", err)
	}
}

// TestRestorePendingKeepsManualVolume 上次留下记录，但用户已经把音量调走，就不要覆盖。
func TestRestorePendingKeepsManualVolume(t *testing.T) {
	resetMusicDuck(t)
	dir := t.TempDir()
	musicDuckFile = filepath.Join(dir, "music-duck.json")
	saveMusicDuckRecord(musicDuckRecord{Original: 70, Applied: 35})

	vol := 52
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.RestorePendingMusicDuck()
	if vol != 52 {
		t.Fatalf("音量 = %d, want 52（保留用户调整）", vol)
	}
}

// TestRestoreWithoutStateKeepsRecord 进程刚起、还没补还原就调 StopMusic 时，
// 不能把上一个进程留下的记录删掉，否则音量永远停在 duck 值。
func TestRestoreWithoutStateKeepsRecord(t *testing.T) {
	resetMusicDuck(t)
	dir := t.TempDir()
	musicDuckFile = filepath.Join(dir, "music-duck.json")
	saveMusicDuckRecord(musicDuckRecord{Original: 70, Applied: 35})

	vol := 35
	get, set, sets := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.RestoreMusicVolume() // 没有进程内状态
	if _, err := os.Stat(musicDuckFile); err != nil {
		t.Fatalf("记录不该被删掉: %v", err)
	}
	if len(*sets) != 0 {
		t.Fatalf("没有状态时不该写音量，实际 %v", *sets)
	}
}

// TestDuckFailedSetUndoesState 改音量失败时要撤销状态、清掉记录，不能留个假 duck。
func TestDuckFailedSetUndoesState(t *testing.T) {
	resetMusicDuck(t)
	dir := t.TempDir()
	musicDuckFile = filepath.Join(dir, "music-duck.json")

	vol := 70
	s := NewSpeaker()
	s.volumeGet = func() (int, error) { return vol, nil }
	s.volumeSet = func(int) error { return errors.New("ubus failed") }

	s.DuckMusicVolume()

	st := s.stateSnapshot()
	original, applied := st.duckOriginal, st.duckApplied
	if original != -1 || applied != -1 {
		t.Fatalf("状态应被撤销，original=%d applied=%d", original, applied)
	}
	if _, err := os.Stat(musicDuckFile); !os.IsNotExist(err) {
		t.Fatalf("记录应被清掉，stat err = %v", err)
	}
}

// TestDuckSkipsWhileBusy 并发进入时只让第一次读"原值"。
func TestDuckSkipsWhileBusy(t *testing.T) {
	resetMusicDuck(t)
	vol := 70
	get, set, sets := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.stMu.Lock()
	s.st.duckBusy = true
	s.stMu.Unlock()

	s.DuckMusicVolume()
	if len(*sets) != 0 || vol != 70 {
		t.Fatalf("busy 时不该动音量，实际 %v (vol=%d)", *sets, vol)
	}
}

// TestAdjustMusicVolumeKeepsRecordInSync 语音调音量必须同步 duck 状态和落盘记录，
// 否则停播还原会把自己调的值当成"用户手动改过"而跳过还原。
func TestAdjustMusicVolumeKeepsRecordInSync(t *testing.T) {
	resetMusicDuck(t)
	dir := t.TempDir()
	musicDuckFile = filepath.Join(dir, "music-duck.json")

	vol := 53
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.DuckMusicVolume() // 53 → 35
	if vol != defaultMusicDuckVolume {
		t.Fatalf("放歌音量 = %d, want %d", vol, defaultMusicDuckVolume)
	}

	before, after, err := s.AdjustMusicVolume(musicVolumeStep)
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if before != defaultMusicDuckVolume || after != defaultMusicDuckVolume+musicVolumeStep {
		t.Fatalf("before/after = %d/%d, want 35/45", before, after)
	}
	if vol != after {
		t.Fatalf("设备音量 = %d, want %d", vol, after)
	}

	rec, ok := loadMusicDuckRecord()
	if !ok || rec.Applied != after || rec.Original != 53 {
		t.Fatalf("记录 = %+v (ok=%v), want original=53 applied=%d", rec, ok, after)
	}

	s.RestoreMusicVolume()
	if vol != 53 {
		t.Fatalf("停播还原 = %d, want 53", vol)
	}
}

// TestAdjustBeyondOriginalRaisesBaseline 调到比用户原本音量还大时，基准跟着抬，
// 否则停播会突然被调小。
func TestAdjustBeyondOriginalRaisesBaseline(t *testing.T) {
	resetMusicDuck(t)
	vol := 40
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.DuckMusicVolume() // 40 → 35
	if vol != 35 {
		t.Fatalf("放歌音量 = %d, want 35", vol)
	}
	s.AdjustMusicVolume(musicVolumeStep) // 45
	s.AdjustMusicVolume(musicVolumeStep) // 55 > 40，基准应抬到 55

	s.RestoreMusicVolume()
	if vol != 55 {
		t.Fatalf("停播音量 = %d, want 55（基准已抬）", vol)
	}
}

// TestMuteAndUnmute 静音是临时的，取消后回到 duck 值。
func TestMuteAndUnmute(t *testing.T) {
	resetMusicDuck(t)
	vol := 53
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.DuckMusicVolume() // → 35
	if prev, acted, err := s.SetMusicMuted(true); err != nil || !acted || prev != 35 {
		t.Fatalf("mute: prev=%d err=%v", prev, err)
	}
	if vol != 0 {
		t.Fatalf("静音后音量 = %d, want 0", vol)
	}
	if got, acted, err := s.SetMusicMuted(false); err != nil || !acted || got != 35 {
		t.Fatalf("unmute: got=%d err=%v", got, err)
	}
	if vol != 35 {
		t.Fatalf("取消静音后音量 = %d, want 35", vol)
	}
	s.RestoreMusicVolume()
	if vol != 53 {
		t.Fatalf("停播还原 = %d, want 53", vol)
	}
}

// TestAdjustWhileMutedUnmutes 静音状态下说"大点声"应该取消静音并生效。
func TestAdjustWhileMutedUnmutes(t *testing.T) {
	resetMusicDuck(t)
	vol := 53
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.DuckMusicVolume() // → 35
	s.SetMusicMuted(true)
	if vol != 0 {
		t.Fatalf("静音后音量 = %d, want 0", vol)
	}
	if _, after, err := s.AdjustMusicVolume(musicVolumeStep); err != nil || after != 45 {
		t.Fatalf("after=%d err=%v, want 45", after, err)
	}
	if vol != 45 {
		t.Fatalf("音量 = %d, want 45", vol)
	}
	muted := s.stateSnapshot().mutedPrev
	if muted != -1 {
		t.Fatalf("应已取消静音，musicMutedPrev=%d", muted)
	}
}

// TestRestoreClearsMute 停播时静音状态要一起清掉。
func TestRestoreClearsMute(t *testing.T) {
	resetMusicDuck(t)
	vol := 53
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.DuckMusicVolume()
	s.SetMusicMuted(true)
	s.RestoreMusicVolume()

	if vol != 53 {
		t.Fatalf("音量 = %d, want 53", vol)
	}
	muted := s.stateSnapshot().mutedPrev
	if muted != -1 {
		t.Fatalf("静音状态应被清掉，musicMutedPrev=%d", muted)
	}
}

// TestRestoreWhileMutedWithEqualOriginal original == applied（本来就不吵）时静音也要放回去，
// 否则会一直停在 0。
func TestRestoreWhileMutedWithEqualOriginal(t *testing.T) {
	resetMusicDuck(t)
	vol := 20
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set
	s.SetMusicDuck(true, 20)

	s.DuckMusicVolume() // 20 <= target，不用压
	s.SetMusicMuted(true)
	if vol != 0 {
		t.Fatalf("静音后 = %d, want 0", vol)
	}
	s.RestoreMusicVolume()
	if vol != 20 {
		t.Fatalf("还原后 = %d, want 20", vol)
	}
}

// TestRestoreWhileMutedWithoutDuck 没有 duck 状态时（比如 duck 被关掉），静音也要放回去，
// 否则音量会永远停在 0。
func TestRestoreWhileMutedWithoutDuck(t *testing.T) {
	resetMusicDuck(t)
	vol := 53
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set
	s.SetMusicDuck(false, 20)

	if _, acted, err := s.SetMusicMuted(true); err != nil || !acted {
		t.Fatalf("mute: acted=%v err=%v", acted, err)
	}
	if vol != 0 {
		t.Fatalf("静音后 = %d, want 0", vol)
	}
	s.RestoreMusicVolume()
	if vol != 53 {
		t.Fatalf("还原后 = %d, want 53", vol)
	}
}

// TestPendingRestoreHonorsMute 进程在静音状态下被 kill：记录里有 muted 标记，
// 重连后要还原成原音量，不能把 0 当成"用户手动改过"。
func TestPendingRestoreHonorsMute(t *testing.T) {
	resetMusicDuck(t)
	dir := t.TempDir()
	musicDuckFile = filepath.Join(dir, "music-duck.json")
	saveMusicDuckRecord(musicDuckRecord{Original: 53, Applied: 20, Muted: true})

	vol := 0
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	s.RestorePendingMusicDuck()
	if vol != 53 {
		t.Fatalf("音量 = %d, want 53", vol)
	}
	if _, err := os.Stat(musicDuckFile); !os.IsNotExist(err) {
		t.Fatalf("记录应被删除，stat err = %v", err)
	}
}

// TestSetMusicMutedReportsActed 重复静音/重复取消都要报告"没动手"。
func TestSetMusicMutedReportsActed(t *testing.T) {
	resetMusicDuck(t)
	vol := 40
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set

	if _, acted, _ := s.SetMusicMuted(false); acted {
		t.Fatal("没静音时取消静音不该报告已处理")
	}
	if _, acted, _ := s.SetMusicMuted(true); !acted {
		t.Fatal("第一次静音应报告已处理")
	}
	if _, acted, _ := s.SetMusicMuted(true); acted {
		t.Fatal("重复静音不该报告已处理")
	}
}

// TestVoiceVolumeCeiling 音乐播放中语音调音量最多到 25：
// 再高麦克风就被音乐盖住，连"小声点/停止"都听不见。
func TestVoiceVolumeCeiling(t *testing.T) {
	resetMusicDuck(t)
	vol := 20
	get, set, _ := fakeVolume(&vol)
	s := NewSpeaker()
	s.volumeGet, s.volumeSet = get, set
	s.SetMusicDuck(true, 20)

	s.DuckMusicVolume() // 20 → 20
	if vol != 20 {
		t.Fatalf("放歌音量 = %d, want 20", vol)
	}
	s.AdjustMusicVolume(musicVolumeStep) // 20 + 10 → 封顶 25
	if vol != voiceVolumeCeiling {
		t.Fatalf("第一次大声点 = %d, want %d", vol, voiceVolumeCeiling)
	}
	s.AdjustMusicVolume(musicVolumeStep) // 已到顶，不再加
	if vol != voiceVolumeCeiling {
		t.Fatalf("到顶后再大声点 = %d, want %d", vol, voiceVolumeCeiling)
	}
	s.AdjustMusicVolume(-musicVolumeStep) // 往下永远允许
	if vol != voiceVolumeCeiling-musicVolumeStep {
		t.Fatalf("小声点 = %d, want %d", vol, voiceVolumeCeiling-musicVolumeStep)
	}
}

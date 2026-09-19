package wirepod_ttr

import (
	"testing"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func TestIsBatteryQuery(t *testing.T) {
	vars.IntentList = []vars.JsonIntent{
		{
			Name:       "intent_battery_level",
			Keyphrases: []string{"现在的电量", "还有多少电", "电量", "battery level"},
		},
		{
			Name:       "intent_weather_extend",
			Keyphrases: []string{"天气"},
		},
	}
	defer func() { vars.IntentList = nil }()

	cases := []struct {
		in   string
		want bool
	}{
		{"现在的电量", true},
		{"现在的 电量", true},      // STT inserts a space
		{"现 在 的 电 量", true},   // STT inserts spaces everywhere
		{"还有多少电", true},
		{"你还有多少电啊", true},     // substring match
		{"battery level", true},
		{"What is the Battery Level", true},
		{"how is the battery level today", true},
		{"今天天气怎么样", false},
		{"设置一个闹钟", false},
		{"set a timer", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsBatteryQuery(tc.in); got != tc.want {
			t.Errorf("IsBatteryQuery(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsBatteryQueryEmptyIntentList(t *testing.T) {
	vars.IntentList = nil
	if IsBatteryQuery("电量") {
		t.Error("IsBatteryQuery should be false when no intents are loaded")
	}
}

func TestBatteryPercent(t *testing.T) {
	// the logarithmic discharge curve, same as the web UI
	// (webroot/js/battery.js getBatteryPercentage): 100% at 4.1V, 80% at
	// 3.85V, 0% at 3.5V, log10-scaled in between
	for _, tc := range []struct {
		volts float32
		want  int
	}{
		{4.2, 100},
		{4.1, 100},
		{3.975, 95}, // middle of the 4.1V-3.85V range: 80 + 20*log10(5.5)
		{3.85, 80},
		{3.675, 59}, // middle of the 3.85V-3.5V range: 80*log10(5.5)
		{3.5, 0},
		{4.35, 100}, // over full -> clamped
		{3.4, 0},    // under empty -> clamped
	} {
		resp := &vectorpb.BatteryStateResponse{BatteryVolts: tc.volts}
		if got, ok := batteryPercent(resp); !ok || got != tc.want {
			t.Errorf("batteryPercent(%vV) = %d, %v; want %d, true", tc.volts, got, ok, tc.want)
		}
	}

	// the firmware reports a full battery -> always 100%
	resp := &vectorpb.BatteryStateResponse{BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_FULL}
	if got, ok := batteryPercent(resp); !ok || got != 100 {
		t.Errorf("batteryPercent(FULL) = %d, %v; want 100, true", got, ok)
	}
	// full battery with a slightly sagged voltage snaps to 100%
	resp = &vectorpb.BatteryStateResponse{BatteryVolts: 3.9, BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_FULL}
	if got, ok := batteryPercent(resp); !ok || got != 100 {
		t.Errorf("batteryPercent(3.9V, FULL) = %d, %v; want 100, true", got, ok)
	}

	// no voltage reading (bot turned on whilst off the charger) -> assume a
	// reasonable 70%, same as the web UI
	resp = &vectorpb.BatteryStateResponse{}
	if got, ok := batteryPercent(resp); !ok || got != 70 {
		t.Errorf("batteryPercent(no voltage) = %d, %v; want 70, true", got, ok)
	}
	resp = &vectorpb.BatteryStateResponse{BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL}
	if got, ok := batteryPercent(resp); !ok || got != 70 {
		t.Errorf("batteryPercent(NOMINAL, no voltage) = %d, %v; want 70, true", got, ok)
	}
}

func TestBatterySpeechText(t *testing.T) {
	defer func() { vars.APIConfig.STT.Language = "" }()

	vars.APIConfig.STT.Language = "zh-CN"
	if got := batterySpeechText(85, true, false, false); got != "我现在的电量是百分之85。" {
		t.Errorf("zh-CN normal text wrong: %q", got)
	}
	if got := batterySpeechText(85, true, true, false); got != "我正在充电，现在的电量是百分之85。" {
		t.Errorf("zh-CN charging text wrong: %q", got)
	}
	if got := batterySpeechText(0, false, false, false); got != "抱歉，我现在读不到我的电量信息。" {
		t.Errorf("zh-CN unknown text wrong: %q", got)
	}

	vars.APIConfig.STT.Language = "en-US"
	if got := batterySpeechText(42, true, false, false); got != "My battery is at 42 percent." {
		t.Errorf("en-US normal text wrong: %q", got)
	}
	// low-battery remark is appended
	if got := batterySpeechText(8, true, false, true); got != "My battery is at 8 percent. My battery is low, I should go charge." {
		t.Errorf("en-US low text wrong: %q", got)
	}
	// unknown language falls back to English
	vars.APIConfig.STT.Language = "xx-XX"
	if got := batterySpeechText(42, true, false, false); got != "My battery is at 42 percent." {
		t.Errorf("fallback text wrong: %q", got)
	}
}

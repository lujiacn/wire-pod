package wirepod_ttr

import (
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func TestIsWhoAmIQuery(t *testing.T) {
	vars.IntentList = []vars.JsonIntent{
		{
			Name:       "intent_names_username_extend",
			Keyphrases: []string{"name is", "names", "name's", "my name is"},
		},
		{
			Name:       "intent_names_ask",
			Keyphrases: []string{"my name", "who am", "who am i", "我是谁"},
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
		{"who am i", true},
		{"Who am I", true},
		{"who am", true},
		{"what is my name", true},
		{"do you know my name", true},
		{"hey vector, who am i", true},
		{"我是谁", true},
		{"你 知 道 我是谁 吗", true}, // STT inserts spaces into CJK text
		// enrollment phrases must NOT be intercepted - the robot needs
		// intent_names_username to actually enroll the face
		{"my name is james", false},
		{"my name is 小明", false},
		{"", false},
		{"天气怎么样", false},
	}
	for _, tc := range cases {
		if got := IsWhoAmIQuery(tc.in); got != tc.want {
			t.Errorf("IsWhoAmIQuery(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsWhoAmIQueryEmptyIntentList(t *testing.T) {
	vars.IntentList = nil
	if IsWhoAmIQuery("who am i") {
		t.Error("IsWhoAmIQuery should be false when no intents are loaded")
	}
}

func TestWhoamiSpeechText(t *testing.T) {
	defer func() { vars.APIConfig.STT.Language = "" }()

	vars.APIConfig.STT.Language = "en-US"
	if got := whoamiSpeechText(WhoAmIStruct{FaceVisible: true, Name: "James", HasEnrolledFaces: true}); got != "You are James." {
		t.Errorf("known text wrong: %q", got)
	}
	if got := whoamiSpeechText(WhoAmIStruct{FaceVisible: true, HasEnrolledFaces: true}); got != whoamiTexts["en-US"].UnknownFaces {
		t.Errorf("unknown-with-faces text wrong: %q", got)
	}
	if got := whoamiSpeechText(WhoAmIStruct{FaceVisible: true}); got != whoamiTexts["en-US"].UnknownNoFaces {
		t.Errorf("unknown-no-faces text wrong: %q", got)
	}
	if got := whoamiSpeechText(WhoAmIStruct{}); got != whoamiTexts["en-US"].NoFace {
		t.Errorf("no-face text wrong: %q", got)
	}

	vars.APIConfig.STT.Language = "zh-CN"
	if got := whoamiSpeechText(WhoAmIStruct{FaceVisible: true, Name: "小明"}); got != "你是小明。" {
		t.Errorf("zh-CN known text wrong: %q", got)
	}

	// unknown language falls back to English
	vars.APIConfig.STT.Language = "xx-XX"
	if got := whoamiSpeechText(WhoAmIStruct{FaceVisible: true, Name: "James"}); got != "You are James." {
		t.Errorf("fallback text wrong: %q", got)
	}
}

package wirepod_ttr

import (
	"strings"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

// the strict contract enforced by the prompt: {{CommandName||parameter}}
// these tests make sure the parser honors it, tolerates sloppy LLM output,
// and never crashes chipper
func TestGetActionsFromString(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantCmd   int
		wantParam string
	}{
		{"basic", "{{setEyeColor||blue}}", ActionSetEyeColor, "blue"},
		{"embedded", "Sure! {{setEyeColor||blue}} My eyes are blue now.", ActionSetEyeColor, "blue"},
		{"case tolerant", "{{SetEyeColor||Blue}}", ActionSetEyeColor, "Blue"},
		{"spaces", "{{ setEyeColor || blue }}", ActionSetEyeColor, "blue"},
		{"single pipe", "{{setEyeColor|green}}", ActionSetEyeColor, "green"},
		{"natural language alias", "{{change eye color||blue}}", ActionSetEyeColor, "blue"},
		{"underscore alias", "{{change_eye_color||purple}}", ActionSetEyeColor, "purple"},
		{"chinese command alias", "{{改变眼睛颜色||红色}}", ActionSetEyeColor, "红色"},
		{"chinese param", "{{setEyeColor||蓝色}}", ActionSetEyeColor, "蓝色"},
		{"no param - must not panic", "{{setEyeColor}}", ActionSetEyeColor, ""},
		{"volume", "{{setVolume||3}}", ActionSetVolume, "3"},
		{"animation", "{{playAnimationWI||happy}}", ActionPlayAnimationWI, "happy"},
		{"drive with distance", "{{drive||forward:300}}", ActionDrive, "forward:300"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acts := GetActionsFromString(c.input)
			found := false
			for _, a := range acts {
				if a.Action == c.wantCmd {
					found = true
					if a.Parameter != c.wantParam {
						t.Fatalf("input %q: param = %q, want %q", c.input, a.Parameter, c.wantParam)
					}
				}
			}
			if !found {
				t.Fatalf("input %q: no action %d in %v", c.input, c.wantCmd, acts)
			}
		})
	}
}

func TestGetActionsFromStringUnknownCommand(t *testing.T) {
	acts := GetActionsFromString("Let me fly. {{fly||now}}")
	for _, a := range acts {
		if a.Action != ActionSayText {
			t.Fatalf("unknown command should be dropped, got action %d", a.Action)
		}
	}
}

func TestParseEyeColor(t *testing.T) {
	cases := []struct {
		input string
		hue   float32
		sat   float32
		ok    bool
	}{
		{"blue", 240, 1, true},
		{"Blue", 240, 1, true},
		{"light blue", 240, 1, true},
		{"sky blue.", 240, 1, true},
		{"green", 120, 1, true},
		{"red", 0, 1, true},
		{"cyan", 180, 1, true},
		{"pink", 330, 1, true},
		{"white", 0, 0, true},
		{"240", 240, 1, true},
		{"390", 30, 1, true},
		{"蓝色", 240, 1, true},
		{"绿色", 120, 1, true},
		{"白色", 0, 0, true},
		// fallback usage: the user's full transcribed utterance
		{"change your eye color to blue", 240, 1, true},
		{"can you make your eyes green please", 120, 1, true},
		{"把眼睛颜色改成蓝色", 240, 1, true},
		{"眼睛颜色改成绿色", 120, 1, true},
		// must NOT match
		{"", 0, 0, false},
		{"chartreuse", 0, 0, false},
		{"scarlet oh wait no color change", 0, 1, true}, // contains "scarlet" - a real color name
		{"change my color", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			hue, sat, ok := parseEyeColor(c.input)
			if ok != c.ok {
				t.Fatalf("parseEyeColor(%q) ok = %v, want %v", c.input, ok, c.ok)
			}
			if ok && (hue != c.hue || sat != c.sat) {
				t.Fatalf("parseEyeColor(%q) = %v/%v, want %v/%v", c.input, hue, sat, c.hue, c.sat)
			}
		})
	}
}

func TestParseEyeColorWordBoundaries(t *testing.T) {
	// "scarlet" is a color (red), but padding must prevent arbitrary
	// substring hits - e.g. "wintered" must not match "red"
	if _, _, ok := parseEyeColor("wintered"); ok {
		t.Fatal("'wintered' must not match any color")
	}
	if h, _, ok := parseEyeColor("scarlet"); !ok || h != 0 {
		t.Fatalf("'scarlet' should match red hue 0, got %v/%v", h, ok)
	}
}

// the prompt is the primary enforcement of the strict format - make sure it
// keeps the rules and a Chinese few-shot example for setEyeColor
func TestCreatePromptStrictContract(t *testing.T) {
	oldEnable := vars.APIConfig.Knowledge.CommandsEnable
	defer func() { vars.APIConfig.Knowledge.CommandsEnable = oldEnable }()
	vars.APIConfig.Knowledge.CommandsEnable = true
	prompt := CreatePrompt("You are Vector.", "test-model", false)
	for _, want := range []string{
		"{{CommandName||parameter}}",
		"never write a command without a parameter",
		"always written in English",
		"{{setEyeColor||blue}}",
		"{{setEyeColor||green}}",
		"把眼睛颜色改成绿色",
	} {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(want)) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

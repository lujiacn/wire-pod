package wirepod_ttr

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/sashabaranov/go-openai"
)

const (
	// arg: text to say
	// not a command
	ActionSayText = 0
	// arg: animation name
	ActionPlayAnimation = 1
	// arg: animation name
	ActionPlayAnimationWI = 2
	// arg: now
	ActionGetImage   = 3
	ActionNewRequest = 4
	// arg: color name or hue 0-359
	ActionSetEyeColor = 5
	// arg: 1-5 / low..high
	ActionSetVolume = 6
	// arg: up / down
	ActionMoveHead = 7
	// arg: up / down
	ActionMoveLift = 8
	// arg: forward / backward [:mm]
	ActionDrive = 9
	// arg: left / right [:degrees]
	ActionTurn = 10
	// arg: now
	ActionGoHome = 11
	// arg: now
	ActionGoToSleep = 12
	// arg: sound file
	ActionPlaySound = 4
)

var animationMap [][2]string = [][2]string{
	//"happy, veryHappy, sad, verySad, angry, dartingEyes, confused, thinking, celebrate"
	{
		"happy",
		"anim_onboarding_reacttoface_happy_01",
	},
	{
		"veryHappy",
		"anim_blackjack_victorwin_01",
	},
	{
		"sad",
		"anim_feedback_meanwords_01",
	},
	{
		"verySad",
		"anim_feedback_meanwords_01",
	},
	{
		"angry",
		"anim_rtpickup_loop_10",
	},
	{
		"frustrated",
		"anim_feedback_shutup_01",
	},
	{
		"dartingEyes",
		"anim_observing_self_absorbed_01",
	},
	{
		"confused",
		"anim_meetvictor_lookface_timeout_01",
	},
	{
		"thinking",
		"anim_explorer_scan_short_04",
	},
	{
		"celebrate",
		"anim_pounce_success_03",
	},
	{
		"love",
		"anim_feedback_iloveyou_02",
	},
}

var soundMap [][2]string = [][2]string{
	{
		"drumroll",
		"sounds/drumroll.wav",
	},
}

type RobotAction struct {
	Action    int
	Parameter string
}

type LLMCommand struct {
	Command         string
	Description     string
	ParamChoices    string
	Action          int
	SupportedModels []string
}

// create function which parses from LLM and makes a struct of RobotActions

var ValidLLMCommands []LLMCommand = []LLMCommand{
	{
		Command:         "playAnimationWI",
		Description:     "Plays an animation on the robot without interrupting speech. This should be used FAR more than the playAnimation command. This is great for storytelling and making any normal response animated. Don't put two of these right next to each other. Use this MANY times. The param choices are the only choices you have. You can't create any.",
		ParamChoices:    "happy, veryHappy, sad, verySad, angry, frustrated, dartingEyes, confused, thinking, celebrate, love",
		Action:          ActionPlayAnimationWI,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "playAnimation",
		Description:     "Plays an animation on the robot. This will interrupt speech. Only use this if you are directed to play an animaion.",
		ParamChoices:    "happy, veryHappy, sad, verySad, angry, frustrated, dartingEyes, confused, thinking, celebrate, love",
		Action:          ActionPlayAnimation,
		SupportedModels: []string{"all"},
	},
	{
		Command:     "getImage",
		Description: "Gets an image from the robot's camera and places it in the next message. If you want to do this, tell the user what you are about to do THEN use the command. This command should END a sentence. Your response will be stopped when this command is recognized. If a user says something like 'what do you see', you should assume that you need to take a new photo. Do NOT automatically assume that you are analyzing a previous photo.",
		// not impl yet
		ParamChoices:    "front, lookingUp",
		Action:          ActionGetImage,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "newVoiceRequest",
		Description:     "Starts a new voice command from the robot. Use this if you want more input from the user after your response/if you want to carry out a conversation. Below this, there should be a NOTE telling you whether you are in conversation mode or not. If you are, DONT BE AFRAID TO USE THIS COMMAND! This goes at the end of your response, if you use it.",
		ParamChoices:    "now",
		Action:          ActionNewRequest,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "setEyeColor",
		Description:     "Changes the color of the robot's eyes. Use this whenever the user asks to change the eye color, in any language. The parameter is required: an English color name from the parameter choices, or a hue number (0-359). Example: {{setEyeColor||blue}}.",
		ParamChoices:    "red, orange, yellow, green, cyan, blue, purple, pink, white, or a hue number 0-359",
		Action:          ActionSetEyeColor,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "setVolume",
		Description:     "Changes the robot's speaker volume. Use this when the user asks to be louder or quieter.",
		ParamChoices:    "1, 2, 3, 4, 5 (1 = quietest, 5 = loudest) or low, medium, high",
		Action:          ActionSetVolume,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "moveHead",
		Description:     "Moves the robot's head up or down. Only use this if the user asks you to move your head or look up/down. Do not combine with other commands in the same sentence.",
		ParamChoices:    "up, down",
		Action:          ActionMoveHead,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "moveLift",
		Description:     "Moves the robot's lift (the arm between the wheels) up or down. Only use this if the user asks you to raise or lower your lift or arms.",
		ParamChoices:    "up, down",
		Action:          ActionMoveLift,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "drive",
		Description:     "Drives the robot straight forward or backward. The optional number after the colon is the distance in millimeters (default 200, max 1000). Only use this if the user asks you to move or come closer.",
		ParamChoices:    "forward, backward, forward:300, backward:500",
		Action:          ActionDrive,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "turn",
		Description:     "Turns the robot in place to the left or right. The optional number after the colon is the angle in degrees (default 90, max 360). Only use this if the user asks you to turn or spin around.",
		ParamChoices:    "left, right, left:180, right:45",
		Action:          ActionTurn,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "goHome",
		Description:     "Drives the robot back to its charging station (its home). Only use this if the user explicitly asks you to go home, go to your charger, or go charge yourself (e.g. 回家, 充电). This command should END your response - say a very short goodbye first, then use it. After this command the robot starts driving to the charger, so do not add any more sentences.",
		ParamChoices:    "now",
		Action:          ActionGoHome,
		SupportedModels: []string{"all"},
	},
	{
		Command:         "goToSleep",
		Description:     "Puts the robot to sleep. Only use this if the user explicitly tells you to go to sleep or go to bed (e.g. 睡觉, 睡觉吧). This is NOT for questions about sleep. This command should END your response - say a very short goodnight first, then use it. After this command the robot goes to sleep, so do not add any more sentences.",
		ParamChoices:    "now",
		Action:          ActionGoToSleep,
		SupportedModels: []string{"all"},
	},
	// {
	// 	Command:      "playSound",
	// 	Description:  "Plays a sound on the robot.",
	// 	ParamChoices: "drumroll",
	// 	Action:       ActionPlaySound,
	// },
}

func ModelIsSupported(cmd LLMCommand, model string) bool {
	for _, str := range cmd.SupportedModels {
		if str == "all" || str == model {
			return true
		}
	}
	return false
}

func CreatePrompt(origPrompt string, model string, isKG bool) string {
	prompt := origPrompt + "\n\n" + "Keep in mind, user input comes from speech-to-text software, so respond accordingly. No special characters like ampersand, caret, asterisk, hash or at sign. Always use normal sentence punctuation and end every sentence with a period, question mark or exclamation mark. No lists. No formatting. No emoji."
	prompt = prompt + "\n\n" + QwenTTSLanguageInstruction()
	prompt = prompt + "\n\n" + "Today's date is " + time.Now().Format("Monday, January 2, 2006") + "."
	if vars.APIConfig.Knowledge.CommandsEnable {
		prompt = prompt + "\n\n" + "You are running ON an Anki Vector robot. You have a set of commands to control the robot's body. A command uses the exact syntax {{CommandName||parameter}} and can be embedded anywhere inside your sentences. You may use several commands in one reply. Square brackets ([]) are not valid. If you include an emoji, I will make you start over."
		prompt = prompt + "\n\n" + "Command rules, follow them exactly: 1. CommandName must be copied character for character from the command list below. Never invent, translate, or rephrase a command name. 2. Every command must include || followed by one parameter. Never write a command without a parameter. 3. The parameter must be one of the command's listed parameter choices, always written in English, no matter which language the user speaks. If the user names a value in another language, translate it into the English parameter choice first. 4. Your spoken reply stays in the user's language; only commands and parameters are always in English. 5. If the command you want doesn't exist or your desired parameter isn't in the list, don't use any command."
		prompt = prompt + "\n\n" + "Examples. User: How are you feeling? | Response: {{playAnimationWI||sad}} I'm feeling sad... User: Change your eye color to blue. | Response: Sure! {{setEyeColor||blue}} My eyes are blue now. User: 把眼睛颜色改成绿色 | Response: 好的！{{setEyeColor||green}} 我的眼睛变成绿色了。 User: 睡觉吧 | Response: 好的，晚安！{{goToSleep||now}} User: 回家吧 | Response: 好的，我回家充电了。{{goHome||now}}"
		prompt = prompt + "\n\n" + "Use the playAnimation or playAnimationWI commands if you want to express emotion! You are very animated and good at following instructions. Animation takes precendence over words. You are to include many animations in your response.\n\nHere is every valid command:"
		for _, cmd := range ValidLLMCommands {
			if ModelIsSupported(cmd, model) {
				promptAppendage := "\n\nCommand Name: " + cmd.Command + "\nDescription: " + cmd.Description + "\nParameter choices: " + cmd.ParamChoices
				prompt = prompt + promptAppendage
			}
		}
		if isKG && vars.APIConfig.Knowledge.SaveChat {
			promptAppentage := "\n\nNOTE: You are in 'conversation' mode. If you ask the user a question near the end of your response, you MUST use newVoiceRequest. If you decide you want to end the conversation, you should not use it."
			prompt = prompt + promptAppentage
		} else {
			promptAppentage := "\n\nNOTE: You are NOT in 'conversation' mode. Refrain from asking the user any questions and from using newVoiceRequest."
			prompt = prompt + promptAppentage
		}
	}
	if os.Getenv("DEBUG_PRINT_PROMPT") == "true" {
		logger.Println(prompt)
	}
	return prompt
}

func GetActionsFromString(input string) []RobotAction {
	splitInput := strings.Split(input, "{{")
	if len(splitInput) == 1 {
		return []RobotAction{
			{
				Action:    ActionSayText,
				Parameter: input,
			},
		}
	}
	var actions []RobotAction
	for _, spl := range splitInput {
		if strings.TrimSpace(spl) == "" {
			continue
		}
		if !strings.Contains(spl, "}}") {
			// sayText
			action := RobotAction{
				Action:    ActionSayText,
				Parameter: strings.TrimSpace(spl),
			}
			actions = append(actions, action)
			continue
		}

		inner := strings.TrimSpace(strings.Split(spl, "}}")[0])
		// strict format is {{command||parameter}}; tolerate a single | and a
		// missing parameter so a sloppy LLM reply can't crash chipper
		sep := "||"
		if !strings.Contains(inner, sep) && strings.Contains(inner, "|") {
			sep = "|"
		}
		cmdPlusParam := strings.Split(inner, sep)
		cmd := strings.TrimSpace(cmdPlusParam[0])
		param := ""
		if len(cmdPlusParam) > 1 {
			param = strings.TrimSpace(cmdPlusParam[1])
		}
		action := CmdParamToAction(cmd, param)
		if action.Action != -1 {
			actions = append(actions, action)
		}
		if len(strings.Split(spl, "}}")) != 1 {
			action := RobotAction{
				Action:    ActionSayText,
				Parameter: strings.TrimSpace(strings.Split(spl, "}}")[1]),
			}
			actions = append(actions, action)
		}
	}
	return actions
}

// commandAliases maps a normalized, natural-language command name (what an
// LLM tends to write instead of the exact name, e.g. "change eye color") to
// the exact command name from ValidLLMCommands.
var commandAliases = map[string]string{
	"changeeyecolor":    "setEyeColor",
	"changeeyecolour":   "setEyeColor",
	"changeeyescolor":   "setEyeColor",
	"changeeyescolour":  "setEyeColor",
	"eyecolor":          "setEyeColor",
	"eyecolour":         "setEyeColor",
	"eyescolor":         "setEyeColor",
	"seteyecolour":      "setEyeColor",
	"改变眼睛颜色":          "setEyeColor",
	"更改眼睛颜色":          "setEyeColor",
	"设置眼睛颜色":          "setEyeColor",
	"眼睛颜色":            "setEyeColor",
	"changevolume":      "setVolume",
	"setvolumelevel":    "setVolume",
	"changevolumelevel": "setVolume",
	"volumelevel":       "setVolume",
	"takephoto":         "getImage",
	"takeapicture":      "getImage",
	"takepicture":       "getImage",
	"newrequest":        "newVoiceRequest",
	"gotohome":          "goHome",
	"drivetocharger":    "goHome",
	"gotocharger":       "goHome",
	"drivetochargingstation": "goHome",
	"charge":            "goHome",
	"gocharge":          "goHome",
	"回家":              "goHome",
	"回家吧":             "goHome",
	"回充电座":            "goHome",
	"去充电":             "goHome",
	"充电":              "goHome",
	"sleep":             "goToSleep",
	"gotobed":           "goToSleep",
	"fallasleep":        "goToSleep",
	"睡觉":              "goToSleep",
	"睡觉吧":             "goToSleep",
	"去睡觉":             "goToSleep",
	"睡眠":              "goToSleep",
	"晚安":              "goToSleep",
}

// normalizeCommandName lowercases and strips everything except letters and
// digits so that e.g. "Set Eye Color", "set_eye_color" and "setEyeColor"
// all compare equal.
func normalizeCommandName(cmd string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(cmd) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func CmdParamToAction(cmd, param string) RobotAction {
	for _, command := range ValidLLMCommands {
		if cmd == command.Command {
			return RobotAction{
				Action:    command.Action,
				Parameter: param,
			}
		}
	}
	normalized := normalizeCommandName(cmd)
	for _, command := range ValidLLMCommands {
		if normalized == normalizeCommandName(command.Command) {
			logger.Println("LLM command '" + cmd + "' normalized to '" + command.Command + "'")
			return RobotAction{
				Action:    command.Action,
				Parameter: param,
			}
		}
	}
	if canonical, ok := commandAliases[normalized]; ok {
		for _, command := range ValidLLMCommands {
			if canonical == command.Command {
				logger.Println("LLM command '" + cmd + "' matched alias for '" + command.Command + "'")
				return RobotAction{
					Action:    command.Action,
					Parameter: param,
				}
			}
		}
	}
	logger.Println("LLM tried to do a command which doesn't exist: " + cmd + " (param: " + param + ")")
	return RobotAction{
		Action: -1,
	}
}

func DoPlayAnimation(animation string, robot *vector.Vector) error {
	for _, animThing := range animationMap {
		if animation == animThing[0] {
			StartAnim_Queue(robot.Cfg.SerialNo)
			robot.Conn.PlayAnimation(
				context.Background(),
				&vectorpb.PlayAnimationRequest{
					Animation: &vectorpb.Animation{
						Name: animThing[1],
					},
					Loops: 1,
				},
			)
			StopAnim_Queue(robot.Cfg.SerialNo)
			return nil
		}
	}
	logger.Println("Animation provided by LLM doesn't exist: " + animation)
	return nil
}

func DoPlayAnimationWI(animation string, robot *vector.Vector) error {
	for _, animThing := range animationMap {
		if animation == animThing[0] {
			go func() {
				StartAnim_Queue(robot.Cfg.SerialNo)
				robot.Conn.PlayAnimation(
					context.Background(),
					&vectorpb.PlayAnimationRequest{
						Animation: &vectorpb.Animation{
							Name: animThing[1],
						},
						Loops: 1,
					},
				)
				StopAnim_Queue(robot.Cfg.SerialNo)
			}()
			return nil
		}
	}
	logger.Println("Animation provided by LLM doesn't exist: " + animation)
	return nil
}

func DoPlaySound(sound string, robot *vector.Vector) error {
	for _, soundThing := range soundMap {
		if sound == soundThing[0] {
			logger.Println("Would play sound")
		}
	}
	logger.Println("Sound provided by LLM doesn't exist: " + sound)
	return nil
}

// motor actions require a unique id tag in the SDK range (2000001-3000000)
var actionTagCounter int32 = 2000000

func NextActionTag() int32 {
	return atomic.AddInt32(&actionTagCounter, 1)
}

// eyeColorHues maps color names to HSV hues (degrees) for SetEyeColor.
// Includes common synonyms and Chinese names in case the LLM echoes the
// user's own wording instead of the English parameter choice.
var eyeColorHues = map[string]float32{
	"red":       0,
	"scarlet":   0,
	"crimson":   350,
	"orange":    30,
	"gold":      55,
	"yellow":    60,
	"lime":      90,
	"green":     120,
	"cyan":      180,
	"aqua":      180,
	"turquoise": 180,
	"teal":      180,
	"blue":      240,
	"purple":    285,
	"violet":    285,
	"lavender":  285,
	"magenta":   320,
	"pink":      330,
	"红色":        0,
	"红":         0,
	"橙色":        30,
	"橙":         30,
	"橘色":        30,
	"橘":         30,
	"金色":        55,
	"黄色":        60,
	"黄":         60,
	"绿色":        120,
	"绿":         120,
	"青色":        180,
	"青":         180,
	"蓝色":        240,
	"蓝":         240,
	"紫色":        285,
	"紫":         285,
	"品红":        320,
	"粉色":        330,
	"粉红":        330,
	"粉":         330,
}

// no hue can render white - saturation 0 does
var eyeColorWhiteNames = []string{"white", "白色", "白"}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// parseEyeColor extracts a color from free text - an exact color name, a
// multi-word form like "light blue", a Chinese color name, or a hue number
// 0-359. Works both on an LLM command parameter and on the user's raw
// transcribed speech (used as a fallback when the LLM drops the parameter).
func parseEyeColor(text string) (hue float32, saturation float32, ok bool) {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.NewReplacer("-", " ", "_", " ").Replace(t)
	t = strings.Trim(t, " \t.,!?;:\"'()[]{}")
	if t == "" {
		return 0, 0, false
	}
	for _, name := range eyeColorWhiteNames {
		if t == name {
			return 0, 0, true
		}
	}
	if h, hit := eyeColorHues[t]; hit {
		return h, 1, true
	}
	// whole parameter is a hue number (0-359)
	if parsed, err := strconv.ParseFloat(t, 32); err == nil {
		return float32(math.Mod(math.Abs(parsed), 360)), 1, true
	}
	// contains match, longest name first so 蓝色 wins over 蓝 and "pink" over
	// nothing shorter; ASCII names require word boundaries ("scarlet" must
	// not match "red"), CJK names are plain substrings
	names := make([]string, 0, len(eyeColorHues))
	for name := range eyeColorHues {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	padded := " " + t + " "
	for _, name := range names {
		if isASCIIString(name) {
			if strings.Contains(padded, " "+name+" ") {
				return eyeColorHues[name], 1, true
			}
		} else if strings.Contains(t, name) {
			return eyeColorHues[name], 1, true
		}
	}
	for _, name := range eyeColorWhiteNames {
		if isASCIIString(name) {
			if strings.Contains(padded, " "+name+" ") {
				return 0, 0, true
			}
		} else if strings.Contains(t, name) {
			return 0, 0, true
		}
	}
	return 0, 0, false
}

// DoSetEyeColor sets the robot's eye color. If the LLM's parameter is empty
// or unparseable, the color is extracted from the user's own transcribed
// speech (fallbackText) instead - e.g. the user said "change eye color to
// blue" but the LLM emitted {{setEyeColor}} without the color.
func DoSetEyeColor(param string, robot *vector.Vector, fallbackText string) error {
	hue, saturation, ok := parseEyeColor(param)
	if !ok && strings.TrimSpace(param) != strings.TrimSpace(fallbackText) {
		hue, saturation, ok = parseEyeColor(fallbackText)
		if ok {
			logger.Println("(eye color) color not in command parameter, took it from the user's speech instead")
		}
	}
	if !ok {
		logger.Println("(eye color) could not parse eye color parameter: " + param)
		return nil
	}
	logger.Println("(eye color) setting eye color, hue: " + fmt.Sprint(hue) + ", saturation: " + fmt.Sprint(saturation))
	_, err := robot.Conn.SetEyeColor(
		context.Background(),
		&vectorpb.SetEyeColorRequest{
			Hue:        hue,
			Saturation: saturation,
		},
	)
	if err != nil {
		logger.Println("(eye color) error: " + err.Error())
	}
	return nil
}

func DoSetVolume(param string, robot *vector.Vector) error {
	param = strings.ToLower(strings.TrimSpace(param))
	var level vectorpb.MasterVolumeLevel
	switch param {
	case "1", "low":
		level = vectorpb.MasterVolumeLevel_VOLUME_LOW
	case "2":
		level = vectorpb.MasterVolumeLevel_VOLUME_MEDIUM_LOW
	case "3", "medium":
		level = vectorpb.MasterVolumeLevel_VOLUME_MEDIUM
	case "4":
		level = vectorpb.MasterVolumeLevel_VOLUME_MEDIUM_HIGH
	case "5", "high", "max", "maximum":
		level = vectorpb.MasterVolumeLevel_VOLUME_HIGH
	default:
		logger.Println("(volume) could not parse volume parameter: " + param)
		return nil
	}
	logger.Println("(volume) setting master volume to " + param)
	_, err := robot.Conn.SetMasterVolume(
		context.Background(),
		&vectorpb.MasterVolumeRequest{
			VolumeLevel: level,
		},
	)
	if err != nil {
		logger.Println("(volume) error: " + err.Error())
	}
	return nil
}

func DoMoveHead(param string, robot *vector.Vector) error {
	param = strings.ToLower(strings.TrimSpace(param))
	var speed float32
	switch param {
	case "up":
		speed = 2.2
	case "down":
		speed = -2.2
	default:
		logger.Println("(move head) could not parse parameter: " + param)
		return nil
	}
	logger.Println("(move head) moving head " + param)
	_, err := robot.Conn.MoveHead(
		context.Background(),
		&vectorpb.MoveHeadRequest{
			SpeedRadPerSec: speed,
		},
	)
	if err != nil {
		logger.Println("(move head) error: " + err.Error())
	}
	return nil
}

func DoMoveLift(param string, robot *vector.Vector) error {
	param = strings.ToLower(strings.TrimSpace(param))
	var speed float32
	switch param {
	case "up":
		speed = 2.2
	case "down":
		speed = -2.2
	default:
		logger.Println("(move lift) could not parse parameter: " + param)
		return nil
	}
	logger.Println("(move lift) moving lift " + param)
	_, err := robot.Conn.MoveLift(
		context.Background(),
		&vectorpb.MoveLiftRequest{
			SpeedRadPerSec: speed,
		},
	)
	if err != nil {
		logger.Println("(move lift) error: " + err.Error())
	}
	return nil
}

func DoDrive(param string, robot *vector.Vector) error {
	param = strings.ToLower(strings.TrimSpace(param))
	distance := 200.0
	if strings.Contains(param, ":") {
		parts := strings.SplitN(param, ":", 2)
		if mm, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 32); err == nil && mm > 0 && mm <= 1000 {
			distance = mm
		}
		param = strings.TrimSpace(parts[0])
	}
	var speed float32 = 80.0
	switch param {
	case "forward":
	case "backward":
		speed = -speed
	default:
		logger.Println("(drive) could not parse parameter: " + param)
		return nil
	}
	logger.Println("(drive) driving " + param + " " + fmt.Sprint(distance) + "mm")
	_, err := robot.Conn.DriveStraight(
		context.Background(),
		&vectorpb.DriveStraightRequest{
			SpeedMmps:           speed,
			DistMm:              float32(distance),
			ShouldPlayAnimation: false,
			IdTag:               NextActionTag(),
		},
	)
	if err != nil {
		logger.Println("(drive) error: " + err.Error())
	}
	return nil
}

func DoTurn(param string, robot *vector.Vector) error {
	param = strings.ToLower(strings.TrimSpace(param))
	angleDeg := 90.0
	if strings.Contains(param, ":") {
		parts := strings.SplitN(param, ":", 2)
		if deg, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 32); err == nil && deg > 0 && deg <= 360 {
			angleDeg = deg
		}
		param = strings.TrimSpace(parts[0])
	}
	var angleRad float32
	switch param {
	case "left":
		angleRad = float32(angleDeg * math.Pi / 180)
	case "right":
		angleRad = float32(-angleDeg * math.Pi / 180)
	default:
		logger.Println("(turn) could not parse parameter: " + param)
		return nil
	}
	logger.Println("(turn) turning " + param + " " + fmt.Sprint(angleDeg) + " degrees")
	_, err := robot.Conn.TurnInPlace(
		context.Background(),
		&vectorpb.TurnInPlaceRequest{
			AngleRad:        angleRad,
			SpeedRadPerSec:  2.0,
			AccelRadPerSec2: 10.0,
			TolRad:          0.09,
			IsAbsolute:      0,
			IdTag:           NextActionTag(),
		},
	)
	if err != nil {
		logger.Println("(turn) error: " + err.Error())
	}
	return nil
}

func DoSayText(ctx context.Context, input string, robot *vector.Vector, pf *speechPrefetcher) error {
	// just before vector speaks
	input = removeSpecialCharacters(input)

	if strings.TrimSpace(input) == "" {
		return nil
	}

	// if the response was interrupted, say nothing
	if ctx.Err() != nil {
		return nil
	}

	// the audio for this sentence was usually prefetched while the previous
	// sentence was playing, so playback can start immediately; if the text
	// was never prefetched, synthesize it now (blocks and leaves a gap)
	chunks, err, ok := pf.get(ctx, input)
	if !ok {
		chunks, err = synthesizeSpeech(ctx, input)
	}
	if err == errSpeechBuiltin {
		return sayTextBuiltin(ctx, robot, input)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		logger.Println("TTS error: " + err.Error())
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	return playExternalAudioStream(ctx, robot, chunks)
}

// sayTextBuiltin speaks text with the robot's built-in Vector voice.
func sayTextBuiltin(ctx context.Context, robot *vector.Vector, input string) error {
	if ctx.Err() != nil {
		return nil
	}
	robot.Conn.SayText(
		ctx,
		&vectorpb.SayTextRequest{
			Text:           input,
			UseVectorVoice: true,
			DurationScalar: 0.95,
		},
	)

	return nil
}

func pcmLength(data []byte) time.Duration {
	bytesPerSample := 2
	sampleRate := 16000
	numSamples := len(data) / bytesPerSample
	duration := time.Duration(numSamples*1000/sampleRate) * time.Millisecond
	return duration
}

func getOpenAIVoice(voice string) openai.SpeechVoice {
	voiceMap := map[string]openai.SpeechVoice{
		"alloy":   openai.VoiceAlloy,
		"onyx":    openai.VoiceOnyx,
		"fable":   openai.VoiceFable,
		"shimmer": openai.VoiceShimmer,
		"nova":    openai.VoiceNova,
		"echo":    openai.VoiceEcho,
		"":        openai.VoiceFable,
	}
	return voiceMap[voice]
}

// TODO: done
// playExternalAudioStream plays 16 kHz PCM chunks on the robot's speaker
// through an external audio stream. Playback can be cut short by cancelling
// ctx (barge-in): the chunk sender aborts and the stream is closed, which
// stops the audio on the robot immediately. Blocks until the audio has fully
// played or ctx is cancelled, so callers can pace sentence-by-sentence.
// playExternalAudioStream plays 16 kHz PCM chunks on the robot's speaker
// through an external audio stream. It blocks until the robot confirms that
// playback has finished (AudioStreamPlaybackComplete), so the next sentence
// doesn't talk over this one and behavior control is not released while the
// speaker is still playing.
//
// Do not go back to pacing this with a byte-count estimate: the robot's
// actual playback lags behind the estimate (stream startup, jitter buffer,
// animations ducking the audio channel). The lag accumulates over a
// response, and for the last sentence the wait would expire early - the
// following behavior-control release then makes the robot drop the audio
// still in its buffer, so the user never hears the final sentence.
//
// The stream is tied to ctx, so a barge-in (ctx cancelled) tears the stream
// down and the robot stops the audio immediately.
func playExternalAudioStream(ctx context.Context, robot *vector.Vector, audioChunks [][]byte) error {
	// the firmware does not apply the robot's master volume to external
	// audio streams, so scale the PCM digitally to match the volume the
	// user set by voice (or in the web UI)
	applyMasterVolumeGain(robot, audioChunks)
	vclient, err := robot.Conn.ExternalAudioStreamPlayback(ctx)
	if err != nil {
		return err
	}
	vclient.Send(&vectorpb.ExternalAudioStreamRequest{
		AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamPrepare{
			AudioStreamPrepare: &vectorpb.ExternalAudioStreamPrepare{
				AudioFrameRate: 16000,
				AudioVolume:    100,
			},
		},
	})
	// the robot reports how playback actually progresses on this stream
	played := make(chan error, 1)
	go func() {
		for {
			resp, err := vclient.Recv()
			if err != nil {
				// io.EOF (robot closed without confirming) and ctx
				// cancellation (barge-in) are not playback errors
				if err == io.EOF || ctx.Err() != nil {
					played <- nil
				} else {
					played <- err
				}
				return
			}
			switch resp.GetAudioResponseType().(type) {
			case *vectorpb.ExternalAudioStreamResponse_AudioStreamPlaybackComplete:
				played <- nil
				return
			case *vectorpb.ExternalAudioStreamResponse_AudioStreamBufferOverrun:
				logger.Println("(audio) robot reported an audio buffer overrun (chunks arrived faster than it could play)")
			case *vectorpb.ExternalAudioStreamResponse_AudioStreamPlaybackFailyer:
				logger.Println("(audio) robot reported an audio playback failure")
			}
		}
	}()
	go func() {
		for _, chunk := range audioChunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			vclient.Send(&vectorpb.ExternalAudioStreamRequest{
				AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamChunk{
					AudioStreamChunk: &vectorpb.ExternalAudioStreamChunk{
						AudioChunkSizeBytes: uint32(len(chunk)),
						AudioChunkSamples:   chunk,
					},
				},
			})
			select {
			case <-time.After(time.Millisecond * 25):
			case <-ctx.Done():
				return
			}
		}
		vclient.Send(&vectorpb.ExternalAudioStreamRequest{
			AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamComplete{
				AudioStreamComplete: &vectorpb.ExternalAudioStreamComplete{},
			},
		})
	}()
	// fallback in case the firmware never confirms playback: the estimated
	// playback duration (16 kHz, 16-bit mono = 32000 bytes/s) plus slack
	var totalBytes int
	for _, chunk := range audioChunks {
		totalBytes += len(chunk)
	}
	timeout := time.Duration(totalBytes)*time.Millisecond/32 + 3*time.Second
	select {
	case err := <-played:
		return err
	case <-ctx.Done():
		// barge-in: the ctx-bound stream is torn down, cutting the audio
		return nil
	case <-time.After(timeout):
		logger.Println("(audio) no playback confirmation from the robot within " + timeout.String() + ", assuming it finished")
		return nil
	}
}

// synthesizeOpenAI synthesizes text with the OpenAI TTS API and returns the
// audio as 16 kHz PCM chunks ready for playExternalAudioStream.
func synthesizeOpenAI(ctx context.Context, input string) ([][]byte, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	openaiVoice := getOpenAIVoice(vars.APIConfig.Knowledge.OpenAIVoice)

	// Create configuration
	config := openai.DefaultConfig(strings.TrimSpace(vars.APIConfig.Knowledge.Key))

	// If OpenAIBase is not blank, use it as the base URL
	if baseURL := strings.TrimSpace(vars.APIConfig.Knowledge.OpenAIBase); baseURL != "" {
		config.BaseURL = baseURL
	}

	// Create client with the configuration
	client := openai.NewClientWithConfig(config)

	resp, err := client.CreateSpeech(ctx, openai.CreateSpeechRequest{
		Model:          openai.TTSModel1,
		Input:          input,
		Voice:          openaiVoice,
		ResponseFormat: openai.SpeechResponseFormatPcm,
	})

	if err != nil {
		logger.Println(err)
		return nil, err
	}
	speechBytes, _ := io.ReadAll(resp)
	return downsample24kTo16k(speechBytes), nil
}

func DoGetImage(ctx context.Context, msgs []openai.ChatCompletionMessage, param string, robot *vector.Vector) {
	stopImaging := func() bool { return ctx.Err() != nil }
	logger.Println("Get image here...")
	// get image
	robot.Conn.EnableMirrorMode(context.Background(), &vectorpb.EnableMirrorModeRequest{
		Enable: true,
	})
	for i := 3; i > 0; i-- {
		if stopImaging() {
			return
		}
		time.Sleep(time.Millisecond * 300)
		if stopImaging() {
			return
		}
		robot.Conn.SayText(
			context.Background(),
			&vectorpb.SayTextRequest{
				Text:           fmt.Sprint(i),
				UseVectorVoice: true,
				DurationScalar: 1.05,
			},
		)
		if stopImaging() {
			return
		}
	}
	resp, _ := robot.Conn.CaptureSingleImage(
		context.Background(),
		&vectorpb.CaptureSingleImageRequest{
			EnableHighResolution: true,
		},
	)
	robot.Conn.EnableMirrorMode(
		context.Background(),
		&vectorpb.EnableMirrorModeRequest{
			Enable: false,
		},
	)
	go func() {
		robot.Conn.PlayAnimation(
			context.Background(),
			&vectorpb.PlayAnimationRequest{
				Animation: &vectorpb.Animation{
					Name: "anim_photo_shutter_01",
				},
				Loops: 1,
			},
		)
	}()
	// encode to base64
	reqBase64 := base64.StdEncoding.EncodeToString(resp.Data)

	// add image to messages
	msgs = append(msgs, openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		MultiContent: []openai.ChatMessagePart{
			{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL:    fmt.Sprintf("data:image/jpeg;base64,%s", reqBase64),
					Detail: openai.ImageURLDetailLow,
				},
			},
		},
	})

	// recreate openai
	var fullRespText string
	var fullfullRespText string
	var fullRespSlice []string
	var isDone bool
	var c *openai.Client
	switch vars.APIConfig.Knowledge.Provider {
	case "together":
		if vars.APIConfig.Knowledge.Model == "" {
			vars.APIConfig.Knowledge.Model = "meta-llama/Llama-2-70b-chat-hf"
			vars.WriteConfigToDisk()
		}
		conf := openai.DefaultConfig(vars.APIConfig.Knowledge.Key)
		conf.BaseURL = "https://api.together.xyz/v1"
		c = openai.NewClientWithConfig(conf)
	case "openai":
		c = openai.NewClient(vars.APIConfig.Knowledge.Key)
	case "custom":
		conf := openai.DefaultConfig(vars.APIConfig.Knowledge.Key)
		conf.BaseURL = vars.APIConfig.Knowledge.Endpoint
		c = openai.NewClientWithConfig(conf)
	}
	speakReady := make(chan string)
	// prefetch TTS audio for upcoming sentences while the current one plays
	prefetcher := newSpeechPrefetcher(ctx)

	aireq := openai.ChatCompletionRequest{
		MaxCompletionTokens: 2048,
		Temperature:         1,
		TopP:                1,
		FrequencyPenalty:    0,
		PresencePenalty:     0,
		Messages:            msgs,
		Stream:              true,
	}
	if vars.APIConfig.Knowledge.Provider == "openai" {
		aireq.Model = openai.GPT4oMini
		logger.Println("Using " + aireq.Model)
	} else {
		logger.Println("Using " + vars.APIConfig.Knowledge.Model)
		aireq.Model = vars.APIConfig.Knowledge.Model
	}
	if stopImaging() {
		return
	}
	stream, err := c.CreateChatCompletionStream(ctx, aireq)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") && vars.APIConfig.Knowledge.Provider == "openai" {
			logger.Println("GPT-4 model cannot be accessed with this API key. You likely need to add more than $5 dollars of funds to your OpenAI account.")
			logger.LogUI("GPT-4 model cannot be accessed with this API key. You likely need to add more than $5 dollars of funds to your OpenAI account.")
			aireq.Model = openai.GPT3Dot5Turbo
			logger.Println("Falling back to " + aireq.Model)
			logger.LogUI("Falling back to " + aireq.Model)
			stream, err = c.CreateChatCompletionStream(ctx, aireq)
			if err != nil {
				logger.Println("OpenAI still not returning a response even after falling back. Erroring.")
				return
			}
		} else {
			logger.Println("LLM error: " + err.Error())
			return
		}
	}
	//defer stream.Close()

	fmt.Println("LLM stream response: ")
	go func() {
		for {
			response, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				if len(fullRespSlice) == 0 {
					if strings.TrimSpace(fullfullRespText) == "" {
						// LLM returned no content at all
						logger.Println("LLM returned no response")
						isDone = true
						return
					}
					// the LLM responded but used no sentence-ending
					// punctuation - speak the whole response as one chunk
					// instead of indexing into an empty slice
					logger.Println("LLM debug: response has no sentence punctuation, speaking it as one chunk")
					fullRespSlice = append(fullRespSlice, strings.TrimSpace(fullfullRespText))
					prefetcher.prefetchSentence(fullRespSlice[0])
				}
				isDone = true
				newStr := fullRespSlice[0]
				for i, str := range fullRespSlice {
					if i == 0 {
						continue
					}
					newStr = newStr + " " + str
				}
				if strings.TrimSpace(newStr) != strings.TrimSpace(fullfullRespText) {
					logger.Println("LLM debug: there is content after the last punctuation mark")
					extraBit := strings.TrimPrefix(fullRespText, newStr)
					fullRespSlice = append(fullRespSlice, extraBit)
				}
				if vars.APIConfig.Knowledge.SaveChat {
					Remember(msgs[len(msgs)-1],
						openai.ChatCompletionMessage{
							Role:    openai.ChatMessageRoleAssistant,
							Content: newStr,
						},
						robot.Cfg.SerialNo)
				}
				logger.LogUI("LLM response for " + robot.Cfg.SerialNo + ": " + newStr)
				logger.Println("LLM stream finished")
				return
			}

			if err != nil {
				logger.Println("Stream error: " + err.Error())
				return
			}
			if len(response.Choices) == 0 {
				logger.Println("Empty response chunk")
				continue
			}
			fullfullRespText = fullfullRespText + removeSpecialCharacters(response.Choices[0].Delta.Content)
			fullRespText = fullRespText + removeSpecialCharacters(response.Choices[0].Delta.Content)
			if strings.Contains(fullRespText, "...") || strings.Contains(fullRespText, ".'") || strings.Contains(fullRespText, ".\"") || strings.Contains(fullRespText, ".") || strings.Contains(fullRespText, "?") || strings.Contains(fullRespText, "!") {
				var sepStr string
				if strings.Contains(fullRespText, "...") {
					sepStr = "..."
				} else if strings.Contains(fullRespText, ".'") {
					sepStr = ".'"
				} else if strings.Contains(fullRespText, ".\"") {
					sepStr = ".\""
				} else if strings.Contains(fullRespText, ".") {
					sepStr = "."
				} else if strings.Contains(fullRespText, "?") {
					sepStr = "?"
				} else if strings.Contains(fullRespText, "!") {
					sepStr = "!"
				}
				splitResp := strings.Split(strings.TrimSpace(fullRespText), sepStr)
				fullRespSlice = append(fullRespSlice, strings.TrimSpace(splitResp[0])+sepStr)
				fullRespText = splitResp[1]
				if len(fullRespSlice) == 1 {
					// hide first-sentence synthesis latency
					prefetcher.prefetchSentence(fullRespSlice[0])
				}
				select {
				case speakReady <- strings.TrimSpace(splitResp[0]) + sepStr:
				default:
				}
			}
		}
	}()
	numInResp := 0
	for {
		if stopImaging() {
			return
		}
		respSlice := fullRespSlice
		if len(respSlice)-1 < numInResp {
			if !isDone {
				logger.Println("Waiting for more content from LLM...")
				select {
				case <-speakReady:
					respSlice = fullRespSlice
				case <-ctx.Done():
					return
				}
			} else {
				break
			}
		}
		if stopImaging() {
			return
		}
		logger.Println(respSlice[numInResp])
		acts := GetActionsFromString(respSlice[numInResp])
		// kick synthesis for the next sentences while this one plays, so
		// their audio is ready when it is their turn
		for i := numInResp + 1; i <= numInResp+speechPrefetchLookahead && i < len(respSlice); i++ {
			prefetcher.prefetchSentence(respSlice[i])
		}
		PerformActions(ctx, msgs, acts, robot, lastUserText(msgs), prefetcher)
		numInResp = numInResp + 1
		if stopImaging() {
			return
		}
	}
}

func DoNewRequest(robot *vector.Vector) {
	time.Sleep(time.Second / 3)
	robot.Conn.AppIntent(context.Background(), &vectorpb.AppIntentRequest{Intent: "knowledge_question"})
}

// DoGoHome drives the robot back to its charging station, like the native
// "go home" voice command (intent_system_charger). The intent must be sent
// after the LLM response has released behavior control, otherwise the
// robot's own behavior engine cannot run it - so it fires from a goroutine
// after a delay (same pattern as DoNewRequest).
func DoGoHome(robot *vector.Vector) {
	logger.Println("(go home) scheduling intent_system_charger after behavior control release")
	go func() {
		time.Sleep(time.Millisecond * 1500)
		_, err := robot.Conn.AppIntent(context.Background(), &vectorpb.AppIntentRequest{Intent: "intent_system_charger"})
		if err != nil {
			logger.Println("(go home) error sending intent: " + err.Error())
		}
	}()
}

// DoGoToSleep puts the robot to sleep, like the native "go to sleep" voice
// command (intent_system_sleep). See DoGoHome for why it is delayed.
func DoGoToSleep(robot *vector.Vector) {
	logger.Println("(go to sleep) scheduling intent_system_sleep after behavior control release")
	go func() {
		time.Sleep(time.Millisecond * 1500)
		_, err := robot.Conn.AppIntent(context.Background(), &vectorpb.AppIntentRequest{Intent: "intent_system_sleep"})
		if err != nil {
			logger.Println("(go to sleep) error sending intent: " + err.Error())
		}
	}()
}

// lastUserText returns the text of the newest user message in a chat
// history. Used as a fallback source for command parameters the LLM forgot
// to include (e.g. {{setEyeColor}} without the color - the user's own
// words "change eye color to blue" still contain it).
func lastUserText(msgs []openai.ChatCompletionMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != openai.ChatMessageRoleUser {
			continue
		}
		if msg.Content != "" {
			return msg.Content
		}
		var parts []string
		for _, part := range msg.MultiContent {
			if part.Type == openai.ChatMessagePartTypeText {
				parts = append(parts, part.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func PerformActions(ctx context.Context, msgs []openai.ChatCompletionMessage, actions []RobotAction, robot *vector.Vector, userText string, pf *speechPrefetcher) bool {
	// assuming we have behavior control already; if the response gets
	// interrupted (ctx cancelled), abort the remaining actions immediately
	for _, action := range actions {
		if ctx.Err() != nil {
			StopAnim_Queue(robot.Cfg.SerialNo)
			return false
		}
		switch {
		case action.Action == ActionSayText:
			DoSayText(ctx, action.Parameter, robot, pf)
		case action.Action == ActionPlayAnimation:
			DoPlayAnimation(action.Parameter, robot)
		case action.Action == ActionPlayAnimationWI:
			DoPlayAnimationWI(action.Parameter, robot)
		case action.Action == ActionNewRequest:
			go DoNewRequest(robot)
			return true
		case action.Action == ActionGetImage:
			DoGetImage(ctx, msgs, action.Parameter, robot)
			return true
		case action.Action == ActionGoHome:
			// end the response: the rest of the sentence loop is pointless
			// once the robot starts driving to its charger
			DoGoHome(robot)
			return true
		case action.Action == ActionGoToSleep:
			// end the response: the robot is going to sleep
			DoGoToSleep(robot)
			return true
		case action.Action == ActionPlaySound:
			DoPlaySound(action.Parameter, robot)
		case action.Action == ActionSetEyeColor:
			DoSetEyeColor(action.Parameter, robot, userText)
		case action.Action == ActionSetVolume:
			DoSetVolume(action.Parameter, robot)
		case action.Action == ActionMoveHead:
			DoMoveHead(action.Parameter, robot)
		case action.Action == ActionMoveLift:
			DoMoveLift(action.Parameter, robot)
		case action.Action == ActionDrive:
			DoDrive(action.Parameter, robot)
		case action.Action == ActionTurn:
			DoTurn(action.Parameter, robot)
		}
	}
	if ctx.Err() != nil {
		// interrupted - unblock any animation waiter instead of waiting out
		// the current animation
		StopAnim_Queue(robot.Cfg.SerialNo)
		return false
	}
	WaitForAnim_Queue(robot.Cfg.SerialNo)
	return false
}

func WaitForAnim_Queue(esn string) {
	for i, q := range AnimationQueues {
		if q.ESN == esn {
			if q.AnimCurrentlyPlaying {
				for range AnimationQueues[i].AnimDone {
					break
				}
				return
			}
		}
	}
}

func StartAnim_Queue(esn string) {
	// if animation is already playing, just wait for it to be done
	for i, q := range AnimationQueues {
		if q.ESN == esn {
			if q.AnimCurrentlyPlaying {
				for range AnimationQueues[i].AnimDone {
					logger.Println("(waiting for animation to be done...)")
					break
				}
			} else {
				AnimationQueues[i].AnimCurrentlyPlaying = true
			}
			return
		}
	}
	var aq AnimationQueue
	aq.AnimCurrentlyPlaying = true
	aq.AnimDone = make(chan bool)
	aq.ESN = esn
	AnimationQueues = append(AnimationQueues, aq)
}

func StopAnim_Queue(esn string) {
	for i, q := range AnimationQueues {
		if q.ESN == esn {
			AnimationQueues[i].AnimCurrentlyPlaying = false
			select {
			case AnimationQueues[i].AnimDone <- true:
			default:
			}
		}
	}
}

type AnimationQueue struct {
	ESN                  string
	AnimDone             chan bool
	AnimCurrentlyPlaying bool
}

var AnimationQueues []AnimationQueue

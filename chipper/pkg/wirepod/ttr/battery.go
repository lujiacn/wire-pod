package wirepod_ttr

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/vtt"
)

// Battery level voice query ("what is your battery level" / "现在的电量").
//
// There is no stock DDL intent which reports the battery level, so the
// intent_battery_level keyphrases (see intent-data/*.json) are intercepted
// here instead of being passed to the robot: the battery state is queried
// over the SDK, the voice command is closed with a greeting intent (the
// same trick StreamingKGSim uses), and the answer is then spoken through
// the SDK using the configured TTS pipeline, so non-English languages
// (e.g. Chinese via Qwen TTS) work too.

// Vector's single-cell LiPo battery. The voltage-to-percentage mapping is
// the same logarithmic discharge curve the web UI uses
// (webroot/js/battery.js getBatteryPercentage), so the spoken battery
// answer matches what the web interface shows for the same robot: 4.1V is
// full, 3.85V is the midpoint at 80%, 3.5V is empty.
const (
	batteryFullVolts  = 4.1
	batteryMidVolts   = 3.85
	batteryEmptyVolts = 3.5
)

type batterySpeechStrings struct {
	// format string with one %d verb for the percentage
	Level string
	// format string with one %d verb, used when the robot is charging
	Charging string
	// appended when the battery level is low
	Low string
	// used when the battery level could not be determined
	Unknown string
}

var batteryTexts = map[string]batterySpeechStrings{
	"en-US": {"My battery is at %d percent.", "I am charging right now, my battery is at %d percent.", "My battery is low, I should go charge.", "Sorry, I can't read my battery level right now."},
	"zh-CN": {"我现在的电量是百分之%d。", "我正在充电，现在的电量是百分之%d。", "电量有点低了，我该去充电了。", "抱歉，我现在读不到我的电量信息。"},
	"de-DE": {"Mein Akku ist bei %d Prozent.", "Ich lade gerade, mein Akku ist bei %d Prozent.", "Mein Akku ist fast leer, ich sollte mich aufladen.", "Entschuldigung, ich kann meinen Akkustand gerade nicht lesen."},
	"fr-FR": {"Ma batterie est à %d pour cent.", "Je suis en train de charger, ma batterie est à %d pour cent.", "Ma batterie est faible, je devrais aller me charger.", "Désolé, je n'arrive pas à lire mon niveau de batterie."},
	"it-IT": {"La mia batteria è al %d per cento.", "Sono in carica, la mia batteria è al %d per cento.", "La mia batteria è scarica, dovrei andare a caricarmi.", "Scusa, non riesco a leggere il livello della batteria."},
	"es-ES": {"Mi batería está al %d por ciento.", "Estoy cargando, mi batería está al %d por ciento.", "Mi batería está baja, debería ir a cargar.", "Lo siento, no puedo leer mi nivel de batería ahora."},
	"pt-BR": {"Minha bateria está em %d por cento.", "Estou carregando, minha bateria está em %d por cento.", "Minha bateria está fraca, eu deveria ir carregar.", "Desculpe, não consigo ler o nível da minha bateria agora."},
	"pl-PL": {"Poziom mojej baterii to %d procent.", "Ładuję się, poziom baterii to %d procent.", "Mam słabą baterię, powinienem się naładować.", "Przepraszam, nie mogę teraz odczytać poziomu baterii."},
	"ru-RU": {"Мой заряд батареи %d процентов.", "Я заряжаюсь, заряд батареи %d процентов.", "У меня низкий заряд, мне пора на зарядку.", "Извини, я не могу прочитать уровень заряда батареи."},
	"uk-UA": {"Мій заряд батареї %d відсотків.", "Я заряджаюся, заряд батареї %d відсотків.", "У мене низький заряд, мені час зарядитися.", "Вибач, я не можу прочитати рівень заряду батареї."},
	"tr-TR": {"Pil seviyem yüzde %d.", "Şu anda şarj oluyorum, pil seviyem yüzde %d.", "Pilim azaldı, şarj olmam lazım.", "Üzgünüm, pil seviyemi şu anda okuyamıyorum."},
	"nt-NL": {"Mijn batterij is op %d procent.", "Ik ben aan het opladen, mijn batterij is op %d procent.", "Mijn batterij is bijna leeg, ik moet gaan opladen.", "Sorry, ik kan mijn batterijniveau nu niet uitlezen."},
	"vi-VN": {"Pin của tôi còn %d phần trăm.", "Tôi đang sạc, pin của tôi còn %d phần trăm.", "Pin của tôi sắp hết, tôi cần đi sạc.", "Xin lỗi, tôi không đọc được mức pin của mình lúc này."},
	"ko-KR": {"제 배터리는 %d 퍼센트예요.", "지금 충전 중이에요, 배터리는 %d 퍼센트예요.", "배터리가 부족해요, 충전하러 가야 해요.", "죄송해요, 지금은 배터리 잔량을 읽을 수 없어요."},
}

// batteryPercent estimates the charge percentage from the battery state,
// consistent with the web UI: a full battery is always 100%, a missing
// voltage reading (the bot was turned on whilst off the charger, so the
// BatteryState response carries no voltage) assumes a reasonable 70%, and
// everything else follows the logarithmic discharge curve.
func batteryPercent(resp *vectorpb.BatteryStateResponse) (int, bool) {
	if resp.GetBatteryLevel() == vectorpb.BatteryLevel_BATTERY_LEVEL_FULL {
		return 100, true
	}
	volts := float64(resp.GetBatteryVolts())
	if volts <= 0.5 {
		return 70, true
	}
	return batteryPercentFromVolts(volts), true
}

// batteryPercentFromVolts maps a battery voltage to a percentage using the
// same logarithmic discharge curve as the web UI
// (webroot/js/battery.js getBatteryPercentage): a fast drop from 100% to
// 80% between 4.1V and 3.85V, then a gradual drop from 80% to 0% between
// 3.85V and 3.5V.
func batteryPercentFromVolts(volts float64) int {
	var pct float64
	switch {
	case volts >= batteryFullVolts:
		pct = 100
	case volts >= batteryMidVolts:
		scaled := (volts - batteryMidVolts) / (batteryFullVolts - batteryMidVolts)
		pct = 80 + 20*math.Log10(1+scaled*9)
	case volts >= batteryEmptyVolts:
		scaled := (volts - batteryEmptyVolts) / (batteryMidVolts - batteryEmptyVolts)
		pct = 80 * math.Log10(1+scaled*9)
	default:
		pct = 0
	}
	p := int(math.Round(pct))
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return p
}

func batterySpeechText(pct int, ok bool, isCharging bool, isLow bool) string {
	texts, exists := batteryTexts[vars.APIConfig.STT.Language]
	if !exists {
		texts = batteryTexts["en-US"]
	}
	if !ok {
		return texts.Unknown
	}
	var out string
	if isCharging {
		out = fmt.Sprintf(texts.Charging, pct)
	} else {
		out = fmt.Sprintf(texts.Level, pct)
	}
	if isLow {
		out = out + " " + texts.Low
	}
	return out
}

// IsBatteryQuery reports whether voiceText matches one of the
// intent_battery_level keyphrases from the loaded intent data. Besides the
// regular (case-insensitive) perfect/substring matching, the comparison is
// also done with all spaces removed, because STT engines insert arbitrary
// spaces into CJK text ("现 在 的 电 量") which would otherwise never match
// the keyphrases.
func IsBatteryQuery(voiceText string) bool {
	lower := strings.ToLower(voiceText)
	compact := strings.ReplaceAll(lower, " ", "")
	for _, intent := range vars.IntentList {
		if intent.Name != "intent_battery_level" {
			continue
		}
		for _, keyphrase := range intent.Keyphrases {
			kpLower := strings.ToLower(keyphrase)
			if kpLower == "" {
				continue
			}
			if lower == kpLower || (!intent.RequireExactMatch && strings.Contains(lower, kpLower)) {
				return true
			}
			kpCompact := strings.ReplaceAll(kpLower, " ", "")
			if kpCompact == "" {
				continue
			}
			if compact == kpCompact || (!intent.RequireExactMatch && strings.Contains(compact, kpCompact)) {
				return true
			}
		}
	}
	return false
}

// SayBatteryLevel handles the intent_battery_level intent: it queries the
// robot's battery state over the SDK, closes the voice command with a
// greeting intent and speaks the current battery level.
func SayBatteryLevel(req interface{}, speechText string) {
	var esn string
	if r, ok := req.(*vtt.IntentRequest); ok {
		esn = r.Device
	} else if r, ok := req.(*vtt.IntentGraphRequest); ok {
		esn = r.Device
	} else if r, ok := req.(*vtt.KnowledgeGraphRequest); ok {
		esn = r.Device
	} else {
		logger.Println("Battery intent: unknown request type")
		return
	}
	matched := false
	var guid string
	var target string
	for _, bot := range vars.BotInfo.Robots {
		if esn == bot.Esn {
			guid = bot.GUID
			target = bot.IPAddress + ":443"
			matched = true
			break
		}
	}
	if !matched {
		logger.Println("Battery intent: no SDK info known for bot " + esn + ", is it authenticated?")
		IntentPass(req, "intent_system_unmatched", speechText, map[string]string{}, false)
		return
	}
	robot, err := vector.New(vector.WithSerialNo(esn), vector.WithToken(guid), vector.WithTarget(target))
	if err != nil {
		logger.Println("Battery intent: error connecting to robot: " + err.Error())
		IntentPass(req, "intent_system_unmatched", speechText, map[string]string{}, false)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	batteryResp, err := robot.Conn.BatteryState(ctx, &vectorpb.BatteryStateRequest{})
	if err != nil {
		logger.Println("Battery intent: BatteryState error: " + err.Error())
		IntentPass(req, "intent_system_unmatched", speechText, map[string]string{}, false)
		return
	}
	pct, ok := batteryPercent(batteryResp)
	text := batterySpeechText(pct, ok, batteryResp.GetIsCharging(), batteryResp.GetBatteryLevel() == vectorpb.BatteryLevel_BATTERY_LEVEL_LOW)
	logger.LogUI("Bot " + esn + " battery level: " + fmt.Sprint(pct) + "%, volts: " + fmt.Sprint(batteryResp.GetBatteryVolts()) + ", charging: " + fmt.Sprint(batteryResp.GetIsCharging()))
	// close the voice command, then speak the answer over the SDK
	IntentPass(req, "intent_greeting_hello", speechText, map[string]string{}, false)
	go speakLocalAnswer(esn, robot, text)
}

// speakLocalAnswer speaks a locally-computed answer (battery level, face
// recognition result, ...) on the robot using the configured TTS pipeline
// (DoSayText -> API TTS when configured, built-in Vector voice otherwise).
// Modeled on the speaking part of StreamingKGSim, with the same barge-in
// support.
func speakLocalAnswer(esn string, robot *vector.Vector, text string) {
	respCtx, respCancel := context.WithCancel(context.Background())
	defer respCancel()
	respHandle := RegisterActiveResponse(esn, respCancel)
	defer UnregisterActiveResponse(esn, respHandle)
	ctx := context.Background()
	start := make(chan bool, 1)
	stop := make(chan bool, 2)
	stopStop := make(chan bool, 2)
	BControl(robot, ctx, start, stop)
	// watch for barge-in (wake word / touch) while the answer is spoken
	bstate := &bargeInState{}
	go InterruptKGSimWhenTouchedOrWaked(robot, respCancel, stopStop, bstate)
	// wait for behavior control, but don't hang forever if the robot never
	// grants it (e.g. it is busy executing the greeting intent that was just
	// sent to it)
	select {
	case <-start:
	case <-respCtx.Done():
	case <-time.After(8 * time.Second):
		logger.Println("Local answer: behavior control was not granted after 8 seconds, continuing anyway (robot may stay silent)")
	}
	if respCtx.Err() == nil {
		robot.Conn.PlayAnimation(
			ctx,
			&vectorpb.PlayAnimationRequest{
				Animation: &vectorpb.Animation{
					Name: "anim_getin_tts_01",
				},
				Loops: 1,
			},
		)
		var stopTTSLoop bool
		TTSLoopStopped := make(chan bool)
		go func() {
			for {
				if stopTTSLoop {
					TTSLoopStopped <- true
					break
				}
				robot.Conn.PlayAnimation(
					ctx,
					&vectorpb.PlayAnimationRequest{
						Animation: &vectorpb.Animation{
							Name: "anim_tts_loop_02",
						},
						Loops: 1,
					},
				)
			}
		}()
		bstate.markSpeaking()
		pf := newSpeechPrefetcher(respCtx)
		pf.prefetchText(text)
		if err := DoSayText(respCtx, text, robot, pf); err != nil {
			logger.Println("Local answer: error speaking text: " + err.Error())
		}
		stopTTSLoop = true
		<-TTSLoopStopped
	}
	time.Sleep(time.Millisecond * 100)
	// tell the barge-in watcher to exit (it may have already exited because
	// it triggered the interrupt)
	select {
	case stopStop <- true:
	default:
	}
	// always release behavior control - also after an interrupt, so the robot
	// goes back to normal and a new voice request can take control
	stop <- true
}

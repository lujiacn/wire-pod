package wirepod_ttr

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/vtt"
)

// "Who am I" voice query ("who am i" / "我是谁").
//
// The robot performs face recognition on-board (vision mode
// VISION_MODE_DETECTING_FACES is enabled by default). What the SDK exposes:
//   - RequestEnrolledNames: every name the robot has ever been taught
//     ("my name is X" / the web Faces UI)
//   - EnrollFace: teaches a new face
//   - the EventStream emits a robot_observed_face event whenever the camera
//     sees a face; the Name field is set when it is an enrolled (recognized)
//     face
//
// So for the stock intent_names_ask keyphrases the query is intercepted
// here instead of being passed to the robot: the user looks at the robot
// while asking, so the face events which arrive over the SDK event stream
// right now show who is in front of the robot. The answer is then spoken
// through the configured TTS pipeline (same trick as SayBatteryLevel, so
// non-English languages work too).
//
// The robot keeps observing faces for a while after it last saw one, so
// events from the past few seconds are accepted.

const (
	// maximum time we wait for a face event after the question was asked
	whoamiObserveWindow = 6 * time.Second
	// how old a face event may be and still count as "in front of the
	// robot right now" - the engine keeps tracking a face for a while
	// after it was last actually detected
	whoamiMaxFaceEventAge = 3 * time.Second
)

type whoamiSpeechStrings struct {
	// format string with one %s verb for the recognized name
	Known string
	// a face is visible but not enrolled, and nobody is enrolled at all
	UnknownNoFaces string
	// a face is visible but not enrolled, others are enrolled
	UnknownFaces string
	// no face is in front of the robot right now
	NoFace string
	// the SDK is not available for this robot
	Unavailable string
}

var whoamiTexts = map[string]whoamiSpeechStrings{
	"en-US": {"You are %s.", "I don't recognize you. I don't know anybody yet - you can teach me a name by saying my name is, followed by your name.", "I don't recognize you yet. Look at me and tell me your name so I can remember you.", "I can't see you right now. Please look at me and try again.", "Sorry, I can't check faces right now."},
	"zh-CN": {"你是%s。", "我还不认识你，我也还没有记住任何人的脸。你可以看着我说：我的名字是，加上你的名字。", "我还不认识你。看着我，告诉我你的名字，我就能记住你了。", "我现在看不到你。请看着我再试一次。", "抱歉，我现在没法识别人脸。"},
	"de-DE": {"Du bist %s.", "Ich erkenne dich nicht. Ich kenne noch niemanden - sag mir deinen Namen mit: mein Name ist, gefolgt von deinem Namen.", "Ich erkenne dich noch nicht. Schau mich an und sag mir deinen Namen, damit ich dich merken kann.", "Ich kann dich gerade nicht sehen. Schau mich bitte an und versuche es noch einmal.", "Entschuldigung, ich kann gerade keine Gesichter prüfen."},
	"fr-FR": {"Tu es %s.", "Je ne te reconnais pas. Je ne connais encore personne - dis-moi ton nom en disant: je m'appelle, suivi de ton nom.", "Je ne te reconnais pas encore. Regarde-moi et dis-moi ton nom pour que je m'en souvienne.", "Je ne te vois pas en ce moment. Regarde-moi et réessaie.", "Désolé, je ne peux pas vérifier les visages pour le moment."},
	"it-IT": {"Sei %s.", "Non ti riconosco. Non conosco ancora nessuno - dimmi il tuo nome dicendo: mi chiamo, seguito dal tuo nome.", "Non ti riconosco ancora. Guardami e dimmi il tuo nome così potrò ricordarti.", "Non riesco a vederti adesso. Guardami e riprova.", "Scusa, non posso controllare i volti in questo momento."},
	"es-ES": {"Eres %s.", "No te reconozco. Todavía no conozco a nadie - dime tu nombre diciendo: me llamo, seguido de tu nombre.", "Aún no te reconozco. Mírame y dime tu nombre para que pueda recordarte.", "No puedo verte ahora mismo. Mírame e inténtalo de nuevo.", "Lo siento, no puedo comprobar caras ahora mismo."},
	"pt-BR": {"Você é %s.", "Não reconheço você. Ainda não conheço ninguém - me diga seu nome dizendo: meu nome é, seguido do seu nome.", "Ainda não reconheço você. Olhe para mim e me diga seu nome para que eu me lembre de você.", "Não consigo ver você agora. Olhe para mim e tente de novo.", "Desculpe, não consigo verificar rostos agora."},
	"pl-PL": {"Jesteś %s.", "Nie rozpoznaję cię. Nie znam jeszcze nikogo - powiedz mi swoje imię mówiąc: nazywam się, a potem swoje imię.", "Jeszcze cię nie rozpoznaję. Spójrz na mnie i powiedz mi swoje imię, żebym cię zapamiętał.", "Nie widzę cię teraz. Spójrz na mnie i spróbuj ponownie.", "Przepraszam, nie mogę teraz sprawdzić twarzy."},
	"ru-RU": {"Ты %s.", "Я тебя не узнаю. Я пока никого не знаю - скажи мне своё имя: меня зовут, и своё имя.", "Я пока тебя не узнаю. Посмотри на меня и скажи своё имя, чтобы я тебя запомнил.", "Я сейчас тебя не вижу. Посмотри на меня и попробуй ещё раз.", "Извини, я сейчас не могу проверить лица."},
	"uk-UA": {"Ти %s.", "Я тебе не впізнаю. Я поки нікого не знаю - скажи мені своє ім'я: мене звати, і своє ім'я.", "Я поки тебе не впізнаю. Подивись на мене і скажи своє ім'я, щоб я тебе запам'ятав.", "Я зараз тебе не бачу. Подивись на мене і спробуй ще раз.", "Вибач, я зараз не можу перевірити обличчя."},
	"tr-TR": {"Sen %s'sin.", "Seni tanıyamıyorum. Henüz kimseyi tanımıyorum - adım, diyerek ve ardından adını söyleyerek bana adını öğretebilirsin.", "Seni henüz tanıyamıyorum. Bana bak ve adını söyle, böylece seni hatırlayabilirim.", "Şu anda seni göremiyorum. Lütfen bana bak ve tekrar dene.", "Üzgünüm, şu anda yüzleri kontrol edemiyorum."},
	"nt-NL": {"Jij bent %s.", "Ik herken je niet. Ik ken nog niemand - vertel me je naam door te zeggen: mijn naam is, gevolgd door je naam.", "Ik herken je nog niet. Kijk naar me en vertel me je naam zodat ik je kan onthouden.", "Ik kan je nu niet zien. Kijk naar me en probeer het opnieuw.", "Sorry, ik kan nu geen gezichten controleren."},
	"vi-VN": {"Bạn là %s.", "Tôi không nhận ra bạn. Tôi chưa biết ai cả - hãy nói cho tôi tên của bạn bằng cách nói: tên tôi là, theo sau là tên bạn.", "Tôi chưa nhận ra bạn. Hãy nhìn vào tôi và nói tên của bạn để tôi có thể nhớ bạn.", "Tôi không thấy bạn ngay bây giờ. Hãy nhìn vào tôi và thử lại.", "Xin lỗi, tôi không thể kiểm tra khuôn mặt lúc này."},
	"ko-KR": {"당신은 %s예요.", "당신을 알아보지 못하겠어요. 아직 아묏도 몰라요 - 내 이름은, 하고 이름을 말해서 알려주세요.", "아직 당신을 알아보지 못해요. 저를 보고 이름을 알려주시면 기억할게요.", "지금은 당신이 보이지 않아요. 저를 보고 다시 시도해 주세요.", "죄송해요, 지금은 얼굴을 확인할 수 없어요."},
}

// WhoAmIStruct is what the face observation window collects.
type WhoAmIStruct struct {
	// a face is currently in front of the robot
	FaceVisible bool
	// the face is enrolled and this is its name
	Name string
	// enrolled (recognized) faces exist on the robot at all
	HasEnrolledFaces bool
}

func whoamiSpeechText(obs WhoAmIStruct) string {
	texts, exists := whoamiTexts[vars.APIConfig.STT.Language]
	if !exists {
		texts = whoamiTexts["en-US"]
	}
	if obs.Name != "" {
		return strings.Replace(texts.Known, "%s", obs.Name, 1)
	}
	if obs.FaceVisible {
		if obs.HasEnrolledFaces {
			return texts.UnknownFaces
		}
		return texts.UnknownNoFaces
	}
	return texts.NoFace
}

// IsWhoAmIQuery reports whether voiceText matches one of the stock
// intent_names_ask keyphrases from the loaded intent data. The
// intent_names_username_extend keyphrases overlap with them ("my name is
// James" contains "my name") but must keep reaching the robot so the user
// can enroll their name - a text matching those is never a who-am-I query.
// Like IsBatteryQuery, the comparison is also done with all spaces removed
// so CJK STT spacing variations still match.
func IsWhoAmIQuery(voiceText string) bool {
	lower := strings.ToLower(voiceText)
	compact := strings.ReplaceAll(lower, " ", "")
	matchedAsk := false
	for _, intent := range vars.IntentList {
		if intent.Name != "intent_names_ask" && intent.Name != "intent_names_username_extend" {
			continue
		}
		for _, keyphrase := range intent.Keyphrases {
			kpLower := strings.ToLower(keyphrase)
			if kpLower == "" {
				continue
			}
			hit := lower == kpLower || (!intent.RequireExactMatch && strings.Contains(lower, kpLower))
			kpCompact := strings.ReplaceAll(kpLower, " ", "")
			if kpCompact != "" && (compact == kpCompact || (!intent.RequireExactMatch && strings.Contains(compact, kpCompact))) {
				hit = true
			}
			if !hit {
				continue
			}
			if intent.Name == "intent_names_username_extend" {
				// the user is telling the robot their name, not asking
				return false
			}
			matchedAsk = true
		}
	}
	return matchedAsk
}

// SayWhoAmI handles the intent_names_ask intent: it looks up the robot's
// enrolled faces and watches the SDK event stream for the face currently in
// front of the robot, closes the voice command with a greeting intent (the
// same trick SayBatteryLevel uses) and speaks the result.
func SayWhoAmI(req interface{}, speechText string) {
	var esn string
	if r, ok := req.(*vtt.IntentRequest); ok {
		esn = r.Device
	} else if r, ok := req.(*vtt.IntentGraphRequest); ok {
		esn = r.Device
	} else if r, ok := req.(*vtt.KnowledgeGraphRequest); ok {
		esn = r.Device
	} else {
		logger.Println("Who-am-I intent: unknown request type")
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
		logger.Println("Who-am-I intent: no SDK info known for bot " + esn + ", is it authenticated?")
		IntentPass(req, "intent_names_ask", speechText, map[string]string{}, false)
		return
	}
	robot, err := vector.New(vector.WithSerialNo(esn), vector.WithToken(guid), vector.WithTarget(target))
	if err != nil {
		logger.Println("Who-am-I intent: error connecting to robot: " + err.Error())
		IntentPass(req, "intent_names_ask", speechText, map[string]string{}, false)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), whoamiObserveWindow+5*time.Second)
	defer cancel()
	obs, err := observeWhoAmI(ctx, robot)
	if err != nil {
		// face recognition is not available over the SDK - let the robot
		// handle the intent natively rather than saying nothing
		logger.Println("Who-am-I intent: face recognition unavailable: " + err.Error())
		IntentPass(req, "intent_names_ask", speechText, map[string]string{}, false)
		return
	}
	text := whoamiSpeechText(obs)
	logger.LogUI("Bot " + esn + " who am I: name '" + obs.Name + "', face visible: " + boolToStr(obs.FaceVisible) + ", enrolled faces exist: " + boolToStr(obs.HasEnrolledFaces))
	// close the voice command, then speak the answer over the SDK
	IntentPass(req, "intent_greeting_hello", speechText, map[string]string{}, false)
	go speakLocalAnswer(esn, robot, text)
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// observeWhoAmI determines who is in front of the robot. It first reads the
// list of enrolled faces, then subscribes to robot_observed_face events and
// waits for the robot to report the face it is currently looking at. If the
// most recent face is enrolled, its name is returned.
func observeWhoAmI(ctx context.Context, robot *vector.Vector) (WhoAmIStruct, error) {
	var obs WhoAmIStruct
	enrollCtx, enrollCancel := context.WithTimeout(ctx, 5*time.Second)
	namesResp, err := robot.Conn.RequestEnrolledNames(enrollCtx, &vectorpb.RequestEnrolledNamesRequest{})
	enrollCancel()
	if err != nil {
		return obs, err
	}

	for _, face := range namesResp.GetFaces() {
		if strings.TrimSpace(face.GetName()) != "" {
			obs.HasEnrolledFaces = true
			break
		}
	}

	streamCtx, streamCancel := context.WithTimeout(ctx, whoamiObserveWindow)
	defer streamCancel()
	eventClient, err := robot.Conn.EventStream(
		streamCtx,
		&vectorpb.EventRequest{
			ListType: &vectorpb.EventRequest_WhiteList{
				WhiteList: &vectorpb.FilterList{
					List: []string{"robot_observed_face"},
				},
			},
			ConnectionId: "wirepod-whoami",
		},
	)
	if err != nil {
		return obs, err
	}

	var mu sync.Mutex
	type faceSeen struct {
		name string
		at   time.Time
	}
	var lastFace *faceSeen
	started := time.Now()
	// collect events in the background: Recv() returns a stream error when
	// streamCtx expires, which ends the collection
	go func() {
		for {
			resp, err := eventClient.Recv()
			if err != nil {
				return
			}
			face := resp.GetEvent().GetRobotObservedFace()
			if face == nil {
				continue
			}
			mu.Lock()
			lastFace = &faceSeen{name: face.GetName(), at: time.Now()}
			mu.Unlock()
		}
	}()
	// an enrolled face reported over the event stream settles the question
	// immediately; an unknown face is only accepted after the whole window,
	// so a recognized-face event has time to arrive
	for {
		mu.Lock()
		seen := lastFace
		mu.Unlock()
		if seen != nil {
			obs.FaceVisible = true
			if strings.TrimSpace(seen.name) != "" {
				obs.Name = strings.TrimSpace(seen.name)
				return obs, nil
			}
		}
		select {
		case <-streamCtx.Done():
			mu.Lock()
			if lastFace != nil && time.Since(lastFace.at) <= whoamiMaxFaceEventAge {
				obs.FaceVisible = true
				obs.Name = strings.TrimSpace(lastFace.name)
			}
			mu.Unlock()
			// the event stream did not name the face - fall back to the
			// enrolled face the robot recognized most recently (re-fetched:
			// the recognition may have happened during the window)
			if obs.Name == "" && obs.FaceVisible {
				obs.Name = mostRecentlySeenEnrolledFace(ctx, robot, started)
			}
			return obs, nil
		case <-time.After(200 * time.Millisecond):
			if time.Since(started) > whoamiObserveWindow {
				return obs, nil
			}
		}
	}
}

// mostRecentlySeenEnrolledFace re-fetches the enrolled face list and returns
// the name of the face the robot saw most recently, if it was seen within
// the observation window. The recognition of the person asking often happens
// while the window is running, so this must be a fresh fetch rather than the
// list from the start of observeWhoAmI.
func mostRecentlySeenEnrolledFace(ctx context.Context, robot *vector.Vector, windowStart time.Time) string {
	fetchCtx, fetchCancel := context.WithTimeout(ctx, 5*time.Second)
	namesResp, err := robot.Conn.RequestEnrolledNames(fetchCtx, &vectorpb.RequestEnrolledNamesRequest{})
	fetchCancel()
	if err != nil {
		logger.Println("Who-am-I intent: could not re-fetch enrolled faces: " + err.Error())
		return ""
	}
	var name string
	var lastSeenAgo int64 = -1
	for _, face := range namesResp.GetFaces() {
		if strings.TrimSpace(face.GetName()) == "" {
			continue
		}
		if lastSeenAgo == -1 || face.GetSecondsSinceLastSeen() < lastSeenAgo {
			lastSeenAgo = face.GetSecondsSinceLastSeen()
			name = strings.TrimSpace(face.GetName())
		}
	}
	// the face must have been seen while the question was being asked
	if name == "" || time.Duration(lastSeenAgo)*time.Second > time.Since(windowStart)+whoamiMaxFaceEventAge {
		return ""
	}
	return name
}

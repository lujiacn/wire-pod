package wirepod_dashscope

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	"github.com/orcaman/writerseeker"
)

// Aliyun DashScope speech-to-text (Qwen3-ASR-Flash).
//
// The robot streams microphone audio to wire-pod; when the robot stops
// talking, the accumulated PCM is wrapped in a WAV container, sent to the
// DashScope synchronous ASR endpoint as base64, and the recognized text is
// returned. Like the cloud Whisper provider, this is a one-shot
// transcription at end of speech (no partial results).

var Name string = "dashscope"

const defaultDashScopeEndpoint = "https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation"

// language codes accepted by asr_options.language (Qwen3-ASR-Flash)
var dashScopeLanguages = map[string]bool{
	"zh": true, "yue": true, "en": true, "ja": true, "de": true, "ko": true,
	"ru": true, "fr": true, "pt": true, "ar": true, "it": true, "es": true,
	"hi": true, "id": true, "th": true, "tr": true, "uk": true, "vi": true,
	"cs": true, "da": true, "fil": true, "fi": true, "is": true, "ms": true,
	"no": true, "pl": true, "sv": true,
}

type dashScopeContentPart struct {
	Text  string `json:"text,omitempty"`
	Audio string `json:"audio,omitempty"`
}

type dashScopeMessage struct {
	Role    string                 `json:"role"`
	Content []dashScopeContentPart `json:"content"`
}

type dashScopeASROptions struct {
	Language  string `json:"language,omitempty"`
	EnableITN bool   `json:"enable_itn"`
}

type dashScopeParameters struct {
	ASROptions *dashScopeASROptions `json:"asr_options,omitempty"`
}

type dashScopeReq struct {
	Model      string               `json:"model"`
	Input      dashScopeReqInput    `json:"input"`
	Parameters *dashScopeParameters `json:"parameters,omitempty"`
}

type dashScopeReqInput struct {
	Messages []dashScopeMessage `json:"messages"`
}

type dashScopeResp struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Output  struct {
		Choices []struct {
			Message struct {
				Content []dashScopeContentPart `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	} `json:"output"`
}

func Init() error {
	if os.Getenv("DASHSCOPE_API_KEY") == "" && strings.TrimSpace(vars.APIConfig.STT.DashScopeKey) == "" {
		logger.Println("DashScope STT: no API key configured. Set the DASHSCOPE_API_KEY env var or the STT dashscope_key field in apiConfig.json (Speech settings in the web UI).")
	}
	return nil
}

// pcm2wav wraps 16 kHz, 16-bit, mono PCM in a WAV container.
func pcm2wav(in io.Reader) []byte {
	out := &writerseeker.WriterSeeker{}
	e := wav.NewEncoder(out, 16000, 16, 1, 1)
	audioBuf, err := newAudioIntBuffer(in)
	if err != nil {
		logger.Println(err)
	}
	if err := e.Write(audioBuf); err != nil {
		logger.Println(err)
	}
	if err := e.Close(); err != nil {
		logger.Println(err)
	}
	outBuf := new(bytes.Buffer)
	io.Copy(outBuf, out.BytesReader())
	return outBuf.Bytes()
}

func newAudioIntBuffer(r io.Reader) (*audio.IntBuffer, error) {
	buf := audio.IntBuffer{
		Format: &audio.Format{
			NumChannels: 1,
			SampleRate:  16000,
		},
	}
	for {
		var sample int16
		err := binary.Read(r, binary.LittleEndian, &sample)
		if err == io.EOF {
			return &buf, nil
		}
		if err != nil {
			return nil, err
		}
		buf.Data = append(buf.Data, int(sample))
	}
}

// buildVocabPrompt builds a vocabulary hint from the loaded intents. It is
// passed as the system message (context), biasing recognition towards
// expected command wording.
func buildVocabPrompt() string {
	var sb strings.Builder
	for _, in := range vars.IntentList {
		for _, kp := range in.Keyphrases {
			k := strings.TrimSpace(kp)
			if len(k) < 4 {
				continue
			}
			if sb.Len()+len(k)+2 > 850 {
				return sb.String()
			}
			sb.WriteString(k)
			sb.WriteString(". ")
			break
		}
	}
	return sb.String()
}

// dashScopeLanguage maps a BCP-47 language tag (vosk style, e.g. zh-CN) to
// the short codes the ASR API accepts; returns "" when not mappable.
func dashScopeLanguage(language string) string {
	lang := strings.ToLower(language)
	if i := strings.Index(lang, "-"); i > 0 {
		// zh-HK -> yue (Cantonese), otherwise use the primary subtag
		if lang == "zh-hk" || lang == "zh-tw" || lang == "zh-mo" {
			return "yue"
		}
		lang = lang[:i]
	}
	if dashScopeLanguages[lang] {
		return lang
	}
	return ""
}

func makeDashScopeReq(wavAudio []byte) string {
	endpoint := strings.TrimSpace(vars.APIConfig.STT.DashScopeEndpoint)
	if endpoint == "" {
		endpoint = defaultDashScopeEndpoint
	}
	key := strings.TrimSpace(vars.APIConfig.STT.DashScopeKey)
	if key == "" {
		key = os.Getenv("DASHSCOPE_API_KEY")
	}
	if key == "" {
		logger.Println("(DashScope STT) no API key configured - set STT dashscope_key in the web UI (or DASHSCOPE_API_KEY env)")
		return ""
	}
	model := strings.TrimSpace(vars.APIConfig.STT.DashScopeModel)
	if model == "" {
		model = "qwen3-asr-flash"
	}
	logger.Println("(DashScope STT) transcribing with model " + model)

	req := dashScopeReq{
		Model: model,
		Input: dashScopeReqInput{
			Messages: []dashScopeMessage{},
		},
	}
	if vp := buildVocabPrompt(); vp != "" {
		req.Input.Messages = append(req.Input.Messages, dashScopeMessage{
			Role:    "system",
			Content: []dashScopeContentPart{{Text: vp}},
		})
	}
	req.Input.Messages = append(req.Input.Messages, dashScopeMessage{
		Role: "user",
		Content: []dashScopeContentPart{
			{Audio: "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wavAudio)},
		},
	})
	if lang := dashScopeLanguage(vars.APIConfig.STT.Language); lang != "" {
		req.Parameters = &dashScopeParameters{
			ASROptions: &dashScopeASROptions{
				Language:  lang,
				EnableITN: false,
			},
		}
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		logger.Println("(DashScope STT) could not marshal request: " + err.Error())
		return ""
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		logger.Println("(DashScope STT) could not build request: " + err.Error())
		return ""
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		logger.Println("(DashScope STT) request failed: " + err.Error())
		return ""
	}
	defer resp.Body.Close()

	response, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Println("(DashScope STT) could not read response: " + err.Error())
		return ""
	}
	if resp.StatusCode != http.StatusOK {
		logger.Println(fmt.Sprintf("(DashScope STT) HTTP %d from %s: %s", resp.StatusCode, endpoint, string(response)))
		return ""
	}

	var asrResp dashScopeResp
	if err := json.Unmarshal(response, &asrResp); err != nil {
		logger.Println("(DashScope STT) could not parse response: " + err.Error() + " - body: " + string(response))
		return ""
	}
	var text string
	for _, choice := range asrResp.Output.Choices {
		for _, part := range choice.Message.Content {
			text += part.Text
		}
	}
	if strings.TrimSpace(text) == "" {
		logger.Println("(DashScope STT) transcription is empty - audio was silent/unclear, or the STT language does not match the spoken language (API message: " + asrResp.Message + " " + asrResp.Code + ")")
	}
	return text
}

func STT(req sr.SpeechRequest) (string, error) {
	logger.Println("(Bot " + req.Device + ", DashScope) Processing...")
	speechIsDone := false
	var err error
	for {
		_, err = req.GetNextStreamChunk()
		if err != nil {
			return "", err
		}
		// has to be split into 320 []byte chunks for VAD
		speechIsDone, _ = req.DetectEndOfSpeech()
		if speechIsDone {
			break
		}
	}

	pcmBufTo := &writerseeker.WriterSeeker{}
	pcmBufTo.Write(req.DecodedMicData)
	pcmBuf := pcm2wav(pcmBufTo.BytesReader())

	transcribedText := strings.ToLower(makeDashScopeReq(pcmBuf))
	logger.Println("Bot " + req.Device + " Transcribed text: " + transcribedText)
	return transcribedText, nil
}

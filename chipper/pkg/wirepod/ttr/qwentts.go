package wirepod_ttr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

// Qwen3-TTS (DashScope) speech synthesis.
//
// The robot's built-in voice engine (SayText with UseVectorVoice) can only
// speak English. This file adds an API-based speech provider (Qwen3-TTS) so
// the robot can speak Chinese: text is sent to DashScope, the returned WAV is
// converted to 16 kHz PCM and streamed to the robot's speaker through the
// same ExternalAudioStreamPlayback path used by DoSayText_OpenAI.

const (
	qwenTTSDefaultModel  = "qwen3-tts-flash"
	qwenTTSDefaultVoice  = "Momo"
	qwenTTSSampleRateOut = 16000
	// synthesis volume sent with every request (parameters.volume,
	// 0-100). This is a per-call setting: it takes effect immediately and
	// never touches the cloud voice resource itself.
	qwenTTSDefaultVolume = 100
	// used when the model is qwen3-tts-instruct-flash and the user did not
	// configure custom instructions: nudge the voice toward Vector's style
	// (cute little robot, slightly fast and high-pitched)
	qwenTTSDefaultInstructions = "像一只可爱的小机器人一样说话：语速稍快，音调偏高，声音清脆，充满活力"
)

type qwenTTSInput struct {
	Text         string `json:"text"`
	Voice        string `json:"voice"`
	LanguageType string `json:"language_type,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

type qwenTTSReq struct {
	Model string       `json:"model"`
	Input qwenTTSInput `json:"input"`
}

type qwenTTSResp struct {
	StatusCode int    `json:"status_code"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Output     struct {
		Audio struct {
			URL  string `json:"url"`
			Data string `json:"data"`
		} `json:"audio"`
	} `json:"output"`
}

// CosyVoice uses a different endpoint and request shape than the Qwen-TTS
// family (no language_type/instructions; format/sample_rate/volume live in
// the "parameters" object per the DashScope SpeechSynthesizer API).
type cosyVoiceInput struct {
	Text  string `json:"text"`
	Voice string `json:"voice"`
}

type cosyVoiceParams struct {
	TextType   string `json:"text_type"`
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
	Volume     int    `json:"volume"`
}

type cosyVoiceReq struct {
	Model      string          `json:"model"`
	Input      cosyVoiceInput  `json:"input"`
	Parameters cosyVoiceParams `json:"parameters"`
}

// QwenTTSActive returns true if a DashScope API key is configured for Qwen TTS.
func QwenTTSActive() bool {
	return vars.APIConfig.TTS.Service == "qwen" && strings.TrimSpace(vars.APIConfig.TTS.Key) != ""
}

// ttsVolume normalizes the configured synthesis volume: 0/unset means the
// default (max loudness), anything above 100 is capped.
func ttsVolume(v int) int {
	if v <= 0 {
		return qwenTTSDefaultVolume
	}
	if v > 100 {
		return 100
	}
	return v
}

// QwenTTSLanguageInstruction returns the system-prompt snippet describing
// which language the LLM should respond in. When Qwen TTS is active the robot
// can actually speak Chinese, so the LLM may answer in the user's language;
// otherwise the built-in English-only voice constrains it.
func QwenTTSLanguageInstruction() string {
	if QwenTTSActive() {
		return "The user may speak in any language. Always try to understand the user's input no matter which language it is in, and respond in the same language the user used (for example, respond in Chinese if the user speaks Chinese). The robot's speech engine can speak English and Chinese and must be able to follow your response with animations and actions."
	}
	return "The user may speak in any language. Always try to understand the user's input no matter which language it is in, but ALWAYS respond in English only, because the robot's speech engine can only speak English and must be able to follow your response with animations and actions. Never reply in any other language."
}

// containsCJK returns true if the string contains any CJK characters
// (Chinese hanzi, kana, hangul, CJK punctuation or fullwidth forms).
func containsCJK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF: // CJK unified ideographs
			return true
		case r >= 0x3400 && r <= 0x4DBF: // CJK extension A
			return true
		case r >= 0x3040 && r <= 0x30FF: // hiragana / katakana
			return true
		case r >= 0xAC00 && r <= 0xD7AF: // hangul syllables
			return true
		case r >= 0x3000 && r <= 0x303F: // CJK punctuation
			return true
		case r >= 0xFF00 && r <= 0xFFEF: // fullwidth forms
			return true
		}
	}
	return false
}

func qwenBaseURL(region string) string {
	if region == "intl" {
		return "https://dashscope-intl.aliyuncs.com/api/v1"
	}
	return "https://dashscope.aliyuncs.com/api/v1"
}

// dashScopeRequest posts a synthesis request and returns the WAV audio bytes.
// The Qwen-TTS family returns an URL to a WAV file; CosyVoice returns the WAV
// inline as base64 - both shapes are handled here. The request is aborted if
// ctx is cancelled (barge-in).
func dashScopeRequest(ctx context.Context, url, key string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: time.Second * 60}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dashscope returned %d: %s", resp.StatusCode, string(respBytes))
	}
	var qresp qwenTTSResp
	if err := json.Unmarshal(respBytes, &qresp); err != nil {
		return nil, err
	}
	if qresp.Output.Audio.URL == "" && qresp.Output.Audio.Data == "" {
		return nil, fmt.Errorf("dashscope returned no audio (code: %s, message: %s)", qresp.Code, qresp.Message)
	}
	if qresp.Output.Audio.URL != "" {
		audioReq, err := http.NewRequestWithContext(ctx, http.MethodGet, qresp.Output.Audio.URL, nil)
		if err != nil {
			return nil, err
		}
		audioResp, err := client.Do(audioReq)
		if err != nil {
			return nil, err
		}
		defer audioResp.Body.Close()
		return io.ReadAll(audioResp.Body)
	}
	if decoded, err := base64.StdEncoding.DecodeString(qresp.Output.Audio.Data); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(qresp.Output.Audio.Data)
}

// DoSayText_Qwen synthesizes text with Qwen3-TTS and plays it on the robot.
// If ctx is cancelled mid-playback (barge-in), the audio stops immediately.
func DoSayText_Qwen(robot *vector.Vector, input string, ctx context.Context) error {
	input = strings.TrimSpace(input)
	if input == "" || ctx.Err() != nil {
		return nil
	}
	tts := vars.APIConfig.TTS
	model := strings.TrimSpace(tts.Model)
	if model == "" {
		model = qwenTTSDefaultModel
	}
	voice := strings.TrimSpace(tts.Voice)
	if voice == "" {
		voice = qwenTTSDefaultVoice
	}

	// specifying the language improves quality for single-language text
	languageType := "English"
	if containsCJK(input) {
		languageType = "Chinese"
	}

	instructions := strings.TrimSpace(tts.Instructions)
	if strings.Contains(model, "instruct") && instructions == "" {
		instructions = qwenTTSDefaultInstructions
	}

	// per-request volume (0-100): 0/unset means max. Applies to this
	// synthesis call only - the cloned voice on the cloud side stays
	// untouched.
	volume := ttsVolume(tts.Volume)

	reqBodyLog := "(Qwen TTS) requesting speech, model: " + model + ", voice: " + voice
	var audioBytes []byte
	var err error
	if strings.HasPrefix(model, "cosyvoice") {
		// CosyVoice family: SpeechSynthesizer endpoint, different request shape
		reqBodyLog += fmt.Sprintf(" (CosyVoice endpoint, volume %d)", volume)
		bodyBytes, marshalErr := json.Marshal(cosyVoiceReq{
			Model: model,
			Input: cosyVoiceInput{
				Text:  input,
				Voice: voice,
			},
			Parameters: cosyVoiceParams{
				TextType:   "PlainText",
				Format:     "wav",
				SampleRate: 24000,
				Volume:     volume,
			},
		})
		if marshalErr != nil {
			return marshalErr
		}
		audioBytes, err = dashScopeRequest(
			ctx,
			qwenBaseURL(tts.Region)+"/services/audio/tts/SpeechSynthesizer",
			tts.Key, bodyBytes)
	} else {
		qwenInput := qwenTTSInput{
			Text:         input,
			Voice:        voice,
			Instructions: instructions,
		}
		// cloned-voice (VC) models do not take a language hint
		if !strings.Contains(model, "-vc") {
			qwenInput.LanguageType = languageType
		}
		bodyBytes, marshalErr := json.Marshal(qwenTTSReq{
			Model: model,
			Input: qwenInput,
		})
		if marshalErr != nil {
			return marshalErr
		}
		audioBytes, err = dashScopeRequest(
			ctx,
			qwenBaseURL(tts.Region)+"/services/aigc/multimodal-generation/generation",
			tts.Key, bodyBytes)
	}
	if err != nil {
		return err
	}
	logger.Println(reqBodyLog)

	pcm, sampleRate, err := wavToPCM(audioBytes)
	if err != nil {
		return err
	}

	var audioChunks [][]byte
	switch sampleRate {
	case 24000:
		// the standard path: 24 kHz -> 16 kHz, low-pass, boost, chunk
		audioChunks = downsample24kTo16k(pcm)
	default:
		pcm16k := resamplePCMTo16k(pcm, sampleRate)
		audioChunks = chunkPCM16k(pcm16k)
	}
	if len(audioChunks) == 0 {
		return errors.New("qwen tts produced no audio")
	}

	// play on the robot, interruptible via ctx (barge-in cuts the audio)
	return playExternalAudioStream(ctx, robot, audioChunks)
}

// wavToPCM parses a RIFF/WAVE file and returns the PCM sample data of the
// first data chunk along with the sample rate. Only 16-bit PCM is supported;
// stereo audio is mixed down to mono.
func wavToPCM(data []byte) ([]byte, int, error) {
	if len(data) < 12 {
		return nil, 0, errors.New("wav: file too small")
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, errors.New("wav: not a RIFF/WAVE file")
	}
	var sampleRate int
	var channels int
	var pcm []byte
	haveFmt := false
	offset := 12
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		bodyStart := offset + 8
		bodyEnd := bodyStart + chunkSize
		if bodyEnd > len(data) {
			bodyEnd = len(data)
		}
		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return nil, 0, errors.New("wav: fmt chunk too small")
			}
			audioFormat := binary.LittleEndian.Uint16(data[bodyStart : bodyStart+2])
			channels = int(binary.LittleEndian.Uint16(data[bodyStart+2 : bodyStart+4]))
			sampleRate = int(binary.LittleEndian.Uint32(data[bodyStart+4 : bodyStart+8]))
			bitsPerSample := binary.LittleEndian.Uint16(data[bodyStart+14 : bodyStart+16])
			if audioFormat != 1 {
				return nil, 0, fmt.Errorf("wav: unsupported audio format %d (only PCM is supported)", audioFormat)
			}
			if bitsPerSample != 16 {
				return nil, 0, fmt.Errorf("wav: unsupported bit depth %d (only 16-bit is supported)", bitsPerSample)
			}
			if channels != 1 && channels != 2 {
				return nil, 0, fmt.Errorf("wav: unsupported channel count %d", channels)
			}
			haveFmt = true
		case "data":
			pcm = data[bodyStart:bodyEnd]
		}
		offset = bodyStart + chunkSize + (chunkSize & 1) // chunks are word-aligned
		if haveFmt && pcm != nil {
			break
		}
	}
	if !haveFmt || pcm == nil {
		return nil, 0, errors.New("wav: missing fmt or data chunk")
	}
	if sampleRate <= 0 {
		return nil, 0, errors.New("wav: invalid sample rate")
	}
	if channels == 2 {
		pcm = stereoToMono(pcm)
	}
	return pcm, sampleRate, nil
}

// stereoToMono averages L/R sample pairs of 16-bit PCM into a mono stream.
func stereoToMono(data []byte) []byte {
	samples := bytesToInt16s(data)
	if len(samples)%2 != 0 {
		samples = samples[:len(samples)-1]
	}
	mono := make([]int16, len(samples)/2)
	for i := range mono {
		mono[i] = int16((int32(samples[i*2]) + int32(samples[i*2+1])) / 2)
	}
	return int16sToBytes(mono)
}

// resamplePCMTo16k linearly resamples 16-bit mono PCM to 16 kHz.
func resamplePCMTo16k(data []byte, sampleRateIn int) []byte {
	if sampleRateIn == qwenTTSSampleRateOut || len(data) < 2 {
		return data
	}
	samples := bytesToInt16s(data)
	ratio := float64(sampleRateIn) / float64(qwenTTSSampleRateOut)
	outLen := int(float64(len(samples)) / ratio)
	if outLen < 1 {
		outLen = 1
	}
	output := make([]int16, outLen)
	for i := 0; i < outLen; i++ {
		pos := float64(i) * ratio
		idx := int(pos)
		frac := pos - float64(idx)
		s0 := samples[idx]
		s1 := s0
		if idx+1 < len(samples) {
			s1 = samples[idx+1]
		}
		output[i] = int16(float64(s0)*(1-frac) + float64(s1)*frac)
	}
	return int16sToBytes(output)
}

// chunkPCM16k applies the same low-pass/volume treatment as the 24 kHz path
// and splits 16 kHz PCM into 1024-byte chunks for the robot.
func chunkPCM16k(data []byte) [][]byte {
	var audioChunks [][]byte
	filteredBytes := lowPassFilter(data, 4000, qwenTTSSampleRateOut)
	iVolBytes := increaseVolume(filteredBytes, 5)
	for len(iVolBytes) > 0 {
		if len(iVolBytes) < 1024 {
			chunk := make([]byte, 1024)
			copy(chunk, iVolBytes)
			audioChunks = append(audioChunks, chunk)
			break
		}
		audioChunks = append(audioChunks, iVolBytes[:1024])
		iVolBytes = iVolBytes[1024:]
	}
	return audioChunks
}

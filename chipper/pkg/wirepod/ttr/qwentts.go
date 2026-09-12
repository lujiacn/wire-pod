package wirepod_ttr

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
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
			URL string `json:"url"`
		} `json:"audio"`
	} `json:"output"`
}

// QwenTTSActive returns true if a DashScope API key is configured for Qwen TTS.
func QwenTTSActive() bool {
	return vars.APIConfig.TTS.Service == "qwen" && strings.TrimSpace(vars.APIConfig.TTS.Key) != ""
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

// DoSayText_Qwen synthesizes text with Qwen3-TTS and plays it on the robot.
func DoSayText_Qwen(robot *vector.Vector, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
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

	reqBody := qwenTTSReq{
		Model: model,
		Input: qwenTTSInput{
			Text:         input,
			Voice:        voice,
			LanguageType: languageType,
			Instructions: instructions,
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	url := qwenBaseURL(tts.Region) + "/services/aigc/multimodal-generation/generation"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(tts.Key))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: time.Second * 60}
	logger.Println("(Qwen TTS) requesting speech, model: " + model + ", voice: " + voice + ", language: " + languageType)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dashscope returned %d: %s", resp.StatusCode, string(respBytes))
	}
	var qresp qwenTTSResp
	if err := json.Unmarshal(respBytes, &qresp); err != nil {
		return err
	}
	if qresp.Output.Audio.URL == "" {
		return fmt.Errorf("dashscope returned no audio url (code: %s, message: %s)", qresp.Code, qresp.Message)
	}

	// the audio is returned as a URL to a WAV file
	audioReq, err := http.NewRequest(http.MethodGet, qresp.Output.Audio.URL, nil)
	if err != nil {
		return err
	}
	audioResp, err := client.Do(audioReq)
	if err != nil {
		return err
	}
	defer audioResp.Body.Close()
	audioBytes, err := io.ReadAll(audioResp.Body)
	if err != nil {
		return err
	}

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

	vclient, err := robot.Conn.ExternalAudioStreamPlayback(context.Background())
	if err != nil {
		return err
	}
	vclient.Send(&vectorpb.ExternalAudioStreamRequest{
		AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamPrepare{
			AudioStreamPrepare: &vectorpb.ExternalAudioStreamPrepare{
				AudioFrameRate: qwenTTSSampleRateOut,
				AudioVolume:    100,
			},
		},
	})

	var chunksToDetermineLength []byte
	for _, chunk := range audioChunks {
		chunksToDetermineLength = append(chunksToDetermineLength, chunk...)
	}
	go func() {
		for _, chunk := range audioChunks {
			vclient.Send(&vectorpb.ExternalAudioStreamRequest{
				AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamChunk{
					AudioStreamChunk: &vectorpb.ExternalAudioStreamChunk{
						AudioChunkSizeBytes: 1024,
						AudioChunkSamples:   chunk,
					},
				},
			})
			time.Sleep(time.Millisecond * 25)
		}
		vclient.Send(&vectorpb.ExternalAudioStreamRequest{
			AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamComplete{
				AudioStreamComplete: &vectorpb.ExternalAudioStreamComplete{},
			},
		})
	}()
	time.Sleep(pcmLength(chunksToDetermineLength) + (time.Millisecond * 50))
	return nil
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

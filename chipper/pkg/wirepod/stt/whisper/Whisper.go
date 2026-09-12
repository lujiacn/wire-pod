package wirepod_whisper

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	"github.com/orcaman/writerseeker"
)

var Name string = "whisper"

type openAiResp struct {
	Text string `json:"text"`
}

func Init() error {
	if os.Getenv("OPENAI_KEY") == "" && strings.TrimSpace(vars.APIConfig.STT.WhisperKey) == "" {
		logger.Println("Whisper API STT: no API key configured. Set the OPENAI_KEY env var or the STT whisper_key field in apiConfig.json.")
	}
	return nil
}

func pcm2wav(in io.Reader) []byte {

	// Output file.
	out := &writerseeker.WriterSeeker{}

	// 8 kHz, 16 bit, 1 channel, WAV.
	e := wav.NewEncoder(out, 16000, 16, 1, 1)

	// Create new audio.IntBuffer.
	audioBuf, err := newAudioIntBuffer(in)
	if err != nil {
		logger.Println(err)
	}
	// Write buffer to output file. This writes a RIFF header and the PCM chunks from the audio.IntBuffer.
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
		switch {
		case err == io.EOF:
			return &buf, nil
		case err != nil:
			return nil, err
		}
		buf.Data = append(buf.Data, int(sample))
	}
}

// buildVocabPrompt builds a vocabulary hint from the loaded intents. whisper-1
// accepts a prompt to bias transcription towards expected wording, which helps a
// lot with command phrases in languages other than English.
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

func makeOpenAIReq(in []byte) string {
	// the transcription endpoint, key and model are configurable, so any
	// OpenAI-compatible transcription API works (OpenAI, Groq, a proxy...)
	endpoint := strings.TrimSpace(vars.APIConfig.STT.WhisperEndpoint)
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/audio/transcriptions"
	}
	key := strings.TrimSpace(vars.APIConfig.STT.WhisperKey)
	if key == "" {
		key = os.Getenv("OPENAI_KEY")
	}
	model := strings.TrimSpace(vars.APIConfig.STT.WhisperModel)
	if model == "" {
		model = "whisper-1"
	}
	logger.Println("(Whisper API) transcribing with model " + model + " at " + endpoint)

	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	w.WriteField("model", model)
	// Without an explicit language whisper-1 guesses per utterance and gets short
	// commands wrong, sometimes returning English for German speech.
	if lang := strings.Split(vars.APIConfig.STT.Language, "-")[0]; lang != "" {
		w.WriteField("language", lang)
	}
	if vp := buildVocabPrompt(); vp != "" {
		w.WriteField("prompt", vp)
	}
	sendFile, _ := w.CreateFormFile("file", "audio.mp3")
	sendFile.Write(in)
	w.Close()

	httpReq, _ := http.NewRequest("POST", endpoint, buf)
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	httpReq.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		logger.Println(err)
		return "There was an error."
	}

	defer resp.Body.Close()

	response, _ := io.ReadAll(resp.Body)

	var aiResponse openAiResp
	json.Unmarshal(response, &aiResponse)

	return aiResponse.Text
}

func STT(req sr.SpeechRequest) (string, error) {
	logger.Println("(Bot " + req.Device + ", Whisper) Processing...")
	speechIsDone := false
	var err error
	for {
		_, err = req.GetNextStreamChunk()
		if err != nil {
			return "", err
		}
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

	transcribedText := strings.ToLower(makeOpenAIReq(pcmBuf))
	logger.Println("Bot " + req.Device + " Transcribed text: " + transcribedText)
	return transcribedText, nil
}

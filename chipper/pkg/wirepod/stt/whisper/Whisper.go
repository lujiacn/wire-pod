package wirepod_whisper

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	"github.com/orcaman/writerseeker"
	wav2 "github.com/youpy/go-wav"
)

var Name string = "whisper"

type openAiResp struct {
	Text string `json:"text"`
}

func Init() error {
	if os.Getenv("OPENAI_KEY") == "" {
		logger.Println("This is an early implementation of the Whisper API which has not been implemented into the web interface. You must set the OPENAI_KEY env var.")
		//os.Exit(1)
	}
	return nil
}

func pcm2wav_old(in io.Reader) []byte {

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

func makeOpenAIReq(fileName string) string {
	// check vars openai config and use the config
	// logger.Println("check quest len", len(in))
	//
	// file, err := os.Create("/tmp/test.mp3")
	// if err != nil {
	//     // Handle error
	// }
	// _, err = file.Write(in)
	//
	// if err != nil {
	//     // Handle error
	//     logger.Println("wirte file err", err)
	// }
	// file.Close()

	url := "https://api.openai.com/v1/audio/transcriptions"
	if baseURL := strings.TrimSpace(vars.APIConfig.Knowledge.OpenAIBase); baseURL != "" {
		url = baseURL + "/audio/transcriptions"
	}

	key := strings.TrimSpace(vars.APIConfig.Knowledge.Key)
	logger.Println("Connect to openai whisper with base", url)

	// Open the WAV file
	file, err := os.Open(fileName)
	if err != nil {
		logger.Println("error opening file:", err)
		return "There was an error opening the file."
	}
	defer file.Close()

	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	w.WriteField("model", "whisper-1")
	sendFile, _ := w.CreateFormFile("file", filepath.Base(fileName))
	_, err = io.Copy(sendFile, file)
	if err != nil {
		logger.Println("error copying file content:", err)
		return "There was an error copying the file content."
	}
	w.Close()

	logger.Println("after writing file to the request body")

	httpReq, _ := http.NewRequest("POST", url, buf)
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	httpReq.Header.Set("Authorization", "Bearer "+key)

	logger.Println("debug api key", key)

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		logger.Println(err)
		return "There was an error sending the request."
	}

	defer resp.Body.Close()

	response, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Println("readall err", err)
		return "There was an error reading the response."
	}

	var aiResponse openAiResp
	err = json.Unmarshal(response, &aiResponse)
	if err != nil {
		logger.Println("error unmarshaling response:", err)
		return "There was an error parsing the response."
	}

	return aiResponse.Text
}

func STT(req sr.SpeechRequest) (string, error) {
	logger.Println("(Bot " + req.Device + ", Whisper) Processing...")
	// speechIsDone := false
	var err error
	out := req.FirstReq
	for {
		chunk, err := req.GetNextStreamChunk()
		if err != nil {
			return "", err
		}
		speechIsDone, doProcess := req.DetectEndOfSpeech()
		if doProcess {
			out = append(out, chunk...)
			// rec.AcceptWaveform(chunk)
		}
		if speechIsDone {
			break
		}
	}

	// write re.DecodedMicData to local file

	file, err := os.Create("/tmp/test.wav")
	if err != nil {
		// Handle error
	}
	_, err = file.Write(out)
	if err != nil {
		// Handle error
	}
	file.Close()
	logger.Println("wav file saved")

	savePCMToWAV("audio.wav", out)

	// pcmBufTo := &writerseeker.WriterSeeker{}
	// pcmBufTo.Write(req.DecodedMicData)
	// pcmBuf := pcm2wav(pcmBufTo.BytesReader())
	// pcmBuf := pcm2wav(bytes.NewReader(out))

	transcribedText := strings.ToLower(makeOpenAIReq("audio.wav"))
	logger.Println("Bot " + req.Device + " Transcribed text: " + transcribedText)
	return transcribedText, nil
}

func savePCMToWAV(filename string, pcmData []byte) error {
	// Open the output file
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	// WAV parameters
	sampleRate := uint32(16000) // 8 kHz
	bitsPerSample := uint16(16) // 16-bit
	numChannels := uint16(1)    // Mono

	// Calculate the number of samples
	numSamples := uint32(len(pcmData) / int(numChannels) / (int(bitsPerSample) / 8))

	// Create a new WAV writer
	wavWriter := wav2.NewWriter(file, numSamples, numChannels, sampleRate, bitsPerSample)

	// Write the PCM data
	_, err = wavWriter.Write(pcmData)
	if err != nil {
		return err
	}

	return nil
}

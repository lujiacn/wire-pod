package wirepod_ttr

import (
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

// The robot's master volume (set by the volume voice intents, the web UI or
// the LLM volume command) only scales the robot's own sounds and built-in
// voice. Audio streamed via ExternalAudioStreamPlayback - which is how the
// API TTS providers (Qwen, OpenAI) play the LLM's answers - bypasses it
// entirely, so "set volume to low" never made the LLM any quieter.
//
// To fix that, the synthesized PCM is scaled digitally on the wire-pod side
// with the robot's current master volume before it is streamed.

// volumeGainForRobot returns the amplitude scale factor matching the robot's
// current master volume setting (VOLUME_1..VOLUME_5), read from the cached
// vic.RobotSettings jdoc. The robot writes that jdoc to wire-pod whenever the
// volume changes, so the value is current. Defaults to full volume when the
// setting is unknown.
func volumeGainForRobot(robot *vector.Vector) float64 {
	esn := robot.Cfg.SerialNo
	if esn == "" {
		return 1.0
	}
	jdoc, exists := vars.GetJdoc("vic:"+esn, "vic.RobotSettings")
	if !exists {
		return 1.0
	}
	var settings struct {
		MasterVolume int `json:"master_volume"`
	}
	if err := json.Unmarshal([]byte(jdoc.JsonDoc), &settings); err != nil {
		return 1.0
	}
	if settings.MasterVolume < 1 || settings.MasterVolume > 5 {
		return 1.0
	}
	// VOLUME_1 is the quietest the robot offers (there is no VOLUME_0), map
	// the five levels to linear 20%..100% amplitude steps
	return float64(settings.MasterVolume) / 5.0
}

// scalePCM applies a digital gain to 16-bit little-endian PCM chunks in
// place, with clipping protection. A no-op at (near) unity gain.
func scalePCM(chunks [][]byte, gain float64) {
	if gain >= 0.999 {
		return
	}
	for _, chunk := range chunks {
		for i := 0; i+1 < len(chunk); i += 2 {
			sample := int16(binary.LittleEndian.Uint16(chunk[i:]))
			scaled := int(float64(sample) * gain)
			if scaled > 32767 {
				scaled = 32767
			} else if scaled < -32768 {
				scaled = -32768
			}
			binary.LittleEndian.PutUint16(chunk[i:], uint16(int16(scaled)))
		}
	}
}

// applyMasterVolumeGain scales the PCM chunks to the robot's current master
// volume so streamed TTS follows the volume voice commands.
func applyMasterVolumeGain(robot *vector.Vector, audioChunks [][]byte) {
	gain := volumeGainForRobot(robot)
	if gain < 0.999 {
		logger.Println("(audio) applying master volume gain " + fmt.Sprint(int(gain*100+0.5)) + "% to streamed TTS for bot " + robot.Cfg.SerialNo)
		scalePCM(audioChunks, gain)
	}
}

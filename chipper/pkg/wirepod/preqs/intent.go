package processreqs

import (
	"strings"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/vtt"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	ttr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/ttr"
)

// This is here for compatibility with 1.6 and older software
func (s *Server) ProcessIntent(req *vtt.IntentRequest) (*vtt.IntentResponse, error) {
	var successMatched bool
	speechReq := sr.ReqToSpeechRequest(req)
	var transcribedText string
	// if the request may end up at the LLM (always-LLM or IntentGraph
	// fallback), capture a silent environment photo in parallel with
	// transcription, so the LLM can answer based on what the robot sees
	llmPlausible := vars.APIConfig.Knowledge.Enable && vars.APIConfig.Knowledge.Provider != "houndify" &&
		(vars.APIConfig.Knowledge.AlwaysLLM || vars.APIConfig.Knowledge.IntentGraph)
	photoCh := startEnvPhotoCapture(req.Device, llmPlausible)
	if !isSti {
		var err error
		transcribedText, err = sttHandler(speechReq)
		if err != nil {
			ttr.IntentPass(req, "intent_system_noaudio", "voice processing error: "+err.Error(), map[string]string{"error": err.Error()}, true)
			return nil, nil
		}
		if strings.TrimSpace(transcribedText) == "" {
			ttr.IntentPass(req, "intent_system_noaudio", "", map[string]string{}, false)
			return nil, nil
		}
		if vars.APIConfig.Knowledge.Enable && vars.APIConfig.Knowledge.AlwaysLLM && vars.APIConfig.Knowledge.Provider != "houndify" {
			// always-LLM mode: send every request straight to the LLM, which
			// is much more accurate than the internal intent matching. if the
			// LLM is not accessible, fall back to the internal intents
			logger.Println("Making LLM request for device " + req.Device + " (always-LLM mode)...")
			_, llmErr := ttr.StreamingKGSim(req, req.Device, transcribedText, false, waitEnvPhoto(photoCh))
			if llmErr != nil {
				logger.Println("LLM error: " + llmErr.Error())
				logger.LogUI("LLM error: " + llmErr.Error())
				logger.Println("Falling back to internal intent processing...")
				if !ttr.ProcessTextAll(req, transcribedText, vars.IntentList, speechReq.IsOpus) {
					logger.Println("No intent was matched.")
					ttr.IntentPass(req, "intent_system_unmatched", transcribedText, map[string]string{"": ""}, false)
					ttr.KGSim(req.Device, "There was an error getting a response from the L L M. Check the logs in the web interface.")
				}
			}
			logger.Println("Bot " + speechReq.Device + " request served.")
			return nil, nil
		}
		successMatched = ttr.ProcessTextAll(req, transcribedText, vars.IntentList, speechReq.IsOpus)
	} else {
		intent, slots, err := stiHandler(speechReq)
		if err != nil {
			if err.Error() == "inference not understood" {
				logger.Println("No intent was matched")
				ttr.IntentPass(req, "intent_system_unmatched", "voice processing error", map[string]string{"error": err.Error()}, true)
				return nil, nil
			}
			logger.Println(err)
			ttr.IntentPass(req, "intent_system_noaudio", "voice processing error", map[string]string{"error": err.Error()}, true)
			return nil, nil
		}
		ttr.ParamCheckerSlotsEnUS(req, intent, slots, speechReq.IsOpus, speechReq.Device)
		return nil, nil
	}
	if !successMatched {
		if vars.APIConfig.Knowledge.IntentGraph && vars.APIConfig.Knowledge.Enable {
			logger.Println("Making LLM request for device " + req.Device + "...")
			_, err := ttr.StreamingKGSim(req, req.Device, transcribedText, false, waitEnvPhoto(photoCh))
			if err != nil {
				logger.Println("LLM error: " + err.Error())
				logger.LogUI("LLM error: " + err.Error())
				ttr.IntentPass(req, "intent_system_unmatched", transcribedText, map[string]string{"": ""}, false)
				ttr.KGSim(req.Device, "There was an error getting a response from the L L M. Check the logs in the web interface.")
			}
			logger.Println("Bot " + speechReq.Device + " request served.")
			return nil, nil
		}
		logger.Println("No intent was matched.")
		ttr.IntentPass(req, "intent_system_unmatched", transcribedText, map[string]string{"": ""}, false)
		return nil, nil
	}
	logger.Println("Bot " + speechReq.Device + " request served.")
	return nil, nil
}

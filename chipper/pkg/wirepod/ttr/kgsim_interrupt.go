package wirepod_ttr

import (
	"context"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
)

// Barge-in support for streaming LLM responses.
//
// An in-flight response registers a context cancellation handle here. The
// response is interrupted by cancelling that context, which:
//   - cuts the audio currently playing on the robot (the external audio
//     stream client is closed and chunk senders abort),
//   - stops the LLM stream reader (the HTTP stream is closed, unblocking
//     Recv) so no further sentences are queued,
//   - makes the sentence loop drop all remaining chunks and return quickly,
//   - releases behavior control so the robot is free again.
//
// Interrupt sources:
//   - a new voice request from the same robot (user spoke again -> CancelActiveResponse)
//   - the wake word event (user says "Hey Vector" while the robot is talking)
//   - the touch sensor (button press)

type activeResponse struct {
	cancel context.CancelFunc
}

var (
	activeResponsesMu sync.Mutex
	activeResponses   = map[string]*activeResponse{}
)

// CancelActiveResponse interrupts any in-flight LLM response for the given
// robot, if one exists. Called as soon as a new voice request arrives from
// that robot, so the robot stops speaking what it was saying and reacts to
// the new user input instead.
func CancelActiveResponse(esn string) {
	activeResponsesMu.Lock()
	resp := activeResponses[esn]
	activeResponsesMu.Unlock()
	if resp != nil {
		logger.Println("(barge-in) interrupting in-flight LLM response for " + esn + " (new user input)")
		resp.cancel()
	}
}

// RegisterActiveResponse registers a cancellation handle for the response
// being started on the given robot. If a response is still in flight for
// that robot (e.g. the user started talking over it), it is cancelled and
// this function waits briefly for it to unwind, so the two responses never
// fight over behavior control. Returns the handle to pass to
// UnregisterActiveResponse when this response ends.
func RegisterActiveResponse(esn string, cancel context.CancelFunc) *activeResponse {
	for i := 0; i < 50; i++ {
		activeResponsesMu.Lock()
		old := activeResponses[esn]
		if old == nil {
			resp := &activeResponse{cancel: cancel}
			activeResponses[esn] = resp
			activeResponsesMu.Unlock()
			return resp
		}
		activeResponsesMu.Unlock()
		// a response is still winding down for this robot - make sure it is
		// cancelled and give it a moment to release behavior control
		old.cancel()
		time.Sleep(time.Millisecond * 100)
	}
	logger.Println("(barge-in) previous response for " + esn + " did not unwind in time, continuing anyway")
	return nil
}

// UnregisterActiveResponse removes the registration made by
// RegisterActiveResponse. Only removes the handle if it is still the one
// registered (a newer response may have replaced it).
func UnregisterActiveResponse(esn string, resp *activeResponse) {
	if resp == nil {
		return
	}
	activeResponsesMu.Lock()
	if activeResponses[esn] == resp {
		delete(activeResponses, esn)
	}
	activeResponsesMu.Unlock()
}

// bargeInState tracks whether the robot has actually started speaking the
// response. The wake word event that triggers the request itself (and
// duplicates/re-triggers of it around the greeting intent) must not be
// treated as barge-in - only wake words detected while the robot is
// audibly speaking, at least wakeWordGrace after speech began.
type bargeInState struct {
	mu           sync.Mutex
	speakStarted bool
	startedAt    time.Time
}

// wakeWordGrace is how long after the robot starts speaking a wake word
// event is still ignored, so late/repeated wake events from the request
// startup cannot kill the response before the user hears anything.
const wakeWordGrace = 1500 * time.Millisecond

func (b *bargeInState) markSpeaking() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.speakStarted {
		b.speakStarted = true
		b.startedAt = time.Now()
	}
}

func (b *bargeInState) wakeWordCanInterrupt() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.speakStarted && time.Since(b.startedAt) > wakeWordGrace
}

// InterruptKGSimWhenTouchedOrWaked watches the robot's event stream while an
// LLM response is being spoken. If the user says the wake word while the
// robot is speaking, or presses the button (touch sensor), it cancels the
// response context, which stops the speech mid-sentence. Returns true if it
// triggered an interrupt, false if it exited because the response ended
// normally (stopStop was signalled).
func InterruptKGSimWhenTouchedOrWaked(rob *vector.Vector, cancel context.CancelFunc, stopStop chan bool, bstate *bargeInState) bool {
	strm, err := rob.Conn.EventStream(
		context.Background(),
		&vectorpb.EventRequest{
			ListType: &vectorpb.EventRequest_WhiteList{
				WhiteList: &vectorpb.FilterList{
					List: []string{"robot_state", "wake_word"},
				},
			},
		},
	)
	if err != nil {
		logger.Println("Couldn't make an event stream: " + err.Error())
		return false
	}
	// when the response ends normally, close the event stream so the
	// blocking Recv() below unblocks and this watcher exits
	go func() {
		for range stopStop {
			logger.Println("KG Interrupter has been stopped")
			strm.CloseSend()
			break
		}
	}()
	// grab the untouched touch value as the baseline
	var origTouchValue uint32
	var origValueGotten bool
	for {
		var resp *vectorpb.EventResponse
		resp, err = strm.Recv()
		if err != nil {
			return false
		}
		switch resp.Event.EventType.(type) {
		case *vectorpb.Event_RobotState:
			origTouchValue = resp.Event.GetRobotState().TouchData.GetRawTouchValue()
			origValueGotten = true
		default:
		}
		if origValueGotten {
			break
		}
	}
	var valsAboveValue int
	var valsAboveValueMax int = 5
	for {
		var resp *vectorpb.EventResponse
		resp, err = strm.Recv()
		if err != nil {
			return false
		}
		switch resp.Event.EventType.(type) {
		case *vectorpb.Event_RobotState:
			if resp.Event.GetRobotState().TouchData.GetRawTouchValue() > origTouchValue+50 {
				valsAboveValue++
			} else {
				valsAboveValue = 0
			}
			if valsAboveValue > valsAboveValueMax {
				logger.Println("Interrupting LLM response (source: touch sensor)")
				cancel()
				return true
			}
		case *vectorpb.Event_WakeWord:
			if bstate.wakeWordCanInterrupt() {
				logger.Println("Interrupting LLM response (source: wake word)")
				cancel()
				return true
			}
			logger.Println("(barge-in) ignoring wake word event (robot has not been speaking long enough)")
		default:
		}
	}
}

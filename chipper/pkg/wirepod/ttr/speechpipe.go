package wirepod_ttr

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

// TTS prefetch pipeline.
//
// Speech is produced sentence by sentence: the LLM stream is split at
// sentence-ending punctuation and every sentence is spoken as it comes.
// Synthesizing a sentence with an API TTS provider (Qwen3-TTS, OpenAI TTS)
// takes a second or more, so doing it only when the sentence is due to be
// spoken leaves an audible gap between every two sentences - the response
// sounds choppy ("part and part").
//
// The prefetcher hides that latency: while sentence N is playing, the audio
// for the next sentences is already being synthesized in the background.
// When a sentence is due, its audio is usually ready and playback starts
// immediately, so the whole response sounds continuous.
//
// Prefetching is bounded by speechPrefetchLookahead sentences, so a barge-in
// interrupt wastes at most a couple of TTS calls. The prefetcher is scoped
// to one response: in-flight synthesis is cancelled with the response
// context and the whole prefetcher is garbage collected afterwards.

// speechPrefetchLookahead is how many sentences ahead of the one currently
// being spoken get synthesized in the background. One sentence of lookahead
// usually hides the TTS latency; two also covers very short sentences whose
// playback is shorter than the synthesis of the next one.
const speechPrefetchLookahead = 2

// errSpeechBuiltin is returned by synthesizeSpeech when the text should be
// spoken by the robot's built-in voice (SayText) - no API TTS provider
// applies, so there is nothing to prefetch.
var errSpeechBuiltin = errors.New("spoken with built-in robot voice")

type speechEntry struct {
	done   chan struct{}
	chunks [][]byte
	err    error
}

type speechPrefetcher struct {
	ctx     context.Context
	synth   func(ctx context.Context, input string) ([][]byte, error)
	mu      sync.Mutex
	entries map[string]*speechEntry
}

func newSpeechPrefetcher(ctx context.Context) *speechPrefetcher {
	return &speechPrefetcher{
		ctx:     ctx,
		synth:   synthesizeSpeech,
		entries: make(map[string]*speechEntry),
	}
}

// speechKey normalizes spoken text the same way DoSayText does before
// playback, so a prefetched segment matches the lookup done when the
// sentence is due to be spoken.
func speechKey(text string) string {
	return strings.TrimSpace(removeSpecialCharacters(text))
}

// prefetchSentence parses a queued LLM sentence and starts synthesizing
// every spoken segment in it (the text between commands) in the background.
func (p *speechPrefetcher) prefetchSentence(sentence string) {
	if p == nil {
		return
	}
	for _, act := range GetActionsFromString(sentence) {
		if act.Action == ActionSayText {
			p.prefetchText(act.Parameter)
		}
	}
}

// prefetchText starts background synthesis of one spoken segment. Calls are
// deduplicated by normalized text: identical segments are synthesized once
// and the audio is reused.
func (p *speechPrefetcher) prefetchText(text string) {
	if p == nil || p.ctx.Err() != nil {
		return
	}
	key := speechKey(text)
	if key == "" {
		return
	}
	p.mu.Lock()
	if _, exists := p.entries[key]; exists {
		p.mu.Unlock()
		return
	}
	entry := &speechEntry{done: make(chan struct{})}
	p.entries[key] = entry
	p.mu.Unlock()
	go func() {
		defer close(entry.done)
		entry.chunks, entry.err = p.synth(p.ctx, key)
		if entry.err == nil {
			logger.Println("(TTS prefetch) audio ready: \"" + key + "\"")
		}
	}()
}

// get returns the prefetched audio for text, waiting for in-flight
// synthesis to finish (or for ctx to be cancelled on barge-in). ok is false
// when the text was never prefetched - the caller should synthesize it on
// the spot instead.
func (p *speechPrefetcher) get(ctx context.Context, text string) (chunks [][]byte, err error, ok bool) {
	if p == nil {
		return nil, nil, false
	}
	p.mu.Lock()
	entry, exists := p.entries[speechKey(text)]
	p.mu.Unlock()
	if !exists {
		return nil, nil, false
	}
	select {
	case <-entry.done:
		return entry.chunks, entry.err, true
	case <-ctx.Done():
		return nil, ctx.Err(), true
	}
}

// synthesizeSpeech produces 16 kHz PCM chunks for the configured API TTS
// provider. It returns errSpeechBuiltin when the robot's built-in voice
// should speak the text instead. The provider decision tree mirrors what
// DoSayText used to do inline (including the Qwen -> OpenAI/built-in
// fallback on error).
func synthesizeSpeech(ctx context.Context, input string) ([][]byte, error) {
	// Qwen3-TTS (DashScope): API-based speech so the robot can speak Chinese.
	// In "auto" mode only text containing Chinese characters uses it, so
	// English keeps the robot's built-in Vector voice.
	if QwenTTSActive() && (vars.APIConfig.TTS.Mode == "all" || containsCJK(input)) {
		chunks, err := synthesizeQwen(ctx, input)
		if err == nil {
			return chunks, nil
		}
		logger.Println("(Qwen TTS) error, falling back: " + err.Error())
		logger.LogUI("(Qwen TTS) error, falling back: " + err.Error())
	}
	if (vars.APIConfig.STT.Language != "en-US" && vars.APIConfig.Knowledge.Provider == "openai") || vars.APIConfig.Knowledge.OpenAIVoiceWithEnglish {
		return synthesizeOpenAI(ctx, input)
	}
	return nil, errSpeechBuiltin
}

package wirepod_ttr

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSynth returns a synth function that records how often it was called
// and which texts it synthesized.
func fakeSynth(calls *int32, seen *[][]byte) func(ctx context.Context, input string) ([][]byte, error) {
	return func(ctx context.Context, input string) ([][]byte, error) {
		atomic.AddInt32(calls, 1)
		if seen != nil {
			*seen = append(*seen, []byte(input))
		}
		return [][]byte{[]byte("audio:" + input)}, nil
	}
}

func TestPrefetcherDeduplicates(t *testing.T) {
	var calls int32
	pf := newSpeechPrefetcher(context.Background())
	pf.synth = fakeSynth(&calls, nil)

	pf.prefetchText("Hello there.")
	pf.prefetchText("Hello there.") // duplicate: must not synthesize again
	pf.prefetchText("General Kenobi.")

	chunks, err, ok := pf.get(context.Background(), "Hello there.")
	if !ok || err != nil {
		t.Fatalf("get failed: ok=%v err=%v", ok, err)
	}
	if string(chunks[0]) != "audio:Hello there." {
		t.Fatalf("unexpected chunks: %q", chunks[0])
	}
	if _, err, ok := pf.get(context.Background(), "General Kenobi."); !ok || err != nil {
		t.Fatalf("get failed: ok=%v err=%v", ok, err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 synthesis calls, got %d", got)
	}
}

func TestPrefetcherMiss(t *testing.T) {
	pf := newSpeechPrefetcher(context.Background())
	if _, _, ok := pf.get(context.Background(), "never prefetched"); ok {
		t.Fatal("expected ok=false for a text that was never prefetched")
	}
}

func TestPrefetcherNilSafe(t *testing.T) {
	var pf *speechPrefetcher
	pf.prefetchSentence("hello.") // must not panic
	if _, _, ok := pf.get(context.Background(), "hello."); ok {
		t.Fatal("expected ok=false from a nil prefetcher")
	}
}

func TestPrefetcherSkipsBuiltinVoice(t *testing.T) {
	pf := newSpeechPrefetcher(context.Background())
	pf.synth = func(ctx context.Context, input string) ([][]byte, error) {
		return nil, errSpeechBuiltin
	}
	pf.prefetchText("Built in voice sentence.")
	_, err, ok := pf.get(context.Background(), "Built in voice sentence.")
	if !ok {
		t.Fatal("expected entry to exist")
	}
	if !errors.Is(err, errSpeechBuiltin) {
		t.Fatalf("expected errSpeechBuiltin, got %v", err)
	}
}

func TestPrefetcherCancelsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pf := newSpeechPrefetcher(ctx)
	started := make(chan struct{})
	pf.synth = func(ctx context.Context, input string) ([][]byte, error) {
		close(started)
		<-ctx.Done() // synthesis blocks until the response is interrupted
		return nil, ctx.Err()
	}
	pf.prefetchText("Slow sentence.")
	<-started
	cancel()
	_, err, ok := pf.get(context.Background(), "Slow sentence.")
	if !ok {
		t.Fatal("expected entry to exist")
	}
	if err == nil {
		t.Fatal("expected the cancelled synthesis error to surface")
	}
}

func TestPrefetcherWaitsForInflight(t *testing.T) {
	pf := newSpeechPrefetcher(context.Background())
	release := make(chan struct{})
	pf.synth = func(ctx context.Context, input string) ([][]byte, error) {
		<-release
		return [][]byte{[]byte("audio:" + input)}, nil
	}
	pf.prefetchText("Soon.")
	got := make(chan string)
	go func() {
		chunks, _, _ := pf.get(context.Background(), "Soon.")
		got <- string(chunks[0])
	}()
	select {
	case <-got:
		t.Fatal("get returned before synthesis finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case text := <-got:
		if text != "audio:Soon." {
			t.Fatalf("unexpected chunks: %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("get did not return after synthesis finished")
	}
}

func TestPrefetchSentenceParsesActions(t *testing.T) {
	var calls int32
	var seen [][]byte
	pf := newSpeechPrefetcher(context.Background())
	pf.synth = fakeSynth(&calls, &seen)

	// only the spoken segments get prefetched, not the command
	pf.prefetchSentence("Sure! {{setEyeColor||blue}} My eyes are blue now.")
	if _, _, ok := pf.get(context.Background(), "Sure!"); !ok {
		t.Fatal("expected 'Sure!' to be prefetched")
	}
	if _, _, ok := pf.get(context.Background(), "My eyes are blue now."); !ok {
		t.Fatal("expected 'My eyes are blue now.' to be prefetched")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 synthesis calls, got %d", got)
	}
	for _, text := range seen {
		if string(text) == "blue" {
			t.Fatal("command parameter must not be synthesized as speech")
		}
	}
}

func TestPrefetcherNormalization(t *testing.T) {
	var calls int32
	pf := newSpeechPrefetcher(context.Background())
	pf.synth = fakeSynth(&calls, nil)

	// DoSayText normalizes with removeSpecialCharacters before lookup;
	// prefetching the raw form must match the normalized lookup
	pf.prefetchText("What’s up — really?")
	_, err, ok := pf.get(context.Background(), "What's up - really?")
	if !ok || err != nil {
		t.Fatalf("normalized lookup failed: ok=%v err=%v", ok, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 synthesis call, got %d", got)
	}
}

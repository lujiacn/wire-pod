package wirepod_ttr

import (
	"encoding/binary"
	"testing"
)

func makeTestWav(sampleRate, channels int, samples []int16) []byte {
	dataLen := len(samples) * 2
	buf := make([]byte, 0, 44+dataLen)
	appendStr := func(b []byte, s string) []byte { return append(b, s...) }
	buf = appendStr(buf, "RIFF")
	buf = binary.LittleEndian.AppendUint32(buf, uint32(36+dataLen))
	buf = appendStr(buf, "WAVE")
	buf = appendStr(buf, "fmt ")
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1) // PCM
	buf = binary.LittleEndian.AppendUint16(buf, uint16(channels))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(sampleRate))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(sampleRate*channels*2))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(channels*2))
	buf = binary.LittleEndian.AppendUint16(buf, 16)
	buf = appendStr(buf, "data")
	buf = binary.LittleEndian.AppendUint32(buf, uint32(dataLen))
	for _, s := range samples {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(s))
	}
	return buf
}

func TestWavToPCM24k(t *testing.T) {
	samples := []int16{0, 1000, -1000, 500, -500}
	wav := makeTestWav(24000, 1, samples)
	pcm, sr, err := wavToPCM(wav)
	if err != nil {
		t.Fatalf("wavToPCM error: %v", err)
	}
	if sr != 24000 {
		t.Fatalf("expected sample rate 24000, got %d", sr)
	}
	got := bytesToInt16s(pcm)
	if len(got) != len(samples) {
		t.Fatalf("expected %d samples, got %d", len(samples), len(got))
	}
	for i := range samples {
		if got[i] != samples[i] {
			t.Fatalf("sample %d: expected %d, got %d", i, samples[i], got[i])
		}
	}
}

func TestWavToPCMStereo(t *testing.T) {
	samples := []int16{100, 300, -200, 400} // two L/R pairs
	wav := makeTestWav(24000, 2, samples)
	pcm, sr, err := wavToPCM(wav)
	if err != nil {
		t.Fatalf("wavToPCM error: %v", err)
	}
	if sr != 24000 {
		t.Fatalf("expected sample rate 24000, got %d", sr)
	}
	got := bytesToInt16s(pcm)
	if len(got) != 2 {
		t.Fatalf("expected 2 mono samples, got %d", len(got))
	}
	if got[0] != 200 || got[1] != 100 {
		t.Fatalf("expected mixdown [200 100], got %v", got)
	}
}

func TestResampleTo16k(t *testing.T) {
	// a 48 kHz buffer of N samples becomes N/3 samples at 16 kHz
	in := make([]int16, 4800)
	for i := range in {
		in[i] = int16(i % 251)
	}
	out := resamplePCMTo16k(int16sToBytes(in), 48000)
	if got := len(bytesToInt16s(out)); got != 1600 {
		t.Fatalf("expected 1600 samples after resample, got %d", got)
	}
	// 16 kHz input must pass through untouched
	out = resamplePCMTo16k(int16sToBytes(in), 16000)
	if len(out) != len(in)*2 {
		t.Fatalf("16 kHz input should pass through unchanged")
	}
}

func TestContainsCJK(t *testing.T) {
	if !containsCJK("你好") || !containsCJK("你好，世界！") || !containsCJK("mixed 中文 text") {
		t.Fatal("expected CJK text to be detected")
	}
	if containsCJK("Hello, world!") || containsCJK("") {
		t.Fatal("expected non-CJK text not to be detected")
	}
}

func TestChunkPCM16k(t *testing.T) {
	// 3 seconds of 16 kHz silence -> ~3 s of chunks of exactly 1024 bytes each
	in := make([]byte, 16000*2*3)
	chunks := chunkPCM16k(in)
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	total := 0
	for _, c := range chunks {
		if len(c) != 1024 {
			t.Fatalf("expected 1024-byte chunks, got %d", len(c))
		}
		total += len(c)
	}
	if total < len(in)-1024 {
		t.Fatalf("chunked %d bytes from %d input bytes", total, len(in))
	}
}

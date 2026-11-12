package clean

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"q2-pocket/internal/sponsor"
)

func TestCaptions(t *testing.T) {
	text := strings.Repeat("word ", 30) + strings.Repeat("x", 70)
	got := captions(text)
	for i, line := range got[:len(got)-1] {
		if len(line) > captionMax || strings.HasPrefix(line, " ") || strings.HasSuffix(line, " ") {
			t.Errorf("line %d %q", i, line)
		}
	}
	if got[len(got)-1] != strings.Repeat("x", 70) || strings.Join(got, " ") != strings.TrimSpace(text) {
		t.Errorf("captions %q", got)
	}
	if captions("  ") != nil {
		t.Error("blank")
	}
}

// The transcript beside an episode: an .lrc of short lines, its times moved as the audio was, a cue
// whose start was cut left out, a long one's lines timed through it by length.
func TestKeepTranscript(t *testing.T) {
	dir := t.TempDir()
	ep := filepath.Join(dir, "2024-10-07 - Show.mp3")
	if TranscriptPath(ep) != filepath.Join(dir, "2024-10-07 - Show.lrc") {
		t.Fatal(TranscriptPath(ep))
	}
	rep := &Report{Cuts: []Cut{{From: 10, To: 40, Kind: "ad"}}}
	cues := []sponsor.Cue{
		{From: 1, To: 4, Text: "Hello there."},
		{From: 20, To: 25, Text: "Buy this thing."},
		{From: 61.5, To: 81.5, Text: strings.Repeat("abcd ", 20)},
		{From: 3725.25, To: 3726, Text: "An hour in."},
	}
	keepTranscript(ep, cues, func(t float64) (float64, bool) {
		for _, k := range rep.Cuts {
			if t >= k.From && t < k.To {
				return 0, false
			}
		}
		return rep.Kept(t), true
	})
	b, err := os.ReadFile(TranscriptPath(ep))
	if err != nil {
		t.Fatal(err)
	}
	want := "[00:01.00]Hello there.\n" +
		"[00:31.50]" + strings.TrimSpace(strings.Repeat("abcd ", 12)) + "\n" +
		"[00:43.54]" + strings.TrimSpace(strings.Repeat("abcd ", 8)) + "\n" +
		"[61:35.25]An hour in.\n"
	if string(b) != want {
		t.Errorf("got\n%s\nwant\n%s", b, want)
	}
	keepTranscript("", cues, func(t float64) (float64, bool) { return t, true }) // no episode: nothing
}

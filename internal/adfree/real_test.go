//go:build realaudio

package adfree

// Real episodes, downloaded and labelled by transcribing them (2026-11-09):
//
//	Q2POCKET_DAI=/path/to/dir go test -tags realaudio -run TestReal -v ./internal/adfree
//
// The directory holds the files named below. Each label is a stretch of the episode ("show") or an
// advert ("ad"), in seconds; "first" marks an advert that is the episode's short first part, which
// only evidence or the feed's length may cut (it can't be told from a cold open by its shape).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"q2-pocket/internal/mp3"
)

type label struct {
	kind     string
	from, to float64
}

type realCase struct {
	file   string
	copy   string   // another copy, fetched again
	peers  []string // other episodes of the show
	budget float64
	labels []label
}

var cases = []realCase{
	{file: "te-a.mp3", peers: []string{"te2.mp3", "te3.mp3", "te4.mp3", "te5.mp3", "te6.mp3"}, labels: []label{
		{"first", 0, 42.7}, {"ad", 42.7, 72.9}, {"show", 72.9, 633.9}, {"ad", 633.9, 702.0}, {"ad", 702.0, 772.5},
		{"show", 772.5, 1305.4}, {"ad", 1305.4, 1369.8}, {"ad", 1369.8, 1434.6}, {"show", 1434.6, 1953.6},
		{"ad", 1953.6, 1999.9}, {"ad", 1999.9, 2046.1}}},
	{file: "te2.mp3", peers: []string{"te-a.mp3", "te3.mp3"}, labels: []label{
		{"show", 0, 59.5}, {"ad", 59.5, 130.4}, {"show", 130.4, 948.0}, {"ad", 948.0, 1040.5},
		{"show", 1040.5, 2085.0}, {"ad", 2085.0, 2161.1}}},
	{file: "daily-a.mp3", copy: "daily-b.mp3", budget: 1806, labels: []label{
		{"ad", 0, 32.6}, {"show", 32.6, 941.7}, {"ad", 941.7, 1034.3}, {"show", 1034.8, 1931.3}, {"ad", 1931.3, 1961.3}}},
	{file: "daily2.mp3", budget: 2502, labels: []label{
		{"ad", 0, 60.2}, {"show", 60.2, 495.2}, {"ad", 495.2, 586.6}, {"show", 586.6, 1377.4}, {"ad", 1377.4, 1468.8},
		{"show", 1468.8, 2744.6}, {"ad", 2744.6, 2775.7}}},
	{file: "pm.mp3", budget: 1943, labels: []label{
		{"ad", 0, 15.9}, {"show", 15.9, 321.3}, {"ad", 321.3, 395.4}, {"show", 395.4, 1475.4}, {"ad", 1475.4, 1523.6},
		{"show", 1523.6, 2080.7}, {"ad", 2080.7, 2129.7}}},
	{file: "sysk.mp3", labels: []label{
		{"first", 4.1, 38.8}, {"show", 38.8, 1341.3}, {"ad", 1341.3, 1376.7}, {"show", 1376.7, 2709.8}}},
	{file: "morbid.mp3", labels: []label{{"show", 0, 2628.0}, {"ad", 2628.0, 2658.1}}},
	{file: "rih.mp3", labels: []label{
		{"first", 0, 112.4}, {"ad", 112.4, 142.5}, {"show", 142.5, 4183.4}, {"ad", 4183.4, 4213.5}}},
	{file: "lib.mp3", labels: []label{{"show", 0, 1727.5}}},
	{file: "bz.mp3", labels: []label{{"show", 0, 3407.6}}},
}

func TestReal(t *testing.T) {
	dir := os.Getenv("Q2POCKET_DAI")
	if dir == "" {
		t.Skip("Q2POCKET_DAI unset")
	}
	hashes := func(name string) []uint64 {
		s, err := mp3.ScanFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(name, err)
		}
		return FoldAll(s.Hashes())
	}
	var adSec, adCut, showLost float64
	for _, c := range cases {
		s, err := mp3.ScanFile(filepath.Join(dir, c.file))
		if err != nil {
			t.Fatal(c.file, err)
		}
		ev := Evidence{Budget: c.budget, Now: time.Now()}
		if c.copy != "" {
			ev.Copy = hashes(c.copy)
		}
		for _, p := range c.peers {
			ev.Peers = append(ev.Peers, hashes(p))
		}
		start := time.Now()
		r := Detect(s, ev)
		took := time.Since(start)
		var out []string
		for _, k := range r.Cuts {
			out = append(out, fmt.Sprintf("%.1f-%.1f %s", s.At(k.From), s.At(k.To), k.Why))
		}
		t.Logf("%s: %d parts, %.0f ms; cut %s", c.file, r.Parts, float64(took.Microseconds())/1000, strings.Join(out, "; "))
		if len(r.Cuts) > 0 {
			writeAndPlay(t, s, filepath.Join(dir, c.file), r.Cuts)
		}
		for _, l := range c.labels {
			from, to := s.FrameAt(l.from), s.FrameAt(l.to)
			in := 0
			for _, k := range r.Cuts {
				in += max(0, min(k.To, to)-max(k.From, from))
			}
			got := s.Seconds(0, in)
			switch l.kind {
			case "show":
				showLost += got
				if got > 1.5 {
					t.Errorf("%s: %.1f s of the episode at %.1f-%.1f cut", c.file, got, l.from, l.to)
				}
			case "ad":
				adSec += l.to - l.from
				adCut += got
				if got < 0.9*(l.to-l.from) {
					t.Logf("  %s: advert %.1f-%.1f only %.1f s cut", c.file, l.from, l.to, got)
				}
			case "first":
				adSec += l.to - l.from
				adCut += got
			}
		}
	}
	t.Logf("adverts: %.0f of %.0f s cut (%.0f%%); episode lost: %.1f s", adCut, adSec, 100*adCut/adSec, showLost)
}

// writeAndPlay writes the episode without its cuts and plays it through ffmpeg: no decoding error,
// and the length the cuts leave.
func writeAndPlay(t *testing.T, s *mp3.Stream, src string, cuts []Cut) {
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	dst := filepath.Join(t.TempDir(), "out.mp3")
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Write(in, out, nil, Ranges(cuts))
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	want := s.Seconds(0, n)
	b, err := exec.Command("ffmpeg", "-v", "error", "-i", dst, "-f", "null", "-").CombinedOutput()
	if err != nil || len(b) > 0 {
		t.Errorf("%s: ffmpeg: %v %s", filepath.Base(src), err, b)
	}
	b, _ = exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", dst).Output()
	t.Logf("  written: %.1f s; ffprobe says %s", want, strings.TrimSpace(string(b)))
}

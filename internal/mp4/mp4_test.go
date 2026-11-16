package mp4

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// fragmented makes a fragmented AAC M4A as YouTube serves them (an empty moov, then moof/mdat
// pairs of about ten seconds), sec seconds of tone, with ffmpeg; the test is skipped without it.
func fragmented(t *testing.T, sec int) string {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	p := filepath.Join(t.TempDir(), "in.m4a")
	cmd := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration="+strconv.Itoa(sec),
		"-c:a", "aac", "-b:a", "128k", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-frag_duration", "10000000", p)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return p
}

// probe is ffmpeg's view of a file: its duration in seconds, its chapters' starts and titles, and
// its output (tags and all); it fails the test when ffmpeg can't decode the whole file.
func probe(t *testing.T, p string) (float64, []string, string) {
	out, _ := exec.Command("ffmpeg", "-v", "info", "-i", p, "-map", "0:a", "-f", "null", "-").CombinedOutput()
	s := string(out)
	if strings.Contains(s, "[aac @") || strings.Contains(s, "Error while decoding") {
		t.Fatalf("ffmpeg didn't read it:\n%s", s)
	}
	m := regexp.MustCompile(`time=(\d+):(\d+):(\d+\.\d+)`).FindAllStringSubmatch(s, -1)
	if m == nil {
		t.Fatalf("no time in:\n%s", s)
	}
	last := m[len(m)-1]
	h, _ := strconv.Atoi(last[1])
	mi, _ := strconv.Atoi(last[2])
	sec, _ := strconv.ParseFloat(last[3], 64)
	var ch []string
	input, _, _ := strings.Cut(s, "Output #0")
	for _, c := range regexp.MustCompile(`Chapter #\d+:\d+: start (\d+\.\d+)`).FindAllStringSubmatch(input, -1) {
		ch = append(ch, c[1])
	}
	return float64(h*3600+mi*60) + sec, ch, s
}

func TestCutAndTag(t *testing.T) {
	in := fragmented(t, 60)
	f, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	st, err := Read(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	if d := st.Duration(); d < 59.9 || d > 60.2 {
		t.Fatalf("duration %.2f", d)
	}
	if len(st.runs) < 5 {
		t.Fatalf("%d runs: not fragmented as asked", len(st.runs))
	}
	cuts := st.Ranges([][2]float64{{10, 20}, {30, 35}, {33, 40}})
	if len(cuts) != 2 {
		t.Fatalf("cuts %v", cuts)
	}
	out := filepath.Join(t.TempDir(), "out.m4a")
	o, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	cover := []byte("\xff\xd8\xff\xe0 not really a jpeg")
	w, err := st.Write(o, f, cuts, &Tags{Title: "Ep 1 – Ünïcode", Artist: "Host", Album: "Show", Date: "2026-11-10", Cover: cover},
		[]Chapter{{0, "Introduction"}, {15000, "Middle"}, {30000, strings.Repeat("é", 200)}})
	o.Close()
	if err != nil {
		t.Fatal(err)
	}
	if w.Length < 39.8 || w.Length > 40.2 {
		t.Fatalf("written length %.2f, want 40", w.Length)
	}
	if fi, _ := os.Stat(out); fi.Size() != w.Size {
		t.Fatalf("size %d, Write said %d", fi.Size(), w.Size)
	}
	d, ch, s := probe(t, out)
	if d < 39.8 || d > 40.2 {
		t.Fatalf("ffmpeg decoded %.2f s, want 40:\n%s", d, s)
	}
	if strings.Join(ch, ",") != "0.000000,15.000000,30.000000" {
		t.Fatalf("chapters %v:\n%s", ch, s)
	}
	for _, want := range []string{"title           : Ep 1 – Ünïcode", "artist          : Host", "album           : Show", "genre           : Podcast", "Middle"} {
		if !strings.Contains(s, want) {
			t.Fatalf("no %q in:\n%s", want, s)
		}
	}
}

func TestInPlace(t *testing.T) {
	in := fragmented(t, 45)
	f, err := os.OpenFile(in, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := f.Stat()
	st, err := Read(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.Write(f, f, st.Ranges([][2]float64{{5, 9}}), &Tags{Title: "T"}, nil)
	if err == nil {
		err = f.Truncate(w.Size)
	}
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if d, _, s := probe(t, in); d < 40.8 || d > 41.2 {
		t.Fatalf("ffmpeg decoded %.2f s, want 41:\n%s", d, s)
	}
	// and the audio itself: the same as a copy written beside it
	f2, _ := os.Open(fragmented(t, 45))
	fi2, _ := f2.Stat()
	st2, _ := Read(f2, fi2.Size())
	copyPath := filepath.Join(t.TempDir(), "copy.m4a")
	o, _ := os.Create(copyPath)
	st2.Write(o, f2, st2.Ranges([][2]float64{{5, 9}}), &Tags{Title: "T"}, nil)
	o.Close()
	f2.Close()
	a, _ := os.ReadFile(in)
	b, _ := os.ReadFile(copyPath)
	if string(a) != string(b) {
		t.Fatal("written in place, it differs from a copy")
	}
}

func TestSampleAt(t *testing.T) {
	st := &Stream{tr: &track{timescale: 44100}, dur: 1024, sizes: make([]uint16, 100)}
	st.total = 100 * 1024
	if i := st.SampleAt(1024.0 / 44100); i != 1 {
		t.Fatalf("SampleAt one sample = %d", i)
	}
	if i := st.SampleAt(1.5 * 1024 / 44100); i != 2 {
		t.Fatalf("SampleAt one and a half = %d", i)
	}
	if i := st.SampleAt(1000); i != 100 {
		t.Fatalf("past the end = %d", i)
	}
	if a := st.At(2); a != 2048.0/44100 {
		t.Fatalf("At(2) = %v", a)
	}
}

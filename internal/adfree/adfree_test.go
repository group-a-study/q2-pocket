package adfree

import (
	"bytes"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"q2-pocket/internal/mp3"
)

// Real frames: the mp3 package's fixtures, separately encoded files as a server has them.

func scan(t *testing.T, b []byte) *mp3.Stream {
	t.Helper()
	s, err := mp3.Scan(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fixture(t *testing.T, name string) (*mp3.Stream, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "mp3", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return scan(t, b), b
}

// audio is a file's audio frames [from, to), whole.
func audio(s *mp3.Stream, b []byte, from, to int) []byte {
	return b[s.Frames[from].Off : s.Frames[to-1].Off+int64(s.Frames[to-1].Len)]
}

// copyWith is the episode served with ad between its frames at and at+1, and post after it; and
// where each advert lies in the copy's frames.
func copyWith(t *testing.T, at int, ad, post string) ([]byte, Span, Span) {
	c, cb := fixture(t, "content.mp3")
	out := append([]byte(nil), cb[:c.Frames[at].Off]...)
	var mid, end Span
	frames := at
	if ad != "" {
		a, ab := fixture(t, ad)
		out = append(out, audio(a, ab, a.Info, len(a.Frames))...)
		mid = Span{frames, frames + len(a.Frames) - a.Info}
		frames = mid.To
	}
	out = append(out, audio(c, cb, at, len(c.Frames))...)
	frames += len(c.Frames) - at
	if post != "" {
		p, pb := fixture(t, post)
		out = append(out, audio(p, pb, p.Info, len(p.Frames))...)
		end = Span{frames, frames + len(p.Frames) - p.Info}
	}
	return out, mid, end
}

func covers(cuts []Cut, sp Span) bool {
	for _, c := range cuts {
		if c.From <= sp.From && c.To >= sp.To {
			return true
		}
	}
	return false
}

func cutFrames(cuts []Cut) int { return length(cuts) }

// Two copies with other adverts: each one's adverts are found, the episode left whole, and the
// other copy's advert learned.
func TestCopies(t *testing.T) {
	a, mid, end := copyWith(t, 150, "ad1.mp3", "ad2.mp3")
	b, _, _ := copyWith(t, 150, "ad2.mp3", "")
	sa, sb := scan(t, a), scan(t, b)
	r := Detect(sa, Evidence{Copy: FoldAll(sb.Hashes()), Now: time.Now()})
	if !covers(r.Cuts, mid) || !covers(r.Cuts, end) {
		t.Fatalf("cuts %+v, adverts at %v and %v", r.Cuts, mid, end)
	}
	if n, want := cutFrames(r.Cuts), (mid.To-mid.From)+(end.To-end.From); n != want {
		t.Errorf("cut %d frames, the adverts are %d", n, want)
	}
	if len(r.Learn) == 0 {
		t.Error("nothing learned")
	}
}

// Copies that are the same file say nothing.
func TestSameCopy(t *testing.T) {
	a, _, _ := copyWith(t, 150, "", "")
	s := scan(t, a)
	if r := Detect(s, Evidence{Copy: FoldAll(s.Hashes())}); len(r.Cuts) != 0 {
		t.Errorf("cuts %+v", r.Cuts)
	}
}

// An advert this episode shares with another is found; the episode itself never matches.
func TestPeer(t *testing.T) {
	a, mid, _ := copyWith(t, 200, "ad1.mp3", "")
	ad, ab := fixture(t, "ad1.mp3")
	other, ob := fixture(t, "ad2.mp3")
	peer := append(append([]byte(nil), ob...), audio(ad, ab, ad.Info, len(ad.Frames))...)
	_ = other
	r := Detect(scan(t, a), Evidence{Peers: [][]uint64{FoldAll(scan(t, peer).Hashes())}})
	if !covers(r.Cuts, mid) || cutFrames(r.Cuts) != mid.To-mid.From {
		t.Errorf("cuts %+v, advert %v", r.Cuts, mid)
	}
}

// The same audio published twice (another feed) isn't taken for an advert.
func TestPeerSameEpisode(t *testing.T) {
	a, _, _ := copyWith(t, 200, "", "")
	s := scan(t, a)
	if r := Detect(s, Evidence{Peers: [][]uint64{FoldAll(s.Hashes())}}); len(r.Cuts) != 0 {
		t.Errorf("cuts %+v", r.Cuts)
	}
}

// An advert learned once is found again, and the library keeps it across a save.
func TestLibrary(t *testing.T) {
	ad, _ := fixture(t, "ad1.mp3")
	path := filepath.Join(t.TempDir(), "ads.bin")
	l := LoadLibrary(path)
	l.Add(FoldAll(ad.Hashes()[ad.Info:]), time.Now())
	l.Add(FoldAll(ad.Hashes()[ad.Info:]), time.Now()) // known: not added twice
	if err := l.Save(); err != nil || l.Len() != 1 {
		t.Fatal(err, l.Len())
	}
	l = LoadLibrary(path)
	a, mid, _ := copyWith(t, 120, "ad1.mp3", "")
	r := Detect(scan(t, a), Evidence{Lib: l, Now: time.Now()})
	if !covers(r.Cuts, mid) || r.Cuts[0].Why != WhyHeard {
		t.Errorf("cuts %+v, advert %v", r.Cuts, mid)
	}
}

// An advert played twice in one episode is found both times.
func TestTwice(t *testing.T) {
	c, cb := fixture(t, "content.mp3")
	ad, ab := fixture(t, "ad2.mp3")
	adv := audio(ad, ab, ad.Info, len(ad.Frames))
	n := len(ad.Frames) - ad.Info
	in := append([]byte(nil), cb[:c.Frames[100].Off]...)
	in = append(in, adv...)
	in = append(in, audio(c, cb, 100, 300)...)
	in = append(in, adv...)
	in = append(in, audio(c, cb, 300, len(c.Frames))...)
	r := Detect(scan(t, in), Evidence{})
	one, two := Span{100, 100 + n}, Span{300 + n, 300 + 2*n}
	if !covers(r.Cuts, one) || !covers(r.Cuts, two) {
		t.Errorf("cuts %+v, adverts %v %v", r.Cuts, one, two)
	}
}

// Synthetic streams: frames with the padding, reservoir and header bits of separately encoded
// parts, and hashes of their own, long enough for an episode's shape.

type part struct {
	sec  float64
	sig  uint32 // header bits (Sig)
	kind int    // 0 episode, 1 advert: only for the test's own checks
}

const (
	head128 = 0xfffb9064 // MPEG-1 L3, 128 kb/s, 44.1 kHz, padded, joint stereo, original
	frameS  = 1152.0 / 44100
)

// synth is a stream of parts, each encoded alone: the unpadded frame every 24 or 25 (128 kb/s at
// 44.1 kHz), its phase random; the first frame's reservoir empty.
func synth(parts []part, seed int64) (*mp3.Stream, []Span) {
	rnd := rand.New(rand.NewSource(seed))
	s := &mp3.Stream{Version: 1, Layer: 3, SampleRate: 44100, Samples: 1152}
	var spans []Span
	for _, p := range parts {
		n := int(p.sec / frameS)
		lag := rnd.Intn(44100) // LAME's slot lag: 144000*128 % 44100 = 42300 taken each frame
		from := len(s.Frames)
		for i := 0; i < n; i++ {
			lag -= 42300
			pad := lag < 0
			if pad {
				lag += 44100
			}
			h := uint32(head128)&^0x1cf | p.sig
			if pad {
				h |= 1 << 9
			}
			mdb := uint16(1 + rnd.Intn(400))
			if i == 0 {
				mdb = 0
			}
			s.Frames = append(s.Frames, mp3.Frame{Off: int64(len(s.Frames)) * 418, Len: 418, Hash: rnd.Uint64(), MDB: mdb, Head: h})
		}
		spans = append(spans, Span{from, len(s.Frames)})
	}
	return s, spans
}

const sigShow, sigAd = 0x0064 & 0x1cf, 0x0060 & 0x1cf // "original" set for the show, not the adverts

func checkCuts(t *testing.T, s *mp3.Stream, spans []Span, parts []part, want []bool) {
	t.Helper()
	r := Detect(s, Evidence{Now: time.Now()})
	for i, sp := range spans {
		in := 0
		for _, c := range r.Cuts {
			in += max(0, min(c.To, sp.To)-max(c.From, sp.From))
		}
		got := float64(in) >= 0.9*float64(sp.To-sp.From)
		if got != want[i] {
			t.Errorf("part %d (%.0f s, kind %d): cut %v, want %v; cuts %+v", i, parts[i].sec, parts[i].kind, got, want[i], r.Cuts)
		}
		if !want[i] && float64(in) > 1.0/frameS {
			t.Errorf("part %d: %d of its frames cut", i, in)
		}
	}
}

// The shape of a Megaphone episode (Today, Explained): a cold open, a pre-roll, the episode in three
// long parts with two breaks of two adverts, and a post-roll. Every short part inside is an advert;
// the short first part is kept (it may be the cold open, as it is here).
func TestShape(t *testing.T) {
	parts := []part{{59.5, sigShow, 0}, {70.9, sigShow, 1}, {817.6, sigShow, 0}, {46.2, sigShow, 1}, {46.2, sigShow, 1},
		{1044.5, sigShow, 0}, {30.2, sigShow, 1}, {46.0, sigShow, 1}}
	s, spans := synth(parts, 1)
	if got := Parts(s); len(got) != len(parts) {
		t.Fatalf("%d parts, want %d: %+v", len(got), len(parts), got)
	}
	checkCuts(t, s, spans, parts, []bool{false, true, false, true, true, false, true, true})
}

// AdsWizz's adverts clear the "original" bit: a short first part that differs is an advert too.
func TestMarkedFirst(t *testing.T) {
	parts := []part{{32.6, sigAd, 1}, {909.1, sigShow, 0}, {30, sigAd, 1}, {32.3, sigAd, 1}, {30.7, sigAd, 1},
		{896.6, sigShow, 0}, {30, sigAd, 1}}
	s, spans := synth(parts, 2)
	checkCuts(t, s, spans, parts, []bool{true, false, true, true, true, false, true})
}

// The feed's length settles a short first part: the episode is exactly that much shorter.
func TestBudget(t *testing.T) {
	parts := []part{{42.7, sigShow, 1}, {600, sigShow, 0}, {68, sigShow, 1}, {700, sigShow, 0}}
	s, spans := synth(parts, 3)
	clean := (len(s.Frames) - (spans[0].To - spans[0].From) - (spans[2].To - spans[2].From)) * 1152
	r := Detect(s, Evidence{Budget: float64(clean) / 44100})
	if !covers(r.Cuts, spans[0]) || !covers(r.Cuts, spans[2]) || len(r.Cuts) != 2 || r.Cuts[0].Why != WhyLength {
		t.Errorf("cuts %+v", r.Cuts)
	}
	r = Detect(s, Evidence{Budget: float64(len(s.Frames)) * frameS}) // the length with everything: keep it
	if covers(r.Cuts, spans[0]) {
		t.Errorf("cut the first part against the budget: %+v", r.Cuts)
	}
}

// An episode without ad breaks is one part, cut nowhere; nor is a stream whose parts are all short.
func TestNoBreaks(t *testing.T) {
	s, _ := synth([]part{{2400, sigShow, 0}}, 4)
	if r := Detect(s, Evidence{}); len(r.Cuts) != 0 || r.Parts != 1 {
		t.Errorf("%+v", r)
	}
	s, spans := synth([]part{{50, sigShow, 0}, {40, sigShow, 0}, {60, sigShow, 0}}, 5)
	r := Detect(s, Evidence{})
	if len(r.Cuts) > 0 {
		t.Errorf("a short episode cut on its shape: %+v (parts %v)", r.Cuts, spans)
	}
}

// Breaks cut on their shape alone stop at a third of the episode.
func TestGuessCap(t *testing.T) {
	parts := []part{{181, sigShow, 0}, {170, sigShow, 1}, {181, sigShow, 0}, {170, sigShow, 1}, {181, sigShow, 0}}
	s, _ := synth(parts, 6)
	if r := Detect(s, Evidence{}); len(r.Cuts) != 0 {
		t.Errorf("%.0f%% cut on shape alone: %+v", 100*float64(cutFrames(r.Cuts))/float64(len(s.Frames)), r.Cuts)
	}
}

func TestSplicesExact(t *testing.T) {
	parts := []part{{300, sigShow, 0}, {30, sigAd, 1}, {300, sigShow, 0}}
	s, spans := synth(parts, 7)
	got := Splices(s)
	if len(got) != 2 || got[0].At != spans[1].From || got[1].At != spans[2].From || got[0].Soft {
		t.Errorf("splices %v, want %d and %d", got, spans[1].From, spans[2].From)
	}
	_ = math.Abs
}

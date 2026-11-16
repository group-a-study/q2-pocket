package clean

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"q2-pocket/internal/mp3"
	"q2-pocket/internal/pocketcasts"
)

func fixture(t *testing.T, name string) (*mp3.Stream, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "mp3", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s, err := mp3.Scan(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

// served is the episode with an advert after its frame at.
func served(t *testing.T, at int, ad string) []byte {
	c, cb := fixture(t, "content.mp3")
	a, ab := fixture(t, ad)
	out := append([]byte(nil), cb[:c.Frames[at].Off]...)
	out = append(out, ab[a.Frames[a.Info].Off:]...)
	return append(out, cb[c.Frames[at].Off:]...)
}

func TestClean(t *testing.T) {
	first := served(t, 200, "ad1.mp3")
	other := served(t, 200, "ad2.mp3")
	var ranged, fetched int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			ranged++
			w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(other)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(other[:1])
			return
		}
		fetched++
		w.Write(other)
	}))
	defer srv.Close()
	dir := t.TempDir()
	part := filepath.Join(dir, "ep.part")
	os.WriteFile(part, first, 0o644)
	c := &Cleaner{Dir: dir, Fetch: srv.Client(), API: srv.Client(), Agent: "test",
		PC:       pocketcasts.New(srv.Client(), "test", pocketcasts.Token{}),
		Settings: func() Settings { return Settings{Ads: true, Sponsors: true, Copy: true} }}
	var notes []string
	e := Episode{Key: "k", EID: "e", Show: "S", Title: "T", URL: srv.URL + "/ep.mp3"}
	ed, err := c.Clean(context.Background(), part, e, func(s string) { notes = append(notes, s) })
	if err != nil || ed == nil {
		t.Fatal(err)
	}
	if ranged != 1 || fetched != 1 {
		t.Errorf("probed %d, fetched %d", ranged, fetched)
	}
	a, _ := fixture(t, "ad1.mp3")
	adLen := a.Duration()
	if len(ed.Cuts) != 1 || ed.Cuts[0].From != 200 || ed.Cuts[0].To != 200+len(a.Frames)-a.Info {
		t.Fatalf("cuts %+v", ed.Cuts)
	}
	r := Load(dir, "k", "e")
	if r == nil || len(r.Cuts) != 1 || r.Cuts[0].Kind != "ad" || r.Cuts[0].Why != "differs between copies" {
		t.Fatalf("report %+v", r)
	}
	at := ed.Stream.At(200)
	if k := r.Kept(at + adLen + 3); math.Abs(k-(at+3)) > 0.01 {
		t.Errorf("Kept: %.2f, want %.2f", k, at+3)
	}
	if o := r.Original(at + 3); math.Abs(o-(at+adLen+3)) > 0.01 {
		t.Errorf("Original: %.2f, want %.2f", o, at+adLen+3)
	}
	if k := r.Kept(at + 1); math.Abs(k-at) > 0.01 { // a cut second: where its cut was
		t.Errorf("Kept inside a cut: %.2f", k)
	}
	if got := r.Summary(); got != "1 ad cut (4 s)" {
		t.Errorf("summary %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "frames", "k-e.bin")); err != nil {
		t.Error("frames not kept")
	}
	// the same advert in another episode (here ad2's audio stands for one) is found by comparing
	// with this one, with no second copy
	o, ob := fixture(t, "ad2.mp3")
	next := append([]byte(nil), ob[:o.Frames[100].Off]...)
	next = append(next, first[ed.Stream.Frames[200].Off:ed.Stream.Frames[200+len(a.Frames)-a.Info].Off]...)
	next = append(next, ob[o.Frames[100].Off:]...)
	os.WriteFile(part, next, 0o644)
	ed2, _ := c.Clean(context.Background(), part, Episode{Key: "k", EID: "f", Show: "S", Title: "U"}, func(string) {})
	if len(ed2.Cuts) != 1 || ed2.Cuts[0].From != 100 {
		t.Errorf("next episode: %+v", ed2.Cuts)
	}
}

// The same file from the server: no second download.
func TestSameCopy(t *testing.T) {
	first := served(t, 200, "ad1.mp3")
	fetched := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(first)))
			w.WriteHeader(http.StatusPartialContent)
			return
		}
		fetched++
		w.Write(first)
	}))
	defer srv.Close()
	dir := t.TempDir()
	part := filepath.Join(dir, "ep.part")
	os.WriteFile(part, first, 0o644)
	c := &Cleaner{Dir: dir, Fetch: srv.Client(), API: srv.Client(), Agent: "test",
		Settings: func() Settings { return Settings{Ads: true, Copy: true} }}
	ed, err := c.Clean(context.Background(), part, Episode{Key: "k", EID: "e", URL: srv.URL}, func(string) {})
	if err != nil || fetched != 0 || ed == nil {
		t.Errorf("fetched %d, %v", fetched, err)
	}
}

func TestNotMP3(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "ep.part")
	os.WriteFile(part, bytes.Repeat([]byte("m4a? "), 4000), 0o644)
	c := &Cleaner{Dir: dir, Settings: func() Settings { return Settings{Ads: true} }}
	if ed, err := c.Clean(context.Background(), part, Episode{Key: "k", EID: "e"}, func(string) {}); ed != nil || err != nil {
		t.Fatal(ed, err)
	}
	if r := Load(dir, "k", "e"); r == nil || r.Summary() != "Not an MP3: left as it came" {
		t.Errorf("%+v", r)
	}
}

// An episode too big to check for ads in the memory the Q2 has free goes in as it came, marked
// Unchecked (Ads Not Removed) for a Remove Ads Again later, rather than taking the app down with it
// or being checked in part (the user's rule: a whole check or none). The memory is reckoned by its
// frames, not its bytes (a 320 kbps episode has fewer a MB), with the library of adverts and the
// episodes it is compared with.
func TestTooBigForMemory(t *testing.T) {
	defer func(w time.Duration) { memoryWait = w }(memoryWait)
	memoryWait = 0
	dir := t.TempDir()
	part := filepath.Join(dir, "ep.part")
	_, b := fixture(t, "content.mp3")
	os.WriteFile(part, b, 0o644)
	frames := mp3.FramesIn(part)
	s, _ := mp3.ScanFile(part)
	if s == nil || frames < len(s.Frames) || frames > len(s.Frames)*5/4+16 {
		t.Fatal("frames", frames, s)
	}
	os.MkdirAll(filepath.Join(dir, "frames"), 0o755)
	os.WriteFile(filepath.Join(dir, "frames", "other-ep.bin"), make([]byte, 4000), 0o644)
	os.WriteFile(filepath.Join(dir, "ads.bin"), []byte("Q2ADS1\n"), 0o644)
	want := need(frames, 7, 4000) + reserve
	asked := 0
	c := &Cleaner{Dir: dir, Settings: func() Settings { return Settings{Ads: true, Sponsors: true} },
		Free: func() int64 { asked++; return want - 1 }}
	if ed, err := c.Clean(context.Background(), part, Episode{Key: "k", EID: "e"}, func(string) {}); ed != nil || err != nil || asked != 1 {
		t.Fatal(ed, err, asked)
	}
	if r := Load(dir, "k", "e"); r == nil || !r.Unchecked || len(r.Cuts) != 0 || !strings.HasPrefix(r.Summary(), "Ads not removed") {
		t.Errorf("%+v", r)
	}
	c.Free = func() int64 { return want }
	if ed, err := c.Clean(context.Background(), part, Episode{Key: "k", EID: "e"}, func(string) {}); ed == nil || err != nil {
		t.Fatal("room enough: checked", ed, err)
	}
	if r := Load(dir, "k", "e"); r == nil || r.Unchecked {
		t.Errorf("checked: %+v", r)
	}
}

// A cut copy that couldn't be written leaves the adverts in: Ads Not Removed.
func TestUncutIsUnchecked(t *testing.T) {
	c := &Cleaner{Dir: t.TempDir()}
	e := Episode{Key: "k", EID: "e"}
	c.save(e, &Report{Cuts: []Cut{{From: 1, To: 30, Kind: "ad"}}})
	c.Uncut(e, "Couldn't write the cut episode: left as it came")
	if r := Load(c.Dir, "k", "e"); r == nil || !r.Unchecked || len(r.Cuts) != 0 {
		t.Errorf("%+v", r)
	}
	c.save(e, &Report{})
	c.Uncut(e, "Couldn't write the cut episode: left as it came")
	if r := Load(c.Dir, "k", "e"); r == nil || r.Unchecked {
		t.Errorf("nothing found, nothing left in: %+v", r)
	}
}

// What a new copy leaves out of the old one (Recut), in the old copy's seconds: with an old copy as
// it came, every cut of the new download; else the new sponsor reads, less those the old copy had
// cut, both copies' adverts taken as cut alike.
func TestRecut(t *testing.T) {
	ad, read := func(f, t float64) Cut { return Cut{From: f, To: t, Kind: "ad"} }, func(f, t float64) Cut { return Cut{From: f, To: t, Kind: "sponsor"} }
	now := &Report{Cuts: []Cut{ad(10, 20), read(100, 160)}}
	if got := Recut(nil, now); !slices.Equal(got, [][2]float64{{10, 20}, {100, 160}}) {
		t.Errorf("as it came: %v", got)
	}
	if got := Recut(&Report{Cuts: []Cut{ad(30, 45)}}, now); !slices.Equal(got, [][2]float64{{90, 150}}) {
		t.Errorf("its adverts cut: %v", got)
	}
	if got := Recut(&Report{Cuts: []Cut{ad(10, 20), read(100, 130)}}, now); !slices.Equal(got, [][2]float64{{90, 120}}) {
		t.Errorf("a read cut already: %v", got)
	}
	if Recut(&Report{Cuts: []Cut{ad(10, 20), read(100, 160)}}, now) != nil || Recut(nil, nil) != nil {
		t.Error("nothing new")
	}
}

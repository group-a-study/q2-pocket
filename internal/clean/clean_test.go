package clean

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

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

// An episode too big for the memory the Q2 has free goes in as it came, unchecked, rather than
// taking the app down with it.
func TestTooBigForMemory(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "ep.part")
	_, b := fixture(t, "content.mp3")
	os.WriteFile(part, b, 0o644)
	asked := 0
	c := &Cleaner{Dir: dir, Settings: func() Settings { return Settings{Ads: true} },
		Free: func() int64 { asked++; return memoryFor(int64(len(b))) - 1 }}
	if ed, err := c.Clean(context.Background(), part, Episode{Key: "k", EID: "e"}, func(string) {}); ed != nil || err != nil || asked != 1 {
		t.Fatal(ed, err, asked)
	}
	if r := Load(dir, "k", "e"); r == nil || r.Summary() != "Too long to check in the Q2's free memory: left as it came" {
		t.Errorf("%+v", r)
	}
	c.Free = func() int64 { return memoryFor(int64(len(b))) }
	if ed, err := c.Clean(context.Background(), part, Episode{Key: "k", EID: "e"}, func(string) {}); ed == nil || err != nil {
		t.Fatal("room enough: checked", ed, err)
	}
}

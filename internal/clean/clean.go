// Package clean takes an episode as downloaded and finds what to cut from it: the adverts its host
// stitched in (package adfree), then the sponsor reads in the episode itself (package sponsor). It
// keeps what it learns on the card, in the app's folder:
//
//	ads.bin                     the adverts found so far
//	frames/<key>-<eid>.bin      each episode's frames, for later episodes to be compared with
//	cuts/<key>-<eid>.json       what was cut from each episode, and why (Report)
package clean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"q2-pocket/internal/adfree"
	"q2-pocket/internal/mp3"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/sponsor"
	"q2-pocket/internal/tag"
)

// Settings is what the user chose.
type Settings struct {
	Ads       bool // cut stitched-in adverts
	Sponsors  bool // cut sponsor reads
	SelfPromo bool // and the show's promotion of itself (SponsorBlock's "selfpromo")
	Copy      bool // fetch a second copy when it has other adverts, to compare

	YouTubeKey               string // YouTube Data API key, to find an episode's video
	SpotifyID, SpotifySecret string // a Spotify app's, to find an episode on Spotify
}

// Episode is what Clean knows of the episode it cleans.
type Episode struct {
	Key, EID            string // the library's ids, which name its files here
	Path                string // where it goes on the card: its notes go beside it (NotesPath)
	Show, Title, Author string
	Date                time.Time
	Duration            int    // seconds, as its feed or Pocket Casts gives it; 0 unknown
	URL                 string // its audio's address
	Podcast, UUID       string // Pocket Casts' ids, "" when it didn't come from there

	Transcript, TranscriptType, Chapters string   // its feed's Podcasting 2.0 ones
	Links                                []string // "yt:<id>", "sp:<id>" from its show notes
}

// Cleaner cleans one episode at a time.
type Cleaner struct {
	Dir      string
	Fetch    *http.Client // a second copy: no total timeout
	API      *http.Client // everything else
	Agent    string
	PC       *pocketcasts.Client
	Settings func() Settings
	Free     func() int64                     // bytes of memory free for a check (-1 unknown); nil: no limit
	Log      func(format string, args ...any) // the debug log's lines; nil for none

	mu    sync.Mutex // one episode at a time: the library of adverts is read and written whole
	notes map[string]notesAt
}

type notesAt struct {
	at time.Time
	n  map[string]pocketcasts.Notes
}

// Cut is seconds [From, To) of the episode as downloaded, and what it was.
type Cut struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
	Kind string  `json:"kind"` // "ad" or "sponsor"
	Why  string  `json:"why"`
}

// Report is what Clean did to an episode, kept beside it.
type Report struct {
	Checked time.Time `json:"checked"`
	Length  float64   `json:"length"` // seconds, as downloaded
	Cuts    []Cut     `json:"cuts,omitempty"`
	Note    string    `json:"note,omitempty"` // why it wasn't checked, or what didn't work
	Sources []string  `json:"sources,omitempty"`
}

// Edit is the frames to leave out of the download, and the stream they are of; and the chapters its
// publisher gives (milliseconds of the episode as cut), for a download without its own.
type Edit struct {
	Stream   *mp3.Stream
	Cuts     []mp3.Range
	Report   *Report
	Chapters []tag.Chapter
}

const (
	maxSponsorShare = 0.25  // of the episode: sponsor segments past this are a source gone wrong
	minSponsor      = 4.0   // seconds
	maxSponsor      = 360.0 // seconds
	peersSame       = 6     // the show's latest episodes compared with
	peersOther      = 6     // and other shows'
	framesKept      = 40
	notesFor        = 30 * time.Minute
)

func (c *Cleaner) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

// memoryFor is about the memory a check of an MP3 of size bytes takes besides what the app has: its
// frames (24 bytes each, a frame being about 400 bytes at 128 kbps), their hashes, another
// episode's and the matching's tables. Measured on a four-hour episode (230 MB): about 29 MB.
func memoryFor(size int64) int64 { return size / 8 }

func (c *Cleaner) path(kind, key, eid, ext string) string {
	return filepath.Join(c.Dir, kind, key+"-"+eid+ext)
}

// Clean finds what to cut from the download at part (an MP3; anything else is left as it is, with
// nil). note says what it is doing, for the Q2's page. A source that fails is passed over: the
// report says so.
func (c *Cleaner) Clean(ctx context.Context, part string, e Episode, note func(string)) (*Edit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	set := c.Settings()
	rep := &Report{Checked: time.Now()}
	c.keepNotes(ctx, e)
	if !set.Ads && !set.Sponsors {
		c.uncutTranscript(ctx, e)
		return nil, nil
	}
	note("Checking for ads")
	if st, err := os.Stat(part); err == nil && c.Free != nil {
		if free := c.Free(); free >= 0 && free < memoryFor(st.Size()) {
			c.logf("%q: %d MB, too big to check in the %d MB of memory free: left as it came", e.Title,
				st.Size()>>20, free>>20)
			rep.Note = "Too long to check in the Q2's free memory: left as it came"
			c.save(e, rep)
			c.uncutTranscript(ctx, e)
			return nil, nil
		}
	}
	s, err := mp3.ScanFile(part)
	if err != nil {
		c.logf("%q: not an MP3 (%v)", e.Title, err)
		rep.Note = "Not an MP3: left as it came"
		c.save(e, rep)
		c.uncutTranscript(ctx, e)
		return nil, nil
	}
	rep.Length = s.Duration()
	c.logf("%q: %d frames, %.0f minutes", e.Title, len(s.Frames), rep.Length/60)
	src := c.sources(ctx, e)
	rep.Sources = append(rep.Sources, src.used...)

	var dai []adfree.Cut
	if set.Ads {
		ev := adfree.Evidence{Opens: src.opens, Now: time.Now()}
		if e.Duration > 0 {
			ev.Budget = float64(e.Duration)
		}
		if set.Copy && e.URL != "" {
			note("Comparing with a second copy")
			if st, err := os.Stat(part); err == nil {
				ev.Copy, err = c.second(ctx, e.URL, st.Size())
				if err != nil && ctx.Err() == nil {
					rep.Sources = append(rep.Sources, "a second copy: "+err.Error())
				} else if ev.Copy != nil {
					rep.Sources = append(rep.Sources, "a second copy, with other adverts")
				}
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		note("Checking for ads")
		ev.Peers = c.peers(e)
		lib := adfree.LoadLibrary(filepath.Join(c.Dir, "ads.bin"))
		ev.Lib = lib
		c.logf("%q: comparing with %d other episodes, a second copy: %v", e.Title, len(ev.Peers), ev.Copy != nil)
		res := adfree.Detect(s, ev)
		c.logf("%q: %d ads found in %d parts", e.Title, len(res.Cuts), res.Parts)
		dai = res.Cuts
		for _, h := range res.Learn {
			lib.Add(h, ev.Now)
		}
		lib.Save()
	}

	// the episode's two timelines: as downloaded, and with the adverts cut
	original := s.Duration()
	kept := original
	for _, k := range dai {
		kept -= s.Seconds(k.From, k.To)
	}
	var spon []Cut
	if set.Sponsors {
		note("Looking for sponsor reads")
		spon = c.sponsors(ctx, s, e, set, src, dai, kept, original, rep, part)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// all of it, as seconds of the download and then as its frames
	for _, k := range dai {
		rep.Cuts = append(rep.Cuts, Cut{s.At(k.From), s.At(k.To), "ad", k.Why})
	}
	rep.Cuts = union(append(rep.Cuts, spon...))
	var ranges []mp3.Range
	for _, k := range rep.Cuts {
		ranges = append(ranges, mp3.Range{From: s.FrameAt(k.From), To: s.FrameAt(k.To)})
	}
	if len(src.cues) > 0 { // its transcript, moved with the audio: as downloaded, or as cut of its adverts
		dl := func(t float64) float64 { return t }
		switch sponsor.Fit(sponsor.End(src.cues), kept, original, true) {
		case sponsor.Clean:
			dl = inDownload(s, dai)
		case sponsor.Unplaced:
			dl = nil
		}
		if dl != nil {
			keepTranscript(e.Path, src.cues, func(t float64) (float64, bool) {
				t = dl(t)
				for _, k := range rep.Cuts {
					if t >= k.From && t < k.To {
						return 0, false
					}
				}
				return rep.Kept(t), true
			})
		}
	}
	os.MkdirAll(filepath.Join(c.Dir, "frames"), 0o755)
	adfree.SaveHashes(c.path("frames", e.Key, e.EID, ".bin"), adfree.FoldAll(s.Hashes()))
	c.prune()
	c.save(e, rep)
	return &Edit{Stream: s, Cuts: ranges, Report: rep, Chapters: c.chapters(ctx, src.chapters, kept)}, nil
}

// chapters is the episode's published chapters (Podcasting 2.0 JSON) as a tag's, ending at length
// (seconds); nil without two or more. Publishers time them on the episode without its adverts, which
// the cut episode is.
func (c *Cleaner) chapters(ctx context.Context, u string, length float64) []tag.Chapter {
	if u == "" {
		return nil
	}
	b, err := c.get(ctx, u)
	if err != nil {
		return nil
	}
	var out []tag.Chapter
	for _, ch := range sponsor.JSONChapters(b) {
		if ch.From < 0 || ch.From >= length {
			continue
		}
		k := tag.Chapter{Start: uint32(ch.From * 1000), Title: ch.Title}
		if ch.To > ch.From {
			k.End = uint32(math.Min(ch.To, length) * 1000)
		}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	if len(out) < 2 {
		return nil
	}
	return out
}

// NotesPath is where an episode's description is kept, as text: a hidden .description folder beside
// it, the episode's name with .txt (Q2 Pod's Now Playing shows it).
func NotesPath(episode string) string {
	base := filepath.Base(episode)
	return filepath.Join(filepath.Dir(episode), ".description", strings.TrimSuffix(base, filepath.Ext(base))+".txt")
}

// inDownload maps a second of the episode as cut of its adverts (dai) to the same second in the
// download.
func inDownload(s *mp3.Stream, dai []adfree.Cut) func(float64) float64 {
	return func(t float64) float64 {
		for _, k := range dai {
			if a := s.At(k.From); a <= t {
				t += s.Seconds(k.From, k.To)
			}
		}
		return t
	}
}

// TranscriptPath is where an episode's transcript is kept for the Q2: an .lrc beside it, its name
// with .lrc. Q2 Podcast's player reads it as the episode's lyrics, and Now Playing shows it as the
// transcript page, following the episode as it plays.
func TranscriptPath(episode string) string {
	return strings.TrimSuffix(episode, filepath.Ext(episode)) + ".lrc"
}

// captionMax is a transcript line's most bytes: the Q2 lays a line out at 34 px and keeps four rows
// of it, about this much.
const captionMax = 60

// keepTranscript writes cues as an .lrc beside the episode at path, each cue's time mapped by at to
// the episode's second on the card (a cue whose start was cut, not at all). A cue longer than
// captionMax is split at its words, its time shared out by length, so each line fits the Q2's page.
func keepTranscript(path string, cues []sponsor.Cue, at func(float64) (float64, bool)) {
	if path == "" {
		return
	}
	var b strings.Builder
	for _, c := range cues {
		from, ok := at(c.From)
		if !ok {
			continue
		}
		to, ok := at(c.To)
		if !ok || to < from {
			to = from + c.To - c.From
		}
		parts := captions(c.Text)
		total := 0
		for _, p := range parts {
			total += len(p)
		}
		done := 0
		for _, p := range parts {
			t := from + (to-from)*float64(done)/float64(max(total, 1))
			done += len(p)
			ms := int(math.Round(t * 1000))
			fmt.Fprintf(&b, "[%02d:%02d.%02d]%s\n", ms/60000, ms/1000%60, ms%1000/10, p)
		}
	}
	p := TranscriptPath(path)
	if b.Len() == 0 {
		return
	}
	if os.WriteFile(p+".tmp", []byte(b.String()), 0o644) == nil {
		os.Rename(p+".tmp", p)
	}
}

// captions is text as lines of at most captionMax bytes, broken between words (a word longer than
// that is a line of its own).
func captions(text string) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(text) {
		if line != "" && len(line)+1+len(w) > captionMax {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// Backfill writes the transcript of an episode already on the card (downloaded before transcripts
// were kept beside episodes), its times moved by the cuts its report has: an advert's (Kind "ad")
// for a transcript of the episode without them, then every cut. Nothing when the episode has no
// transcript, or one whose length is neither the download's nor its length without adverts; false
// when a download is being cleaned (one episode at a time): ask again later.
func (c *Cleaner) Backfill(ctx context.Context, e Episode) bool {
	if !c.mu.TryLock() {
		return false
	}
	defer c.mu.Unlock()
	if _, err := os.Stat(TranscriptPath(e.Path)); err == nil {
		return true
	}
	rep := Load(c.Dir, e.Key, e.EID)
	length := float64(e.Duration)
	if rep != nil && rep.Length > 0 {
		length = rep.Length
	}
	if length <= 0 {
		return true
	}
	src := c.sources(ctx, e)
	if len(src.cues) == 0 {
		return true
	}
	var ads []Cut
	adSec := 0.0
	if rep != nil {
		for _, k := range rep.Cuts {
			if k.Kind == "ad" {
				ads = append(ads, k)
				adSec += k.To - k.From
			}
		}
	}
	dl := func(t float64) float64 { return t }
	switch sponsor.Fit(sponsor.End(src.cues), length-adSec, length, true) {
	case sponsor.Unplaced:
		return true
	case sponsor.Clean:
		dl = func(t float64) float64 {
			for _, k := range ads { // in order: each advert before the point moves it on
				if k.From <= t {
					t += k.To - k.From
				}
			}
			return t
		}
	}
	keepTranscript(e.Path, src.cues, func(t float64) (float64, bool) {
		t = dl(t)
		if rep != nil {
			for _, k := range rep.Cuts {
				if t >= k.From && t < k.To {
					return 0, false
				}
			}
		}
		return rep.Kept(t), true
	})
	return true
}

// uncutTranscript keeps the transcript of an episode left as it came (not an MP3, or nothing to
// cut), when its length is the episode's (Fit against the length its feed gives).
func (c *Cleaner) uncutTranscript(ctx context.Context, e Episode) {
	if e.Duration <= 0 {
		return
	}
	src := c.sources(ctx, e)
	d := float64(e.Duration)
	if len(src.cues) == 0 || sponsor.Fit(sponsor.End(src.cues), d, d, true) == sponsor.Unplaced {
		return
	}
	keepTranscript(e.Path, src.cues, func(t float64) (float64, bool) { return t, true })
}

// keepNotes keeps an episode's description beside it, from Pocket Casts' show notes (the feed's
// description of the episode).
func (c *Cleaner) keepNotes(ctx context.Context, e Episode) {
	if e.Path == "" || e.Podcast == "" || e.UUID == "" || c.PC == nil {
		return
	}
	n, ok := c.showNotes(ctx, e.Podcast)[e.UUID]
	if !ok || strings.TrimSpace(n.Text) == "" {
		return
	}
	p := NotesPath(e.Path)
	if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
		text := e.Title + "\n" + e.Show + "\n\n" + NotesText(n.Text) + "\n"
		if os.WriteFile(p+".tmp", []byte(text), 0o644) == nil {
			os.Rename(p+".tmp", p)
		}
	}
}

var (
	paraEnd = regexp.MustCompile(`(?i)<\s*/(p|div|h[1-6]|ul|ol)\s*>`)
	lineEnd = regexp.MustCompile(`(?i)<\s*(br\s*/?|/li)\s*>`)
	anyTag  = regexp.MustCompile(`<[^>]*>`)
	blanks  = regexp.MustCompile(`[ \t\x{a0}]+`)
	paras   = regexp.MustCompile(`\s*\n\s*\n\s*`)
	lines   = regexp.MustCompile(` *\n *`)
)

// NotesText is a description's HTML as plain text: paragraphs and lines kept, tags and entities gone.
func NotesText(s string) string {
	s = paraEnd.ReplaceAllString(s, "\n\n")
	s = lineEnd.ReplaceAllString(s, "\n")
	s = html.UnescapeString(anyTag.ReplaceAllString(s, ""))
	s = blanks.ReplaceAllString(s, " ")
	s = paras.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(lines.ReplaceAllString(s, "\n"))
}

// union is cuts in order, overlapping ones joined (an advert's kind wins).
func union(cuts []Cut) []Cut {
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].From < cuts[j].From })
	var out []Cut
	for _, k := range cuts {
		if n := len(out); n > 0 && k.From <= out[n-1].To {
			out[n-1].To = math.Max(out[n-1].To, k.To)
			continue
		}
		out = append(out, k)
	}
	return out
}

func (c *Cleaner) save(e Episode, r *Report) {
	os.MkdirAll(filepath.Join(c.Dir, "cuts"), 0o755)
	b, _ := json.Marshal(r)
	p := c.path("cuts", e.Key, e.EID, ".json")
	if os.WriteFile(p+".tmp", b, 0o644) == nil {
		os.Rename(p+".tmp", p)
	}
}

// Load is the report of what was cut from an episode; nil when it wasn't checked.
func Load(dir, key, eid string) *Report {
	b, err := os.ReadFile(filepath.Join(dir, "cuts", key+"-"+eid+".json"))
	if err != nil {
		return nil
	}
	var r Report
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// Forget drops what is kept of an episode (it was deleted).
func Forget(dir, key, eid string) {
	os.Remove(filepath.Join(dir, "cuts", key+"-"+eid+".json"))
}

// Kept is where a second of the episode as downloaded is in it as cut (a cut second, where its cut
// was).
func (r *Report) Kept(at float64) float64 {
	if r == nil {
		return at
	}
	out := at
	for _, k := range r.Cuts {
		switch {
		case at >= k.To:
			out -= k.To - k.From
		case at > k.From:
			out -= at - k.From
		}
	}
	return math.Max(out, 0)
}

// Original is where a second of the episode as cut was in it as downloaded.
func (r *Report) Original(at float64) float64 {
	if r == nil {
		return at
	}
	out := at
	for _, k := range r.Cuts { // in order: each cut before the point moves it on
		if k.From <= out {
			out += k.To - k.From
		}
	}
	return out
}

// Removed is how many adverts and sponsor reads were cut, and their seconds.
func (r *Report) Removed() (ads, sponsors int, sec float64) {
	if r == nil {
		return 0, 0, 0
	}
	for _, k := range r.Cuts {
		if k.Kind == "ad" {
			ads++
		} else {
			sponsors++
		}
		sec += k.To - k.From
	}
	return
}

// Summary is the report in a few words, for an episode's row: "4 ads and 1 sponsor read cut (3
// min)", "No ads found", or why it wasn't checked.
func (r *Report) Summary() string {
	if r == nil {
		return ""
	}
	ads, sp, sec := r.Removed()
	if r.Note != "" && ads+sp == 0 {
		return r.Note
	}
	var parts []string
	if ads > 0 {
		parts = append(parts, plural(ads, "ad"))
	}
	if sp > 0 {
		parts = append(parts, plural(sp, "sponsor read"))
	}
	if len(parts) == 0 {
		return "No ads found"
	}
	m := int(math.Round(sec / 60))
	length := strconv.Itoa(m) + " min"
	if m < 1 {
		length = strconv.Itoa(int(math.Round(sec))) + " s"
	}
	return strings.Join(parts, " and ") + " cut (" + length + ")"
}

func plural(n int, s string) string {
	if n == 1 {
		return "1 " + s
	}
	return fmt.Sprintf("%d %ss", n, s)
}

// peers is other episodes' frames: the show's latest and others', the newest first.
func (c *Cleaner) peers(e Episode) []func() []uint64 {
	ents, _ := os.ReadDir(filepath.Join(c.Dir, "frames"))
	type f struct {
		name string
		at   time.Time
	}
	var all []f
	for _, d := range ents {
		if !strings.HasSuffix(d.Name(), ".bin") || d.Name() == e.Key+"-"+e.EID+".bin" {
			continue
		}
		if fi, err := d.Info(); err == nil {
			all = append(all, f{d.Name(), fi.ModTime()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	var out []func() []uint64
	same, other := 0, 0
	for _, x := range all {
		mine := strings.HasPrefix(x.name, e.Key+"-")
		if mine && same >= peersSame || !mine && other >= peersOther {
			continue
		}
		if mine {
			same++
		} else {
			other++
		}
		path := filepath.Join(c.Dir, "frames", x.name)
		out = append(out, func() []uint64 {
			h, _ := adfree.LoadHashes(path) // nil if it can't be read: passed over
			return h
		})
	}
	return out
}

// prune keeps the newest framesKept episodes' frames.
func (c *Cleaner) prune() {
	dir := filepath.Join(c.Dir, "frames")
	ents, _ := os.ReadDir(dir)
	if len(ents) <= framesKept {
		return
	}
	type f struct {
		name string
		at   time.Time
	}
	var all []f
	for _, d := range ents {
		if fi, err := d.Info(); err == nil {
			all = append(all, f{d.Name(), fi.ModTime()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	for _, x := range all[min(framesKept, len(all)):] {
		os.Remove(filepath.Join(dir, x.name))
	}
}

var errSame = errors.New("the server sent the same adverts")

// second fetches another copy of the episode when the server would send one with other adverts,
// which its size tells first (a request for one byte): its frames' folded hashes, or nil.
func (c *Cleaner) second(ctx context.Context, u string, size int64) ([]uint64, error) {
	for _, agent := range []string{c.Agent + " (second copy)", c.Agent + " (third copy)"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", agent)
		req.Header.Set("Range", "bytes=0-0")
		resp, err := c.API.Do(req)
		if err != nil {
			return nil, errors.New("couldn't reach the server")
		}
		total := int64(-1)
		switch resp.StatusCode {
		case http.StatusPartialContent:
			if i := strings.LastIndexByte(resp.Header.Get("Content-Range"), '/'); i >= 0 {
				total, _ = strconv.ParseInt(strings.TrimSpace(resp.Header.Get("Content-Range")[i+1:]), 10, 64)
			}
		case http.StatusOK: // no ranges: this is the copy
			total = resp.ContentLength
			if total != size {
				defer resp.Body.Close()
				return scanCopy(resp)
			}
		}
		resp.Body.Close()
		if total == size || total <= 0 {
			continue
		}
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", agent)
		resp, err = c.Fetch.Do(req)
		if err != nil {
			return nil, errors.New("couldn't fetch it")
		}
		defer resp.Body.Close()
		return scanCopy(resp)
	}
	return nil, nil
}

func scanCopy(resp *http.Response) ([]uint64, error) {
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered HTTP %d", resp.StatusCode)
	}
	h, err := mp3.ScanHashes(resp.Body)
	if err != nil {
		return nil, errors.New("it wasn't an MP3")
	}
	return h, nil
}

// found is what an episode has to look in for sponsor reads.
type found struct {
	cues     []sponsor.Cue
	opens    bool // the transcript opens with a sponsor read
	chapters string
	links    []string
	used     []string
}

func (c *Cleaner) get(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if c.PC != nil {
		return c.PC.Get(ctx, u, 16<<20)
	}
	return nil, errors.New("no client")
}

// sources gathers the episode's transcript, chapters and links: its feed's, or Pocket Casts' show
// notes (the publisher's, then Pocket Casts' own transcript).
func (c *Cleaner) sources(ctx context.Context, e Episode) found {
	var f found
	f.chapters, f.links = e.Chapters, e.Links
	tr := []pocketcasts.Transcript{}
	if e.Transcript != "" {
		tr = append(tr, pocketcasts.Transcript{URL: e.Transcript, Type: e.TranscriptType})
	}
	if e.Podcast != "" && e.UUID != "" && c.PC != nil {
		if n, ok := c.showNotes(ctx, e.Podcast)[e.UUID]; ok {
			tr = append(tr, n.Transcripts...)
			tr = append(tr, n.Generated...)
			if f.chapters == "" {
				f.chapters = n.ChaptersURL
			}
			yt, sp := sponsor.Links(n.Text)
			for _, id := range yt {
				f.links = append(f.links, "yt:"+id)
			}
			for _, id := range sp {
				f.links = append(f.links, "sp:"+id)
			}
		}
	}
	for _, t := range tr {
		if strings.Contains(t.Type, "html") || t.URL == "" {
			continue
		}
		b, err := c.get(ctx, t.URL)
		if err != nil {
			continue
		}
		if cues := sponsor.Transcript(b, t.Type); len(cues) > 20 {
			f.cues = cues
			reads := sponsor.Reads(cues)
			f.opens = len(reads) > 0 && reads[0].From <= 5
			f.used = append(f.used, "a transcript")
			break
		}
	}
	return f
}

func (c *Cleaner) showNotes(ctx context.Context, podcast string) map[string]pocketcasts.Notes {
	if n, ok := c.notes[podcast]; ok && time.Since(n.at) < notesFor {
		return n.n
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	n, err := c.PC.ShowNotes(ctx, podcast)
	if err != nil {
		return nil
	}
	if c.notes == nil {
		c.notes = map[string]notesAt{}
	}
	c.notes[podcast] = notesAt{time.Now(), n}
	return n
}

// sponsors is the sponsor reads found, in seconds of the download: SponsorBlock's for the episode's
// Spotify episode and YouTube video, its chapters named as adverts, and the reads its transcript
// has, each used only when its timeline is the episode's (cut or not).
func (c *Cleaner) sponsors(ctx context.Context, s *mp3.Stream, e Episode, set Settings, src found, dai []adfree.Cut,
	kept, original float64, rep *Report, part string) []Cut {
	orig := inDownload(s, dai)
	var out []Cut
	add := func(segs []sponsor.Segment, timeline int) bool {
		if timeline == sponsor.Unplaced || len(segs) == 0 {
			return false
		}
		for _, sg := range segs {
			if d := sg.To - sg.From; d < minSponsor || d > maxSponsor {
				continue
			}
			from, to := sg.From, sg.To
			if timeline == sponsor.Clean {
				from, to = orig(from), orig(to)
			}
			out = append(out, Cut{from, math.Min(to, original), "sponsor", sg.Why})
		}
		return true
	}
	cats := []string{sponsor.CatSponsor}
	if set.SelfPromo {
		cats = append(cats, sponsor.CatSelfPromo)
	}
	sb := &sponsor.SponsorBlock{HTTP: c.API, Agent: c.Agent}
	ask := func(service, id string, length float64) bool {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		segs, ref, err := sb.Segments(ctx, service, id, cats)
		if err != nil {
			rep.Sources = append(rep.Sources, "SponsorBlock: "+err.Error())
			return false
		}
		if ref == 0 {
			ref = length
		}
		if len(segs) == 0 {
			return false
		}
		if add(segs, sponsor.Fit(ref, kept, original, false)) {
			rep.Sources = append(rep.Sources, "SponsorBlock ("+service+")")
			return true
		}
		rep.Sources = append(rep.Sources, fmt.Sprintf("SponsorBlock (%s): its %s is another length", service, map[string]string{"YouTube": "video", "Spotify": "episode"}[service]))
		return false
	}
	// Spotify (Spot SponsorBlock): its show notes' episode, else one found with the user's app
	done := false
	var sp, yt []string
	for _, l := range src.links {
		if strings.HasPrefix(l, "sp:") {
			sp = append(sp, l[3:])
		} else if strings.HasPrefix(l, "yt:") {
			yt = append(yt, l[3:])
		}
	}
	for _, id := range sp[:min(len(sp), 2)] {
		if done = ask("Spotify", id, 0); done {
			break
		}
	}
	if !done && set.SpotifyID != "" && set.SpotifySecret != "" {
		spot := &sponsor.Spotify{ID: set.SpotifyID, Secret: set.SpotifySecret, HTTP: c.API, Agent: c.Agent}
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		m, err := spot.Find(fctx, e.Show, e.Title, e.Date, kept)
		cancel()
		if err != nil {
			rep.Sources = append(rep.Sources, "Spotify: "+err.Error())
		} else if m.ID != "" {
			done = ask("Spotify", m.ID, m.Length)
		}
	}
	// YouTube: its show notes' videos, else one found with the user's key
	for _, id := range yt[:min(len(yt), 3)] {
		if ask("YouTube", id, 0) {
			done = true
			break
		}
	}
	if !done && set.YouTubeKey != "" {
		y := &sponsor.YouTube{Key: set.YouTubeKey, HTTP: c.API, Agent: c.Agent}
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		m, err := y.Find(fctx, e.Show, e.Title, e.Date, kept)
		cancel()
		if err != nil {
			rep.Sources = append(rep.Sources, "YouTube: "+err.Error())
		} else if m.ID != "" {
			ask("YouTube", m.ID, m.Length)
		}
	}
	// chapters: the download's own (its timeline), or the feed's when no advert was cut (both the same)
	if h, err := tag.Read(part); err == nil {
		var ch []sponsor.Chapter
		for _, k := range h.Chapters() {
			ch = append(ch, sponsor.Chapter{From: float64(k.Start) / 1000, To: float64(k.End) / 1000, Title: k.Title})
		}
		if add(sponsor.AdChapters(ch, original), sponsor.Original) {
			rep.Sources = append(rep.Sources, "its chapters")
		}
	}
	if src.chapters != "" && len(dai) == 0 {
		if b, err := c.get(ctx, src.chapters); err == nil && add(sponsor.AdChapters(sponsor.JSONChapters(b), original), sponsor.Original) {
			rep.Sources = append(rep.Sources, "its chapters")
		}
	}
	// the transcript's reads
	if len(src.cues) > 0 {
		add(sponsor.Reads(src.cues), sponsor.Fit(sponsor.End(src.cues), kept, original, true))
	}
	// a source gone wrong shouldn't take a quarter of the episode
	total := 0.0
	out = union(out)
	for _, k := range out {
		total += k.To - k.From
	}
	if total > maxSponsorShare*kept {
		rep.Sources = append(rep.Sources, "sponsor segments left: they came to too much of the episode")
		return nil
	}
	return out
}

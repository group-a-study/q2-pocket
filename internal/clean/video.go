package clean

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"q2-pocket/internal/sponsor"
	"q2-pocket/internal/tag"
	"q2-pocket/internal/youtube"
)

// An episode with a YouTube video is fetched from YouTube when its video can be checked (the user's,
// 2026-11-17: "for any podcast episodes with youtube id, use the code in yt-dlp or its port to
// download the episode and apply sponsorblock ... specifically the Lex Fridman podcast"). The video
// has no adverts stitched in, and every source of sponsor reads is timed to it: SponsorBlock, its
// captions, its description's chapters. Lex Fridman's videos leave out the sponsor reads his podcast
// has, saying only "please check out our sponsors in the description", which sponsor.Mentions finds.

// VideoCopy is an episode's YouTube video and what is to be cut from it, found before it is fetched.
type VideoCopy struct {
	Video  *youtube.Video
	Format youtube.Format
	Found  string        // where its id came from: "its page", "its show notes", "a YouTube search"
	Cuts   []Cut         // seconds of the video
	Cues   []sponsor.Cue // its captions, for the transcript beside it
	Chaps  []sponsor.Chapter
	Used   []string // the report's sources
	Shift  float64  // seconds its feed's copy runs longer (Report.Shift)
}

const (
	videoPage  = 2 << 20 // bytes of an episode's web page read for its video
	videoTries = 3       // videos asked about for one episode
)

// FindVideo is the episode's YouTube video, checked and its cuts found, ready to be fetched; nil
// when it has none, or none to use (why says, "" when there was nothing to look at).
func (c *Cleaner) FindVideo(ctx context.Context, e Episode) (*VideoCopy, string) {
	if c.YouTube == nil {
		return nil, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ids, why := c.videoIDs(ctx, e)
	if len(ids) == 0 {
		return nil, why
	}
	var v *youtube.Video
	var found string
	for _, id := range ids[:min(len(ids), videoTries)] {
		vctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		got, err := c.YouTube.Video(vctx, id.id)
		cancel()
		if err != nil {
			why = "YouTube: " + err.Error()
			c.logf("%q: YouTube video %s: %v", e.Title, id.id, err)
			if errors.Is(err, youtube.ErrRefused) || ctx.Err() != nil {
				return nil, why
			}
			continue
		}
		if ok, no := sameEpisode(e, got, id.page); !ok {
			why = "its YouTube video " + id.id + " " + no
			c.logf("%q: YouTube video %s (%q, %.0f s) isn't it: %s", e.Title, id.id, got.Title, got.Length, no)
			continue
		}
		v, found = got, id.from
		break
	}
	if v == nil {
		return nil, why
	}
	f, ok := v.Pick()
	if !ok {
		return nil, "its YouTube video has no audio the Q2 plays"
	}
	vc := &VideoCopy{Video: v, Format: f, Found: found}
	vc.Used = append(vc.Used, fmt.Sprintf("the episode's YouTube video (from %s), which has no adverts put in", found))
	if e.Duration > 0 {
		if d := float64(e.Duration) - v.Length; math.Abs(d) <= 30*60 {
			vc.Shift = d
		}
	}
	if !c.planVideo(ctx, e, vc) {
		return nil, "nothing could check its YouTube video for sponsor reads (no captions, nothing in SponsorBlock)"
	}
	return vc, ""
}

type videoID struct {
	id, from string
	page     bool // on the episode's own page: its video, as its publisher shows it
}

// videoIDs is the videos the episode may be: those its own web page embeds (its link in Pocket
// Casts' show notes), then those its show notes link to, then a search with the user's YouTube key.
func (c *Cleaner) videoIDs(ctx context.Context, e Episode) ([]videoID, string) {
	var out []videoID
	seen := map[string]bool{}
	add := func(id, from string, page bool) {
		if !seen[id] {
			seen[id] = true
			out = append(out, videoID{id, from, page})
		}
	}
	var notes []string
	for _, l := range e.Links {
		if strings.HasPrefix(l, "yt:") {
			notes = append(notes, l[3:])
		}
	}
	if e.Podcast != "" && e.UUID != "" && c.PC != nil {
		if n, ok := c.episodeNotes(ctx, e.Podcast, e.UUID); ok {
			yt, _ := sponsor.Links(n.Text)
			notes = append(notes, yt...)
			if u := strings.TrimSpace(n.Link); strings.HasPrefix(u, "http") {
				pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				page, err := c.PC.Get(pctx, u, videoPage)
				cancel()
				if err == nil {
					for _, id := range youtube.PageVideos(page) {
						add(id, "its page", true)
					}
				}
			}
		}
	}
	for _, id := range notes {
		add(id, "its show notes", false)
	}
	if len(out) == 0 {
		if set := c.Settings(); set.YouTubeKey != "" {
			y := &sponsor.YouTube{Key: set.YouTubeKey, HTTP: c.API, Agent: c.Agent}
			fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			m, err := y.Find(fctx, e.Show, e.Title, e.Date, float64(e.Duration))
			cancel()
			if err != nil {
				return nil, "YouTube search: " + err.Error()
			}
			if m.ID != "" {
				add(m.ID, "a YouTube search", true)
			}
		}
	}
	return out, ""
}

// sameEpisode is whether video v is episode e: a video on the episode's own page (page) only has to
// be as long as it could be (its podcast's copy may hold sponsor reads the video doesn't: a
// quarter of it, or 15 minutes, at most), one its notes link to has to be titled alike too, or be
// as long within a minute.
func sameEpisode(e Episode, v *youtube.Video, page bool) (bool, string) {
	if v.Length < 60 {
		return false, "is too short"
	}
	d := float64(e.Duration)
	fits := d <= 0 || v.Length >= d-math.Max(0.25*d, 15*60)-60 && v.Length <= 1.05*d+120
	if !fits {
		return false, fmt.Sprintf("is %.0f minutes long, the episode %.0f", v.Length/60, d/60)
	}
	if page {
		return true, ""
	}
	if sponsor.Alike(e.Title, v.Title) >= 0.5 || d > 0 && math.Abs(v.Length-d) <= 60 {
		return true, ""
	}
	return false, "has another title: " + v.Title
}

// planVideo finds what to cut from the video, in its own seconds: SponsorBlock's segments, the
// sponsor reads and pointers its captions have, its description's chapters named as sponsors, and
// the show notes' timestamps and the feed's chapters when the episode is the video's length. False
// when nothing could check it (no captions, and SponsorBlock has nothing for it): the feed's copy
// is checked instead, its adverts and all.
func (c *Cleaner) planVideo(ctx context.Context, e Episode, vc *VideoCopy) bool {
	v := vc.Video
	set := c.Settings()
	var segs []sponsor.Segment
	checked := false
	cats := []string{sponsor.CatSponsor}
	if set.SelfPromo {
		cats = append(cats, sponsor.CatSelfPromo)
	}
	sb := &sponsor.SponsorBlock{HTTP: c.API, Agent: c.Agent}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	got, ref, err := sb.Segments(sctx, "YouTube", v.ID, cats)
	cancel()
	switch {
	case err != nil:
		vc.Used = append(vc.Used, "SponsorBlock: "+err.Error())
	case len(got) > 0 && ref > 0 && math.Abs(ref-v.Length) > max(3, 0.003*v.Length):
		vc.Used = append(vc.Used, "SponsorBlock: its segments are for another cut of the video")
	case len(got) > 0:
		segs = append(segs, got...)
		checked = true
		vc.Used = append(vc.Used, "SponsorBlock (YouTube)")
	}
	if t, ok := v.Caption(); ok {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		b, err := c.YouTube.Captions(cctx, t)
		cancel()
		if err != nil {
			vc.Used = append(vc.Used, "its captions: "+err.Error())
		} else if cues := sponsor.Transcript(b, "application/json"); len(cues) > 20 {
			vc.Cues = cues
			segs = append(segs, sponsor.Reads(cues)...)
			if words := sponsor.Words(b); len(words) > 0 { // pointers to the sponsors, to the word
				segs = append(segs, sponsor.Mentions(words)...)
			} else {
				segs = append(segs, sponsor.Mentions(cues)...)
			}
			checked = true
			kind := "its captions"
			if t.Auto {
				kind = "its captions (YouTube's speech recognition)"
			}
			vc.Used = append(vc.Used, kind)
		}
	}
	if ch := sponsor.NotesChapters(v.Description); len(ch) > 0 {
		vc.Chaps = ch
		if ads := sponsor.AdChapters(ch, v.Length); len(ads) > 0 {
			segs = append(segs, ads...)
			vc.Used = append(vc.Used, "its YouTube description's chapters")
		}
	}
	// the podcast's own: only when its copy is the video's length (then both are the same)
	if e.Duration > 0 && sponsor.Fit(float64(e.Duration), v.Length, v.Length, false) != sponsor.Unplaced {
		if e.Podcast != "" && e.UUID != "" && c.PC != nil {
			n, _ := c.episodeNotes(ctx, e.Podcast, e.UUID)
			if ch := sponsor.NotesChapters(NotesText(n.Text)); len(ch) > 0 {
				if ads := sponsor.AdChapters(ch, v.Length); len(ads) > 0 {
					segs = append(segs, ads...)
					vc.Used = append(vc.Used, "its show notes' timestamps")
				}
			}
		}
		if e.Chapters != "" {
			if b, err := c.get(ctx, e.Chapters); err == nil {
				if ads := sponsor.AdChapters(sponsor.JSONChapters(b), v.Length); len(ads) > 0 {
					segs = append(segs, ads...)
					vc.Used = append(vc.Used, "its chapters")
				}
			}
		}
	}
	if !checked {
		return false
	}
	var cuts []Cut
	for _, s := range segs {
		if d := s.To - s.From; d < minLength(s) || d > maxSponsor || s.From >= v.Length {
			continue
		}
		cuts = append(cuts, Cut{s.From, math.Min(s.To, v.Length), "sponsor", s.Why})
	}
	cuts = union(cuts)
	total := 0.0
	for _, k := range cuts {
		total += k.To - k.From
	}
	if total > maxSponsorShare*v.Length {
		vc.Used = append(vc.Used, "sponsor segments left: they came to too much of the episode")
		cuts = nil
	}
	vc.Cuts = cuts
	return true
}

// VideoDone records a video's copy of the episode, length seconds long as fetched: its report
// (its cuts, the video's id, Shift), its transcript beside it (its captions, moved with the cuts),
// its description; and gives the chapters it is to have (its description's, moved with the cuts;
// milliseconds of the copy as cut).
func (c *Cleaner) VideoDone(ctx context.Context, e Episode, vc *VideoCopy, length float64) (*Report, []tag.Chapter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rep := &Report{Checked: time.Now(), Length: length, Video: vc.Video.ID, Shift: vc.Shift,
		Sources: append([]string(nil), vc.Used...)}
	for _, k := range vc.Cuts {
		if k.From < length {
			k.To = math.Min(k.To, length)
			rep.Cuts = append(rep.Cuts, k)
		}
	}
	c.keepNotes(ctx, e)
	if len(vc.Cues) > 0 {
		os.Remove(TranscriptPath(e.Path)) // an older copy's
		keepTranscript(e.Path, vc.Cues, func(t float64) (float64, bool) {
			for _, k := range rep.Cuts {
				if t >= k.From && t < k.To {
					return 0, false
				}
			}
			return rep.Kept(t), true
		})
	}
	os.MkdirAll(filepath.Join(c.Dir, "cuts"), 0o755)
	c.save(e, rep)
	_, _, cut := rep.Removed()
	end := length - cut
	var chapters []tag.Chapter
	for _, ch := range vc.Chaps {
		if ch.From >= length || sponsorChapter(ch.Title) {
			continue
		}
		at := rep.Kept(ch.From)
		if n := len(chapters); n > 0 && uint32(at*1000) <= chapters[n-1].Start {
			continue // all of the one before was cut
		}
		chapters = append(chapters, tag.Chapter{Start: uint32(at * 1000), Title: ch.Title})
	}
	for i := range chapters {
		if i+1 < len(chapters) {
			chapters[i].End = chapters[i+1].Start
		} else {
			chapters[i].End = uint32(end * 1000)
		}
	}
	if len(chapters) < 2 {
		chapters = nil
	}
	return rep, chapters
}

// sponsorChapter is whether a chapter's title names it as sponsors (its audio cut).
func sponsorChapter(title string) bool {
	return len(sponsor.AdChapters([]sponsor.Chapter{{From: 0, To: 1, Title: title}}, 1)) > 0
}

// FromFeed is where a second of the episode as its feed (and Pocket Casts) has it is in the copy on
// the card: for a video's copy, the seconds its feed's copy runs longer taken off first (they are
// the sponsor reads near its start, which the video leaves out).
func (r *Report) FromFeed(at float64) float64 {
	if r != nil && r.Shift != 0 {
		at = math.Max(at-r.Shift, 0)
	}
	return r.Kept(at)
}

// ToFeed is where a second of the copy on the card is in the episode as its feed has it.
func (r *Report) ToFeed(at float64) float64 {
	out := r.Original(at)
	if r != nil && r.Shift != 0 && out > 0 {
		out = math.Max(out+r.Shift, 0)
	}
	return out
}

// Shown is whether an episode's row shows its report's summary: something was cut from it, or it is
// its YouTube video's copy ("From YouTube: no sponsor reads found").
func (r *Report) Shown() bool {
	if r == nil {
		return false
	}
	ads, sp, _ := r.Removed()
	return ads+sp > 0 || r.Video != ""
}

// minLength is the shortest a sponsor segment may be cut: a pointer to the sponsors is short.
func minLength(s sponsor.Segment) float64 {
	if sponsor.IsPointer(s) {
		return sponsor.MinPointer
	}
	return minSponsor
}

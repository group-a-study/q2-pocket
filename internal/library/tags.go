package library

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/tag"
)

// The Q2's Now Playing shows a file's own tags, and a feed's MP3s often have none (Patreon's): an
// episode is tagged as it lands on the card (place), and those that came before without tags are
// tagged in the background (Retag). Either way the tagged file is a copy, its audio as it came, put
// in place by a rename.

var errTagged = errors.New("tagged already")

// covers keeps the covers fetched lately, by address: an episode's file and the next of its show
// share one. nil when it couldn't be had, so it isn't asked for again this run.
type covers struct {
	mu    sync.Mutex
	got   map[string][]byte
	order []string
}

const coversKept = 16

func (l *Library) cover(ctx context.Context, u string) []byte {
	if u == "" {
		return nil
	}
	c := &l.covers
	c.mu.Lock()
	b, ok := c.got[u]
	c.mu.Unlock()
	if ok {
		return b
	}
	b = tag.Cover(ctx, l.o.Client, u, l.o.Agent)
	if ctx.Err() != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.got == nil {
		c.got = map[string][]byte{}
	}
	if len(c.order) == coversKept {
		delete(c.got, c.order[0])
		c.order = c.order[1:]
	}
	c.got[u] = b
	c.order = append(c.order, u)
	return b
}

func isMP3(path string) bool { return strings.EqualFold(filepath.Ext(path), ".mp3") }

// tmpOf is where path's tagged copy is written: hidden beside it, out of the Q2's lists.
func tmpOf(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tag")
}

// tagTo writes src, tagged for e, to dst: errTagged when its tag has all it needs already (or is
// too big to keep), tag.ErrNotMP3 when it isn't one. A panic reading a file (a tag broken some way
// not foreseen) is an error, not the app's end: Retag would meet the file again at each start.
func (l *Library) tagTo(ctx context.Context, src, dst string, e EpisodeInfo) (err error) {
	h, info, cover, err := l.retag(ctx, src, e)
	if err != nil {
		return err
	}
	if st, err := os.Stat(src); err == nil && !roomFor(dst, st.Size()) { // no room for a copy: tagged where it is
		os.Remove(src + ".json")
		if err := shiftInPlace(src, h.Build(info, cover), h.Len); err != nil {
			return fmt.Errorf("%w: %v", errPartBroken, err)
		}
		return errInPlace
	}
	return tag.Write(src, dst, info, cover)
}

// retag is the head of the MP3 at path, what it is to be tagged with, and the cover it is to have
// (nil to keep its own): errTagged when it has all it needs already (or a tag too big to keep).
func (l *Library) retag(ctx context.Context, path string, e EpisodeInfo) (h *tag.Head, info tag.Info, cover []byte, err error) {
	defer func() {
		if p := recover(); p != nil {
			h, err = nil, fmt.Errorf("can't read its tag: %v", p)
		}
	}()
	if h, err = tag.Read(path); err != nil {
		return nil, info, nil, err
	}
	info = tag.Info{Title: e.Title, Show: e.Show, Author: e.Author, Date: e.Date}
	if h.Big() || !h.Needs(info, false) && h.HasCover() && !h.OddCover() {
		return nil, info, nil, errTagged
	}
	cover = l.coverFor(ctx, h, e)
	if !h.Needs(info, cover != nil) {
		return nil, info, nil, errTagged
	}
	return h, info, cover, nil
}

// coverFor is the cover a tag is to have, nil to keep its own: for none, the feed's; for one Q2
// Pod's lists can't show (a PNG: the user's, 2026-11-16, Very Bad Wizards' rows had no art), its
// own as a JPEG, else the feed's.
func (l *Library) coverFor(ctx context.Context, h *tag.Head, e EpisodeInfo) []byte {
	if h.HasCover() && !h.OddCover() {
		return nil
	}
	if h.OddCover() {
		if b := tag.Shrink(h.Picture()); b != nil {
			return b
		}
	}
	return l.cover(ctx, e.Image)
}

// errUncut: nothing to cut from a download (or no cleaner); it is only tagged.
var errUncut = errors.New("nothing to cut")

// cleanTo writes the download at part to dst without the adverts and sponsor reads the cleaner
// finds, tagged as tagTo does, its chapters moved with the audio; errUncut when there are none.
func (l *Library) cleanTo(ctx context.Context, part, dst string, e EpisodeInfo, note func(string)) (err error) {
	if l.o.Clean == nil {
		return errUncut
	}
	if _, crashed := l.plain.Load(e.Key + ":" + e.EID); crashed { // the app crashed checking it: tagged only
		// marked Ads Not Removed (the user's rule, 2026-11-16: a whole check or none, and those
		// without one found and fetched again later)
		note("Left as it came: Pocket crashed checking it for ads")
		l.o.Clean.Unchecked(cleanEpisode(e), "Ads not removed: Pocket crashed checking it for ads")
		return errUncut
	}
	defer func() {
		if p := recover(); p != nil { // a file broken some way not foreseen: it goes in uncut
			os.Remove(dst)
			l.o.Clean.Unchecked(cleanEpisode(e), "Ads not removed: Pocket couldn't read this episode to check it")
			err = errUncut
		}
	}()
	ed, err := l.o.Clean.Clean(ctx, part, cleanEpisode(e), note)
	if err != nil || ctx.Err() != nil {
		return cmp.Or(ctx.Err(), err)
	}
	if ed == nil || len(ed.Cuts) == 0 && len(ed.Chapters) == 0 {
		return errUncut
	}
	if len(ed.Cuts) > 0 {
		note("Cutting the ads out")
	}
	var head []byte
	if h, err := tag.Read(part); err == nil && !h.Big() {
		h.Retime(func(ms uint32) uint32 { return uint32(ed.Report.Kept(float64(ms)/1000) * 1000) })
		_, _, cut := ed.Report.Removed()
		h.AddChapters(ed.Chapters, uint32((ed.Report.Length-cut)*1000)) // the publisher's, when it has none
		head = h.Build(tag.Info{Title: e.Title, Show: e.Show, Author: e.Author, Date: e.Date}, l.coverFor(ctx, h, e))
	} else if f, err := os.Open(part); err == nil { // its own tag, as it was (only its bytes read)
		head = make([]byte, ed.Stream.TagLen)
		_, err = io.ReadFull(f, head)
		f.Close()
		if err != nil {
			return err
		}
	}
	if st, err := os.Stat(part); err == nil && !roomFor(dst, st.Size()) {
		// no room for a cut copy beside it (the user's, 2026-11-16: on a full card an episode went in
		// as it came, its ads and its own tag's title in): cut where it is
		note("Cutting the ads out")
		if err := cutInPlace(part, head, ed); err != nil {
			return fmt.Errorf("%w: %v", errPartBroken, err)
		}
		return errInPlace
	}
	in, err := os.Open(part)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = ed.Stream.Write(in, out, head, ed.Cuts)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		l.o.Clean.Uncut(cleanEpisode(e), "Couldn't write the cut episode: left as it came")
		return errUncut
	}
	return nil
}

// errInPlace: the download was cut or tagged where it is, its part itself, for want of room on the
// card for a copy: the part goes into its place as it is now.
var errInPlace = errors.New("done in place")

// errPartBroken: cutting or tagging a part where it is failed part way, so the part is neither the
// download nor the episode: it is fetched again.
var errPartBroken = errors.New("the download broke as it was cut")

// roomFor is whether the card holding path has room for size more bytes, its reserve kept.
func roomFor(path string, size int64) bool {
	free, ok := freeBytes(filepath.Dir(path))
	return !ok || free >= size+reserve
}

// cutInPlace cuts ed's frames out of part where it is and gives it head for its tag: first the
// audio, under its own tag as it is (so nothing written passes what is still to be read), then the
// whole moved to fit head (shiftInPlace). Its .json goes first: a part broken part way by an ending
// app is never taken for a whole download, but fetched again.
func cutInPlace(part string, head []byte, ed *clean.Edit) error {
	os.Remove(part + ".json")
	f, err := os.OpenFile(part, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	own := make([]byte, ed.Stream.TagLen)
	if _, err = f.ReadAt(own, 0); err == nil {
		_, err = ed.Stream.Write(f, f, own, ed.Cuts)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return shiftInPlace(part, head, int64(len(own)))
}

// shiftInPlace makes the file at path head followed by what it holds from from on, where it is: the
// rest moved toward its end from the last byte back (head longer than from), or toward its start
// from the first on, the file cut short after (shorter); a megabyte held at a time, its times kept.
func shiftInPlace(path string, head []byte, from int64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	size, d := st.Size(), int64(len(head))-from
	buf := make([]byte, 1<<20)
	move := func(at int64, n int) error {
		if _, err := f.ReadAt(buf[:n], at); err != nil {
			return err
		}
		_, err := f.WriteAt(buf[:n], at+d)
		return err
	}
	switch {
	case d > 0:
		for end := size; end > from && err == nil; {
			start := max(from, end-int64(len(buf)))
			err = move(start, int(end-start))
			end = start
		}
	case d < 0:
		for at := from; at < size && err == nil; at += int64(len(buf)) {
			err = move(at, int(min(int64(len(buf)), size-at)))
		}
		if err == nil {
			err = f.Truncate(size + d)
		}
	}
	if err == nil {
		_, err = f.WriteAt(head, 0)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	os.Chtimes(path, st.ModTime(), st.ModTime())
	return err
}

// Transcript writes the transcript beside an episode already on the card, if it has one and has
// none there yet (clean.Cleaner.Backfill); false when that has to wait.
func (l *Library) Transcript(ctx context.Context, e EpisodeInfo) bool {
	if l.o.Clean == nil || !e.OnCard {
		return true
	}
	return l.o.Clean.Backfill(ctx, cleanEpisode(e))
}

// cleanEpisode is an episode as the cleaner takes it.
func cleanEpisode(e EpisodeInfo) clean.Episode {
	f := e.Feed
	return clean.Episode{Key: e.Key, EID: e.EID, Path: e.Path, Show: e.Show, Title: e.Title, Author: e.Author, Date: e.Date,
		Duration: e.Duration, URL: f.URL, Podcast: e.PC, UUID: f.PC, Transcript: f.Transcript,
		TranscriptType: f.TranscriptType, Chapters: f.Chapters, Links: f.Links}
}

// place moves a whole download into its show's folder: an MP3 cut of its adverts and tagged on the
// way, anything else as it came. Cancelled meanwhile (Delete's, maybe), it puts nothing there.
func (l *Library) place(ctx context.Context, part string, e EpisodeInfo, note func(string)) error {
	if err := os.MkdirAll(filepath.Dir(e.Path), 0o755); err != nil {
		return fmt.Errorf("can't make the show's folder: %v", err)
	}
	src := part
	if isMP3(e.Path) {
		tmp := tmpOf(e.Path)
		err := l.cleanTo(ctx, part, tmp, e, note)
		if errors.Is(err, errUncut) {
			err = l.tagTo(ctx, part, tmp, e)
		}
		switch {
		case err == nil:
			src = tmp
		case errors.Is(err, errInPlace): // the part itself, cut or tagged
		case errors.Is(err, tag.ErrNotMP3), errors.Is(err, errTagged), errors.Is(err, errUncut):
		case errors.Is(err, errPartBroken):
			removePart(part)
			return err
		case ctx.Err() != nil:
			os.Remove(tmp)
			return ctx.Err()
		default: // the tagged copy failed some other way: it goes in untagged, for Retag
			os.Remove(tmp)
		}
	}
	l.fileMu.Lock() // a Delete's cancel is seen here, or its remove comes after
	if err := ctx.Err(); err != nil {
		l.fileMu.Unlock()
		os.Remove(tmpOf(e.Path))
		return err
	}
	err := os.Rename(src, e.Path)
	l.fileMu.Unlock()
	if err != nil {
		os.Remove(tmpOf(e.Path))
		return fmt.Errorf("can't move the episode into place: %v", err)
	}
	removePart(part)
	if !e.Date.IsZero() {
		os.Chtimes(e.Path, e.Date, e.Date) // newest first where the Q2 sorts by date
	}
	return nil
}

// Retag tags the MP3 episodes on the card that lack a title, an artist, an album or (when the feed
// has one) a cover Q2 Pod's lists can show, or whose title isn't the episode's, one at a time until
// ctx ends; how many it tagged. A new tag that fits in the old one's room is written over it in
// place; else the file is copied with it.
func (l *Library) Retag(ctx context.Context) int {
	l.retagging.Store(true)
	defer l.retagging.Store(false)
	eps, _ := l.Downloaded()
	n := 0
	for _, e := range eps {
		if ctx.Err() != nil {
			break
		}
		if !isMP3(e.Path) {
			continue
		}
		h, info, cover, err := l.retag(ctx, e.Path, e)
		if err != nil {
			continue
		}
		if head := h.BuildIn(info, cover, h.Len); head != nil {
			if l.overwrite(e.Path, head) == nil {
				l.changed()
				n++
			}
			continue
		}
		if !roomFor(e.Path, e.Size) { // no room for a copy: moved where it is
			l.fileMu.Lock()
			_, err := os.Stat(e.Path) // deleted meanwhile: left gone
			if err == nil {
				err = shiftInPlace(e.Path, h.Build(info, cover), h.Len)
			}
			l.fileMu.Unlock()
			if err == nil {
				l.changed()
				n++
			}
			continue
		}
		tmp := tmpOf(e.Path)
		if err := tag.Write(e.Path, tmp, info, cover); err != nil {
			os.Remove(tmp)
			continue
		}
		l.fileMu.Lock()
		_, err = os.Stat(e.Path) // deleted meanwhile: not brought back
		if err == nil {
			err = os.Rename(tmp, e.Path)
		}
		l.fileMu.Unlock()
		if err != nil {
			os.Remove(tmp)
			continue
		}
		if !e.Date.IsZero() {
			os.Chtimes(e.Path, e.Date, e.Date)
		}
		l.changed()
		n++
	}
	return n
}

// overwrite writes head over the start of the file at path, which it fits exactly (BuildIn), its
// times kept; a file deleted meanwhile is left gone.
func (l *Library) overwrite(path string, head []byte) error {
	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteAt(head, 0)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	os.Chtimes(path, st.ModTime(), st.ModTime())
	return err
}

// Retagging is whether Retag is at work.
func (l *Library) Retagging() bool { return l.retagging.Load() }

package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/mp4"
	"q2-pocket/internal/q2"
	"q2-pocket/internal/youtube"
)

// An episode with a YouTube video comes from YouTube when the cleaner can check the video
// (clean.FindVideo): as an M4A, "<date> - <title>.m4a", the feed's MP3 name with another extension.
// A feed's copy that comes after it (the video gone, say) takes the MP3 name back, and the other
// copy goes.

// withExt is e with its file named for ext, and the file it has now when that is another name
// ("" when the same).
func withExt(e EpisodeInfo, ext string) (EpisodeInfo, string) {
	cur := filepath.Ext(e.Path)
	if strings.EqualFold(cur, ext) {
		return e, ""
	}
	old := e.Path
	e.Path = strings.TrimSuffix(e.Path, cur) + ext
	return e, old
}

// commitName keeps e's file name (its file now in place), and removes the file it had (old).
func (l *Library) commitName(e EpisodeInfo, old string) {
	if old == "" {
		return
	}
	l.mu.Lock()
	var b []byte
	if s := l.find(e.Key); s != nil && s.c != nil {
		s.names[e.EID] = filepath.Base(e.Path)
		s.c.Names = s.names
		b, _ = json.Marshal(s.c)
	}
	l.mu.Unlock()
	if b != nil {
		l.saveCache(e.Key, b)
	}
	l.fileMu.Lock()
	os.Remove(old)
	l.fileMu.Unlock()
	l.changed()
}

// fromVideo fetches the episode's YouTube video's audio (vc) to its .part, resuming what is there,
// and puts it in place without its sponsor reads (placeVideo).
func (l *Library) fromVideo(ctx context.Context, e EpisodeInfo, vc *clean.VideoCopy, progress func(done, size int64), note func(string)) error {
	part := l.partPath(e.Key, e.EID)
	f := vc.Format
	src := fmt.Sprintf("yt:%s:%d", vc.Video.ID, f.Itag)
	var was partInfo
	var off int64
	if st, err := os.Stat(part); err == nil {
		if b, err := os.ReadFile(part + ".json"); err == nil && json.Unmarshal(b, &was) == nil && was.Video == src && was.Size == f.Size {
			off = st.Size()
		}
	}
	if f.Size <= 0 {
		return errors.New("YouTube didn't say how long its audio is")
	}
	if off > f.Size {
		off = 0
	}
	if off == 0 {
		removePart(part)
		if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
			return err
		}
		info, _ := json.Marshal(partInfo{Size: f.Size, Video: src})
		os.WriteFile(part+".json", info, 0o644)
	}
	if free, ok := freeBytes(l.o.Dir); ok && free < f.Size-off+reserve {
		return permanent{errors.New("the card is full")}
	}
	note("Downloading it from YouTube")
	progress(off, f.Size)
	refreshed := false
	for off < f.Size {
		to := min(off+youtube.ChunkSize, f.Size) - 1
		n, err := l.videoChunk(ctx, f, part, off, to, func(done int64) { progress(done, f.Size) })
		off += n
		var no refused
		if errors.As(err, &no) && !refreshed && n == 0 { // its address expired (they last hours): asked anew
			refreshed = true
			v, verr := l.o.Clean.YouTube.Video(ctx, vc.Video.ID)
			if verr != nil {
				return verr
			}
			found := false
			for _, g := range v.Audio {
				if g.Itag == f.Itag && g.Size == f.Size {
					f, found = g, true
					break
				}
			}
			if !found {
				removePart(part)
				return errors.New("YouTube's audio changed: starting again")
			}
			continue
		}
		if err != nil {
			return err
		}
		if n > 0 {
			refreshed = false
		}
	}
	return l.placeVideo(ctx, part, e, vc, note)
}

// videoChunk fetches bytes [from, to] of f onto part, at from: how many came.
func (l *Library) videoChunk(ctx context.Context, f youtube.Format, part string, from, to int64, progress func(int64)) (int64, error) {
	fetching, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	stall := time.AfterFunc(stallFor, func() { stalled.Store(true); cancel() })
	defer stall.Stop()
	resp, err := youtube.Get(fetching, l.o.Fetch, f, from, to)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("can't reach YouTube: check the Wi-Fi")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if start, _ := contentRange(resp.Header.Get("Content-Range")); start != from {
			return 0, errors.New("YouTube sent the wrong part")
		}
	case http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusUnauthorized:
		return 0, refused{resp.StatusCode}
	default:
		return 0, fmt.Errorf("YouTube failed (HTTP %d)", resp.StatusCode)
	}
	go func() { <-fetching.Done(); resp.Body.Close() }()
	buf := make([]byte, chunk)
	n, done := 0, int64(0)
	flush := func() error {
		if n == 0 {
			return nil
		}
		if err := q2.Append(part, from+done, buf[:n]); err != nil {
			if errors.Is(err, q2.ErrNoCard) {
				return err
			}
			if free, ok := freeBytes(l.o.Dir); ok && free < chunk {
				return permanent{errors.New("the card is full")}
			}
			return fmt.Errorf("can't write to the card: %v", err)
		}
		done += int64(n)
		n = 0
		progress(from + done)
		return nil
	}
	for {
		k, rerr := resp.Body.Read(buf[n:])
		if k > 0 {
			stall.Reset(stallFor)
			if n += k; n == len(buf) {
				if err := flush(); err != nil {
					return done, err
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if err := flush(); err != nil {
				return done, err
			}
			if stalled.Load() {
				return done, errors.New("the download stalled")
			}
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			return done, errors.New("the download was cut off")
		}
	}
	if err := flush(); err != nil {
		return done, err
	}
	if done != to-from+1 {
		return done, errors.New("the download was cut off")
	}
	return done, nil
}

// placeVideo writes a whole video download (part) as the episode's M4A without what the cleaner
// found to cut, tagged and with its chapters, and moves it into place: a copy beside it, or the part
// itself rewritten where it is when the card has no room for a copy.
func (l *Library) placeVideo(ctx context.Context, part string, e EpisodeInfo, vc *clean.VideoCopy, note func(string)) error {
	if err := os.MkdirAll(filepath.Dir(e.Path), 0o755); err != nil {
		return fmt.Errorf("can't make the show's folder: %v", err)
	}
	in, err := os.OpenFile(part, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	fi, err := in.Stat()
	if err != nil {
		in.Close()
		return err
	}
	st, err := mp4.Read(in, fi.Size())
	if err != nil {
		in.Close()
		removePart(part)
		return fmt.Errorf("YouTube's audio was broken (%v): fetching it again", err)
	}
	rep, chapters := l.o.Clean.VideoDone(ctx, cleanEpisode(e), vc, st.Duration())
	var secs [][2]float64
	for _, k := range rep.Cuts {
		secs = append(secs, [2]float64{k.From, k.To})
	}
	if len(secs) > 0 {
		note("Cutting the sponsor reads out")
	} else {
		note("Writing it for the Q2")
	}
	tags := &mp4.Tags{Title: e.Title, Artist: e.Author, Album: e.Show, Cover: l.cover(ctx, e.Image)}
	if tags.Artist == "" {
		tags.Artist = e.Show
	}
	if !e.Date.IsZero() {
		tags.Date = e.Date.Format("2006-01-02")
	}
	var chs []mp4.Chapter
	for _, c := range chapters {
		chs = append(chs, mp4.Chapter{Start: c.Start, Title: c.Title})
	}
	ranges := st.Ranges(secs)
	src := part
	if roomFor(e.Path, fi.Size()) {
		tmp := tmpOf(e.Path)
		out, err := os.Create(tmp)
		if err != nil {
			in.Close()
			return err
		}
		_, err = st.Write(out, in, ranges, tags, chs)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		in.Close()
		if err != nil {
			os.Remove(tmp)
			return fmt.Errorf("can't write the episode: %v", err)
		}
		src = tmp
	} else { // no room for a copy: the part rewritten where it is; broken part way, it is fetched again
		note("Cutting the sponsor reads out")
		os.Remove(part + ".json")
		w, err := st.Write(in, in, ranges, tags, chs)
		if err == nil {
			err = in.Truncate(w.Size)
		}
		if err == nil {
			err = in.Sync()
		}
		if cerr := in.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			removePart(part)
			return fmt.Errorf("%w: %v", errPartBroken, err)
		}
	}
	l.fileMu.Lock() // a Delete's cancel is seen here, or its remove comes after
	if err := ctx.Err(); err != nil {
		l.fileMu.Unlock()
		os.Remove(tmpOf(e.Path))
		return err
	}
	err = os.Rename(src, e.Path)
	l.fileMu.Unlock()
	if err != nil {
		os.Remove(tmpOf(e.Path))
		return fmt.Errorf("can't move the episode into place: %v", err)
	}
	removePart(part)
	if !e.Date.IsZero() {
		os.Chtimes(e.Path, e.Date, e.Date)
	}
	return nil
}

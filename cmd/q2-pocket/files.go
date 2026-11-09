package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"q2-pocket/internal/library"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/q2"
)

// Files uploaded to Pocket Casts (a Plus feature) download into the card's Podcasts/Pocket Casts
// Files, so Q2 Pod resumes them as it does episodes; their places are sent back as an episode's are.
// They are fetched one at a time from the signed address Pocket Casts gives each time.

const filesFolder = "Pocket Casts Files"

// filePath is where an uploaded file goes on the card.
func (a *app) filePath(f pocketcasts.File) string {
	ext := strings.ToLower(filepath.Ext(f.Title))
	name := strings.TrimSuffix(f.Title, filepath.Ext(f.Title))
	if !audioExt(ext) {
		ext = ".mp3"
		if exts, _ := mime.ExtensionsByType(f.ContentType); len(exts) > 0 {
			ext = exts[0]
		}
		switch f.ContentType {
		case "audio/mp4", "audio/x-m4a", "audio/m4a":
			ext = ".m4a"
		case "audio/x-m4b":
			ext = ".m4b"
		case "audio/mpeg":
			ext = ".mp3"
		}
		name = f.Title
	}
	return filepath.Join(a.opts.card, "Podcasts", filesFolder, library.Safe(name, library.MaxFile-12)+ext)
}

func audioExt(ext string) bool {
	switch ext {
	case ".mp3", ".m4a", ".m4b", ".aac", ".ogg", ".opus", ".flac", ".wav":
		return true
	}
	return false
}

func fileThere(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

type fileJob struct {
	state      string // "Waiting", "Downloading 40%", "Couldn't download: …"
	done, size int64
}

func (a *app) fileState(uuid string) string {
	a.pending.Lock()
	defer a.pending.Unlock()
	if j := a.fileJobs[uuid]; j != nil {
		return j.state
	}
	return ""
}

func (a *app) filesMoving() bool {
	a.pending.Lock()
	defer a.pending.Unlock()
	for _, j := range a.fileJobs {
		if !strings.HasPrefix(j.state, "Couldn't") {
			return true
		}
	}
	return false
}

// fileGet downloads an uploaded file, after any already downloading.
func (a *app) fileGet(uuid string) string {
	files, _, _ := a.sy.uploaded()
	var f *pocketcasts.File
	for i := range files {
		if files[i].UUID == uuid {
			f = &files[i]
		}
	}
	if f == nil {
		return "That file isn't in the list any more"
	}
	if fileThere(a.filePath(*f)) {
		return "It's already on the Q2"
	}
	a.pending.Lock()
	if a.fileJobs == nil {
		a.fileJobs = map[string]*fileJob{}
	}
	if j := a.fileJobs[uuid]; j != nil && !strings.HasPrefix(j.state, "Couldn't") {
		a.pending.Unlock()
		return "It's already downloading"
	}
	a.fileJobs[uuid] = &fileJob{state: "Waiting"}
	a.pending.Unlock()
	go func() {
		a.fileMu.Lock() // one at a time
		defer a.fileMu.Unlock()
		err := a.fetchFile(*f)
		a.pending.Lock()
		if err != nil {
			a.fileJobs[uuid].state = "Couldn't download: " + err.Error()
		} else {
			delete(a.fileJobs, uuid)
		}
		a.pending.Unlock()
	}()
	return "Downloading"
}

func (a *app) fetchFile(f pocketcasts.File) error {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Hour)
	defer cancel()
	u, err := a.pc.FileURL(ctx, f.UUID)
	if err != nil {
		return err
	}
	dst := a.filePath(f)
	part := filepath.Join(a.opts.dir, "partial", "file-"+f.UUID+".part")
	os.MkdirAll(filepath.Dir(part), 0o755)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := a.web.Do(req)
	if err != nil {
		return errors.New("can't reach Pocket Casts: check the Wi-Fi")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Pocket Casts answered HTTP %d", resp.StatusCode)
	}
	os.Remove(part)
	buf := make([]byte, 512<<10)
	var off int64
	for {
		n, rerr := io.ReadFull(resp.Body, buf)
		if n > 0 {
			if err := q2.Append(part, off, buf[:n]); err != nil {
				return errors.New("can't write to the card")
			}
			off += int64(n)
			a.pending.Lock()
			if j := a.fileJobs[f.UUID]; j != nil {
				j.state = "Downloading " + pct(off, resp.ContentLength)
			}
			a.pending.Unlock()
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			os.Remove(part)
			return errors.New("the download was cut off")
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(part, dst)
}

// filePlaces sends each downloaded file's place to Pocket Casts, as places does an episode's.
func (s *syncer) filePlaces(ctx context.Context) {
	files, err, _ := s.uploaded()
	if err != nil {
		return
	}
	ring := s.a.places()
	for _, f := range files {
		p := s.a.filePath(f)
		if !fileThere(p) {
			continue
		}
		id := "file:" + f.UUID
		q := ring[placeKey(p)]
		s.mu.Lock()
		st := s.st.Eps[id]
		if st == nil {
			st = &epSync{Q2: -1}
			s.st.Eps[id] = st
		}
		s.mu.Unlock()
		switch {
		case q > 0 && q != st.Q2:
			if s.a.pc.SetFilePlace(ctx, f.UUID, q, pocketcasts.InProgress) == nil {
				st.Q2, st.PC = q, q
			}
		case q == 0 && st.Q2 > 0 && f.Duration > 0 && st.Q2 >= int(f.Duration)-int(playedTail.Seconds()):
			if s.a.pc.SetFilePlace(ctx, f.UUID, int(f.Duration), pocketcasts.Played) == nil {
				st.Q2, st.Done = 0, true
			}
		}
	}
}

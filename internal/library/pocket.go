package library

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"q2-pocket/internal/feed"
	"q2-pocket/internal/pocketcasts"
)

// Signed in to Pocket Casts, the shows are the account's: each has Pocket Casts' id (Show.PC) and
// its episodes come from Pocket Casts' public copy of its feed, ids and all, so Up Next, places and
// stars name them as Pocket Casts does. A show that only holds an episode of Up Next, Starred or
// In Progress, not subscribed, is Hidden: kept, out of the lists.

// PCURL is a Pocket Casts show's address when its feed's isn't known: its page on Pocket Casts.
func PCURL(uuid string) string { return "https://pca.st/podcast/" + uuid }

// fetchPC is a Pocket Casts show's episodes, as a feed at u (the show's own address).
func (l *Library) fetchPC(ctx context.Context, pc, u, etag string) (*Fetched, error) {
	if l.o.PC == nil {
		return nil, errors.New("Pocket Casts isn't set up")
	}
	sh, tag, same, err := l.o.PC.Full(ctx, pc, etag)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		var se pocketcasts.StatusError
		if errors.As(err, &se) {
			return nil, statusErr(se.Code)
		}
		return nil, errors.New("can't reach Pocket Casts: check the Wi-Fi")
	}
	out := &Fetched{URL: u, ETag: tag}
	if same {
		out.unchanged = true
		return out, nil
	}
	f := &feed.Feed{Title: sh.Title, Author: sh.Author, Image: pocketcasts.ArtURL(pc)}
	for _, e := range sh.Episodes {
		if e.UUID == "" || e.URL == "" || !feed.Audio(e.FileType, e.URL) {
			continue
		}
		when, _ := time.Parse(time.RFC3339, e.Published)
		f.Episodes = append(f.Episodes, feed.Episode{GUID: e.UUID, PC: e.UUID, Title: cmpOr(e.Title, "Untitled episode"),
			Date: when, Duration: int(e.Duration), URL: e.URL, Type: e.FileType, Length: int64(e.FileSize)})
	}
	sort.SliceStable(f.Episodes, func(i, j int) bool { return f.Episodes[i].Date.After(f.Episodes[j].Date) })
	out.Feed = f
	return out, nil
}

func cmpOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// adopted gives a show's episodes, newly from Pocket Casts, the ids they had from its RSS feed (a
// show subscribed before signing in): an old episode no longer listed under its id is the new one
// with its address, else with its title (when only one has it). Its file on the card stays its own.
func adopted(prev, eps []feed.Episode) {
	ids := make(map[string]bool, len(eps))
	for _, e := range eps {
		ids[e.GUID] = true
	}
	byURL, byTitle, twice := map[string]string{}, map[string]string{}, map[string]bool{}
	for _, e := range prev {
		if ids[e.GUID] {
			continue
		}
		byURL[urlKey(e.URL)] = e.GUID
		t := strings.ToLower(strings.Join(strings.Fields(e.Title), " "))
		if _, ok := byTitle[t]; ok {
			twice[t] = true
		}
		byTitle[t] = e.GUID
	}
	for i, e := range eps {
		if e.PC == "" {
			continue
		}
		if id, ok := byURL[urlKey(e.URL)]; ok {
			eps[i].GUID = id
			delete(byURL, urlKey(e.URL))
			continue
		}
		t := strings.ToLower(strings.Join(strings.Fields(e.Title), " "))
		if id, ok := byTitle[t]; ok && !twice[t] {
			eps[i].GUID = id
			delete(byTitle, t)
		}
	}
}

// urlKey is an address without its query and tracking prefixes' hosts: the file it names.
func urlKey(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[i+1:] + "|" + lastDir(u[:i])
	}
	return u
}

func lastDir(u string) string {
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[i+1:]
	}
	return u
}

// ByPC is the show with Pocket Casts id uuid.
func (l *Library) ByPC(uuid string) (ShowInfo, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.byPC(uuid); s != nil {
		return info(s), true
	}
	return ShowInfo{}, false
}

func (l *Library) byPC(uuid string) *show {
	for _, s := range l.shows {
		if s.PC == uuid && uuid != "" {
			return s
		}
	}
	return nil
}

// AdoptPC adds a Pocket Casts show, its episodes fetched by the next refresh; hidden: only for its
// episodes. One there already is shown when it is subscribed now.
func (l *Library) AdoptPC(p pocketcasts.Podcast, hidden bool) ShowInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.byPC(p.UUID); s != nil {
		if s.Hidden && !hidden {
			s.Hidden = false
			l.saveOPML()
		}
		return info(s)
	}
	u := PCURL(p.UUID)
	if f, err := Normalize(p.Feed); p.Feed != "" && err == nil {
		if s := l.byURL(f); s != nil { // subscribed by its feed: it is that show, from Pocket Casts now
			s.PC, s.Hidden = p.UUID, s.Hidden && hidden
			l.saveOPML()
			return info(s)
		}
		u = f
	}
	key := KeyFor("pc:" + p.UUID)
	for l.find(key) != nil {
		key = KeyFor(key + p.UUID)
	}
	title := cmpOr(p.Title, "Untitled podcast")
	s := &show{Show: Show{Key: key, URL: u, Title: title, PC: p.UUID, Hidden: hidden}}
	s.Folder = l.folderFor(title, key)
	l.shows = append(l.shows, s)
	l.sort()
	l.saveOPML()
	return info(s)
}

// SetPC makes a show subscribed by its feed a Pocket Casts show: its episodes come from Pocket
// Casts from its next refresh, keeping the ids (and so the files) they had (adopted).
func (l *Library) SetPC(key, uuid string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.find(key)
	if s == nil {
		return ErrNoShow
	}
	if o := l.byPC(uuid); o != nil && o != s {
		return errors.New("another show is that Pocket Casts podcast")
	}
	s.PC = uuid
	if s.c != nil {
		s.c.ETag, s.c.Modified = "", ""
	}
	return l.saveOPML()
}

// Hide keeps a show only for its episodes (hidden), or lists it again.
func (l *Library) Hide(key string, hidden bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.find(key)
	if s == nil {
		return ErrNoShow
	}
	if s.Hidden != hidden {
		s.Hidden = hidden
		return l.saveOPML()
	}
	return nil
}

// All is every show, those kept only for an episode too.
func (l *Library) All() []ShowInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ShowInfo, 0, len(l.shows))
	for _, s := range l.shows {
		out = append(out, info(s))
	}
	return out
}

// PCEpisode is the episode Pocket Casts knows as uuid, of the show it knows as podcast.
func (l *Library) PCEpisode(podcast, uuid string) (EpisodeInfo, error) {
	l.mu.Lock()
	s := l.byPC(podcast)
	if s == nil || s.c == nil {
		l.mu.Unlock()
		return EpisodeInfo{}, ErrNoShow
	}
	for _, e := range s.c.Episodes {
		if e.PC == uuid || e.GUID == uuid {
			ei := l.episode(s, e)
			l.mu.Unlock()
			return l.mark([]EpisodeInfo{ei})[0], nil
		}
	}
	l.mu.Unlock()
	return EpisodeInfo{}, ErrNoEpisode
}

// Has is whether a show's episodes have been read at least once.
func (l *Library) Has(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.find(key)
	return s != nil && s.c != nil && len(s.c.Episodes) > 0
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/library"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/q2"
)

// Signed in to Pocket Casts, the app keeps the Q2 and the account in step, as pocketcli does from a
// computer (sync): the podcasts subscribed, Up Next (its first few episodes kept downloaded, played in
// order from the Q2's Up Next page: pages.go), and
// where each downloaded episode was left. Q2 Pod's resume ring says where the Q2 is in each
// episode; a place it saves is sent to Pocket Casts, mapped from the episode as cut to the episode as
// served (clean.Report.Original), and a place moved on another device is put in the ring (setPlaces:
// a Q2 Pod that can) mapped the other way. An episode whose place goes near its end was played: it
// is marked so and leaves Up Next. The episode the Q2 starts becomes Up Next's first, and goes in the
// listening history, as the apps do.

const (
	syncEvery   = 3 * time.Minute
	subsEvery   = 30 * time.Minute
	statesEvery = 15 * time.Minute
	listFor     = 2 * time.Minute // a list a page asked for is fetched again after this
	missFor     = time.Hour       // an Up Next episode Pocket Casts' copy lacks isn't looked for again sooner
	listenFor   = 3 * time.Minute // the ring written this recently: a podcast is playing
	playedTail  = 5 * time.Minute // a place this near the end, then gone: played
	moved       = 15              // seconds a place must move by to be sent
	newPerSync  = 60              // Up Next episodes new to the library looked up a sync
	soonAfter   = 20 * time.Second
)

// syncState is .pocket/sync.json.
type syncState struct {
	Eps      map[string]*epSync `json:"episodes"`                 // by "<key>:<eid>"
	Last     uint32             `json:"last,omitempty"`           // the ring's newest place, as last seen
	Migrated bool               `json:"migrated"`                 // shows subscribed by their feed found in Pocket Casts
	All      bool               `json:"download_all,omitempty"`   // Download All: the rest of Up Next still to be asked for
	Held     bool               `json:"downloads_held,omitempty"` // Stop After This One: Up Next isn't downloaded by itself
	Scripts  map[string]bool    `json:"transcripts,omitempty"`    // episodes on the card whose transcript was looked for
}

// epSync is an episode's place as last seen on both sides.
type epSync struct {
	Q2   int  `json:"q2"`             // seconds into it as cut: Q2 Pod's place
	PC   int  `json:"pc"`             // seconds into it as served: Pocket Casts'
	Done bool `json:"done,omitempty"` // played
}

type pcList struct {
	at      time.Time
	eps     []pocketcasts.Episode
	err     error
	loading bool
}

type syncer struct {
	a    *app
	wake chan struct{}
	one  sync.Mutex // a sync at a time

	mu       sync.Mutex
	st       syncState
	at       time.Time // the last sync that worked
	err      string    // the last one's failure
	failAt   time.Time // and when it failed
	busy     bool
	subsAt   time.Time
	statesAt time.Time
	upnext   []pocketcasts.Episode
	upAt     time.Time
	titles   map[string]string // a podcast's title by Pocket Casts id
	lists    map[string]*pcList
	missed   map[string]time.Time
	wrote    map[uint32]int // places this app put in the ring: Q2 Pod saving them isn't playing
	files    *fileList
	soon     bool   // more to do: the next sync in soonAfter
	stars    string // /tmp/q2-starred as last written
	starsOut bool   // written since the start or the last sign-out: the firmware's heart is the star
}

func newSyncer(a *app) *syncer {
	s := &syncer{a: a, wake: make(chan struct{}, 1), titles: map[string]string{}, lists: map[string]*pcList{},
		missed: map[string]time.Time{}, wrote: map[uint32]int{}}
	if b, err := os.ReadFile(s.path()); err == nil {
		json.Unmarshal(b, &s.st)
	}
	if s.st.Eps == nil {
		s.st.Eps = map[string]*epSync{}
	}
	return s
}

func (s *syncer) path() string { return filepath.Join(s.a.opts.dir, "sync.json") }

func (s *syncer) save() {
	s.mu.Lock()
	b, _ := json.Marshal(s.st)
	s.mu.Unlock()
	q2.WriteFile(s.path(), b)
}

// kick asks for a sync now, the subscriptions too.
func (s *syncer) kick() {
	s.mu.Lock()
	s.subsAt = time.Time{}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// status is when the last sync worked, why the last one failed, and whether one is under way.
func (s *syncer) status() (time.Time, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.at, s.err, s.busy
}

// listening is whether a podcast is playing (signed in: its place is to be synced).
func (s *syncer) listening() bool {
	if !s.a.pc.SignedIn() {
		return false
	}
	_, at := s.a.recentPlace()
	return !at.IsZero() && time.Since(at) < listenFor
}

func (s *syncer) run(ctx context.Context) {
	for {
		if s.a.pc.SignedIn() && s.a.online() {
			s.once(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(s.wait()):
		}
	}
}

func (s *syncer) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failAt = time.Now()
	switch {
	case errors.Is(err, pocketcasts.ErrSignIn):
		s.err = "Sign in to Pocket Casts again"
	case err != nil:
		s.err = "Couldn't reach Pocket Casts"
	}
}

// once is a sync.
func (s *syncer) once(ctx context.Context) {
	s.one.Lock()
	defer s.one.Unlock()
	s.mu.Lock()
	s.busy = true
	subsDue := time.Since(s.subsAt) > subsEvery
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()
	pc := s.a.pc
	if subsDue {
		pods, err := pc.Podcasts(ctx)
		if err != nil {
			s.fail(err)
			return
		}
		s.subscriptions(ctx, pods)
		s.mu.Lock()
		s.subsAt = time.Now()
		s.mu.Unlock()
	}
	up, err := pc.UpNext(ctx)
	if err != nil {
		s.fail(err)
		return
	}
	s.mu.Lock()
	s.upnext, s.upAt = up, time.Now()
	s.mu.Unlock()
	inprog, err := pc.InProgress(ctx)
	if err != nil {
		s.fail(err)
		return
	}
	s.setList("inprog", inprog, nil)
	s.places(ctx, inprog)
	s.nowPlaying(ctx)
	s.starred(ctx)
	s.filePlaces(ctx)
	s.save()
	s.mu.Lock() // the account and the Q2 agree: synced (the app opens on it, menu)
	s.at, s.err = time.Now(), ""
	s.mu.Unlock()
	s.download(ctx, up)
	s.transcripts(ctx)
	s.save()
}

// subscriptions makes the shows the account's: a show subscribed by its feed is found in Pocket
// Casts once and subscribed there too; Pocket Casts' shows are added; one unsubscribed elsewhere is
// hidden, its downloads kept.
func (s *syncer) subscriptions(ctx context.Context, pods []pocketcasts.Podcast) {
	lib, pc := s.a.lib, s.a.pc
	have := map[string]bool{}
	for _, p := range pods {
		have[p.UUID] = true
	}
	s.mu.Lock()
	migrated := s.st.Migrated
	for _, p := range pods {
		s.titles[p.UUID] = p.Title
	}
	s.mu.Unlock()
	if !migrated {
		for _, si := range lib.All() {
			if si.PC != "" || si.Hidden {
				continue
			}
			uuid, err := pc.Resolve(ctx, si.URL)
			if err != nil || uuid == "" {
				continue // Pocket Casts doesn't know it (a private feed): it stays the Q2's own
			}
			if !have[uuid] && pc.Subscribe(ctx, uuid) == nil {
				have[uuid] = true
			}
			lib.SetPC(si.Key, uuid)
		}
		s.mu.Lock()
		s.st.Migrated = true
		s.mu.Unlock()
	}
	for _, p := range pods {
		lib.AdoptPC(p, false)
	}
	for _, si := range lib.All() {
		if si.PC != "" && !si.Hidden && !have[si.PC] {
			lib.Hide(si.Key, true)
		}
	}
	go lib.RefreshAll(s.a.ctx, staleAfter) // the new ones' episodes
}

// title is a Pocket Casts podcast's title, as the account or the library knows it.
func (s *syncer) title(uuid string) string {
	s.mu.Lock()
	t := s.titles[uuid]
	s.mu.Unlock()
	if t == "" {
		if si, ok := s.a.lib.ByPC(uuid); ok {
			t = si.Title
		}
	}
	return t
}

// episode is a Pocket Casts episode in the library: its show added (hidden) and fetched if need be.
func (s *syncer) episode(ctx context.Context, e pocketcasts.Episode) (library.EpisodeInfo, error) {
	lib := s.a.lib
	ep, err := lib.PCEpisode(e.Show(), e.UUID)
	if err == nil || !pocketcasts.IsUUID(e.Show()) {
		return ep, err
	}
	s.mu.Lock()
	miss, missed := s.missed[e.UUID]
	s.mu.Unlock()
	if missed && time.Since(miss) < missFor {
		return ep, err
	}
	si, ok := lib.ByPC(e.Show())
	if !ok {
		title := cmpOr(s.title(e.Show()), e.PodcastTitle)
		if title == "" { // its folder is named after it: ask Pocket Casts' copy first
			if sh, _, _, ferr := s.a.pc.Full(ctx, e.Show(), ""); ferr == nil {
				title = sh.Title
			}
		}
		si = lib.AdoptPC(pocketcasts.Podcast{UUID: e.Show(), Title: title}, true)
	}
	lib.Refresh(ctx, si.Key)
	if ep, err = lib.PCEpisode(e.Show(), e.UUID); err != nil {
		s.mu.Lock()
		s.missed[e.UUID] = time.Now()
		s.mu.Unlock()
	}
	return ep, err
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// download keeps Up Next downloaded: all of it (UpNext < 0), or its first UpNext episodes; all of it
// once while Download All is on, which a pass through the whole of Up Next ends. A sync looks up at
// most newPerSync episodes the library doesn't have yet (their shows fetched first), and asks for
// another sync soon while more are left: a long Up Next takes a while, places first.
func (s *syncer) download(ctx context.Context, up []pocketcasts.Episode) {
	n, looked, all := s.a.settings().UpNext, 0, s.allPending()
	if all {
		n = -1
	} else if s.held() {
		return // Stop After This One: nothing by itself until Resume Downloads
	}
	for _, e := range up {
		if n == 0 || ctx.Err() != nil {
			return
		}
		if e.Show() == pocketcasts.FilesPodcast {
			continue
		}
		n--
		ep, err := s.a.lib.PCEpisode(e.Show(), e.UUID)
		if err != nil {
			if looked == newPerSync {
				s.again()
				return
			}
			looked++
			ep, err = s.episode(ctx, e)
		}
		if err == nil && !ep.OnCard && ep.Job == nil {
			s.a.lib.DL.Queue(ep.Key, ep.EID)
		}
	}
	if all && ctx.Err() == nil { // every episode of Up Next asked for: Download All is done
		s.downloadAll(false)
	}
}

// transcriptsPerSync is how many episodes on the card a sync looks up a transcript for.
const transcriptsPerSync = 5

// transcripts gives episodes downloaded before transcripts were kept theirs (Now Playing's
// transcript page), a few a sync, each looked for once.
func (s *syncer) transcripts(ctx context.Context) {
	eps, _ := s.a.lib.Downloaded()
	n := 0
	for _, e := range eps {
		id := e.Key + ":" + e.EID
		s.mu.Lock()
		done := s.st.Scripts[id]
		s.mu.Unlock()
		if done || ctx.Err() != nil {
			continue
		}
		if n == transcriptsPerSync {
			s.again()
			return
		}
		n++
		if !s.a.lib.Transcript(ctx, e) {
			return // a download is being cleaned: next time
		}
		s.mu.Lock()
		if s.st.Scripts == nil {
			s.st.Scripts = map[string]bool{}
		}
		s.st.Scripts[id] = true
		s.mu.Unlock()
	}
}

// downloadAll turns Download All on (and asks for a sync) or off.
func (s *syncer) downloadAll(on bool) {
	s.mu.Lock()
	s.st.All = on
	if on {
		s.st.Held = false // asked for: downloads go on
	}
	s.mu.Unlock()
	s.save()
	if on {
		s.kick()
	}
}

// hold is Stop After This One (on) and Resume Downloads (off): while held, Up Next isn't downloaded by
// itself (a press on an episode, or Download All, still is).
func (s *syncer) hold(on bool) {
	s.mu.Lock()
	s.st.Held = on
	s.mu.Unlock()
	s.save()
	if !on {
		s.kick()
	}
}

func (s *syncer) held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Held
}

// failedSince is why a sync that ended after t failed, "" when none has.
func (s *syncer) failedSince(t time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAt.After(t) {
		return s.err
	}
	return ""
}

// allPending is whether Download All is still asking for Up Next's episodes.
func (s *syncer) allPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.All
}

// again asks for the next sync soon, not on syncEvery's clock.
func (s *syncer) again() {
	s.mu.Lock()
	s.soon = true
	s.mu.Unlock()
}

// asServed is an episode of the library as Pocket Casts' calls take it.
func asServed(e library.EpisodeInfo) pocketcasts.Episode {
	return pocketcasts.Episode{UUID: e.Feed.PC, Podcast: e.PC, Title: e.Title, URL: e.Feed.URL,
		Published: e.Date.UTC().Format(time.RFC3339)}
}

// cutLength is a downloaded episode's seconds as cut.
func cutLength(r *clean.Report, e library.EpisodeInfo) float64 {
	if r != nil && r.Length > 0 {
		_, _, cut := r.Removed()
		return r.Length - cut
	}
	return float64(e.Duration)
}

// places keeps each downloaded episode's place the same on the Q2 and in Pocket Casts.
func (s *syncer) places(ctx context.Context, inprog []pocketcasts.Episode) {
	lib, pc := s.a.lib, s.a.pc
	eps, _ := lib.Downloaded()
	ring := s.a.places()
	server := map[string]pocketcasts.State{}
	for _, e := range inprog {
		server[e.UUID] = pocketcasts.State{PlayedUpTo: int(e.PlayedUpTo), Status: int(e.Status)}
	}
	s.mu.Lock()
	statesDue := time.Since(s.statesAt) > statesEvery
	s.mu.Unlock()
	if statesDue { // played or moved elsewhere, out of In Progress: each show's states
		asked := map[string]bool{}
		for _, e := range eps {
			id := e.Key + ":" + e.EID
			_ = id
			if e.PC == "" || asked[e.PC] {
				continue
			}
			asked[e.PC] = true
			if st, err := pc.States(ctx, e.PC); err == nil {
				for k, v := range st {
					if _, ok := server[k]; !ok {
						server[k] = v
					}
				}
			}
		}
		s.mu.Lock()
		s.statesAt = time.Now()
		s.mu.Unlock()
	}
	pulls := map[string]int{}
	pulled := map[string]*epSync{} // by path: their Q2 place set only once Q2 Pod takes it
	for _, e := range eps {
		if e.PC == "" || e.Feed.PC == "" {
			continue
		}
		id := e.Key + ":" + e.EID
		r := clean.Load(s.a.opts.dir, e.Key, e.EID)
		q := ring[placeKey(e.Path)]
		srv, known := server[e.Feed.PC]
		s.mu.Lock()
		st := s.st.Eps[id]
		if st == nil { // new to the sync: neither side's place seen yet
			st = &epSync{Q2: -1, PC: -1}
			s.st.Eps[id] = st
		}
		s.mu.Unlock()
		served := func(sec int) int { // a ring place as Pocket Casts counts it
			if sec <= 0 {
				return sec
			}
			return int(r.Original(float64(sec)))
		}
		v := side{Q2: served(q), Q2Was: served(st.Q2), PC: srv.PlayedUpTo, PCWas: st.PC, Status: srv.Status,
			Known: known, Done: st.Done, NearEnd: st.Q2 > 0 && float64(st.Q2) >= cutLength(r, e)-playedTail.Seconds()}
		what, at := decide(v)
		switch what {
		case push:
			if pc.SetPlace(ctx, e.PC, e.Feed.PC, at) == nil {
				st.Q2, st.PC, st.Done = q, at, false
			}
		case pull:
			pulls[e.Path], pulled[e.Path] = int(r.Kept(float64(at))), st
			st.Q2, st.PC, st.Done = q, at, false
		case played:
			if pc.SetStatus(ctx, e.PC, e.Feed.PC, pocketcasts.Played) == nil {
				pc.UpNextRemove(ctx, e.Feed.PC)
				st.Q2, st.Done = 0, true
				if known {
					st.PC = srv.PlayedUpTo
				}
				s.played(e)
			}
		case forget:
			pulls[e.Path], pulled[e.Path] = 0, st
			st.Q2, st.PC, st.Done = q, srv.PlayedUpTo, srv.Status == pocketcasts.Played
			if st.Done {
				s.played(e)
			}
		case settle:
			st.Q2 = q
			if known {
				st.PC, st.Done = srv.PlayedUpTo, srv.Status == pocketcasts.Played
			}
		}
	}
	if len(pulls) > 0 && s.a.setPlaces(pulls) {
		s.mu.Lock()
		for p, sec := range pulls {
			s.wrote[placeKey(p)] = sec
			pulled[p].Q2 = sec
		}
		s.mu.Unlock()
	}
}

// played deletes a played episode's download when the settings say, unless Up Next wants it.
func (s *syncer) played(e library.EpisodeInfo) {
	if !s.a.settings().DeletePlayed {
		return
	}
	s.mu.Lock()
	for _, u := range s.upnext {
		if u.UUID == e.Feed.PC {
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()
	s.a.lib.Delete(e.Key, e.EID)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// nowPlaying: the episode Q2 Pod saved a place in last, newly, becomes Up Next's first and goes in
// the listening history (a place this app put there aside).
func (s *syncer) nowPlaying(ctx context.Context) {
	key, _ := s.a.recentPlace()
	s.mu.Lock()
	if key == 0 || key == s.st.Last {
		s.mu.Unlock()
		return
	}
	s.st.Last = key
	_, ours := s.wrote[key]
	delete(s.wrote, key)
	s.mu.Unlock()
	if ours {
		return
	}
	eps, _ := s.a.lib.Downloaded()
	for _, e := range eps {
		if e.PC != "" && e.Feed.PC != "" && placeKey(e.Path) == key {
			ep := asServed(e)
			s.a.pc.UpNextAdd(ctx, "now", ep)
			s.a.pc.Listened(ctx, ep)
			s.mu.Lock()
			s.upAt = time.Time{} // Up Next changed: the page asks again
			s.mu.Unlock()
			return
		}
	}
}

// upNext is Up Next as last fetched, and whether it is being fetched.
func (s *syncer) upNext() ([]pocketcasts.Episode, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upnext, s.upAt
}

func (s *syncer) setList(name string, eps []pocketcasts.Episode, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.lists[name]
	if l == nil {
		l = &pcList{}
		s.lists[name] = l
	}
	l.loading = false
	if err == nil || l.eps == nil {
		l.eps, l.err, l.at = eps, err, time.Now()
	}
}

// list is one of the account's lists ("inprog", "new", "starred", "history"), fetched again in the
// background when older than listFor; loading while it is.
func (s *syncer) list(name string) ([]pocketcasts.Episode, error, bool) {
	s.mu.Lock()
	l := s.lists[name]
	if l == nil {
		l = &pcList{}
		s.lists[name] = l
	}
	fresh := !l.at.IsZero() && time.Since(l.at) < listFor
	if !fresh && !l.loading && s.a.pc.SignedIn() {
		l.loading = true
		go func() {
			ctx, cancel := context.WithTimeout(s.a.ctx, 60*time.Second)
			defer cancel()
			var eps []pocketcasts.Episode
			var err error
			switch name {
			case "inprog":
				eps, err = s.a.pc.InProgress(ctx)
			case "new":
				eps, err = s.a.pc.NewReleases(ctx)
			case "starred":
				eps, err = s.a.pc.Starred(ctx)
			case "history":
				eps, err = s.a.pc.History(ctx)
			}
			s.setList(name, eps, err)
		}()
	}
	eps, err, loading := l.eps, l.err, l.loading
	s.mu.Unlock()
	return eps, err, loading
}

// forget drops the lists and Up Next, after a change: the pages ask again.
func (s *syncer) forget() {
	s.mu.Lock()
	for _, l := range s.lists {
		l.at = time.Time{}
	}
	s.upAt = time.Time{}
	s.mu.Unlock()
}

// signedOut forgets everything of the account but what is on the card.
func (s *syncer) signedOut() {
	s.mu.Lock()
	s.lists, s.upnext, s.err, s.at = map[string]*pcList{}, nil, "", time.Time{}
	s.st.Eps, s.st.Migrated = map[string]*epSync{}, false
	s.stars, s.starsOut = "", false
	s.mu.Unlock()
	s.save()
	os.Remove(s.a.opts.starred) // Now Playing's heart is My Fav again
}

// fileList is the account's uploaded files.
type fileList struct {
	at      time.Time
	files   []pocketcasts.File
	err     error
	loading bool
}

// files is the account's uploaded files, fetched again in the background when older than listFor.
func (s *syncer) uploaded() ([]pocketcasts.File, error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = &fileList{}
	}
	f := s.files
	if (f.at.IsZero() || time.Since(f.at) > listFor) && !f.loading && s.a.pc.SignedIn() {
		f.loading = true
		go func() {
			ctx, cancel := context.WithTimeout(s.a.ctx, 60*time.Second)
			defer cancel()
			files, err := s.a.pc.Files(ctx)
			s.mu.Lock()
			f.files, f.err, f.at, f.loading = files, err, time.Now(), false
			s.mu.Unlock()
		}()
	}
	return f.files, f.err, f.loading
}

// dropFiles forgets the uploaded files, so they are asked for again.
func (s *syncer) dropFiles() {
	s.mu.Lock()
	if s.files != nil && !s.files.loading {
		s.files.at = time.Time{}
	}
	s.mu.Unlock()
}

// roundMin is seconds as whole minutes, for a row.
func roundMin(sec float64) int { return int(math.Round(sec / 60)) }

// wait is how long until the next sync: soonAfter when one left work over, else syncEvery.
func (s *syncer) wait() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.soon {
		s.soon = false
		return soonAfter
	}
	return syncEvery
}

// The firmware's star (Q2 Pod's Now Playing): it asks for a downloaded episode to be starred or not
// in /tmp/q2-star, "<1|0>\t<path>", and shows the star from /tmp/q2-starred, the paths of the
// downloaded episodes Pocket Casts has starred.

// starred writes /tmp/q2-starred from the account's Starred list.
func (s *syncer) starred(ctx context.Context) {
	eps, err := s.a.pc.Starred(ctx)
	if err != nil {
		return
	}
	s.setList("starred", eps, nil)
	s.writeStarred(eps)
}

func (s *syncer) writeStarred(eps []pocketcasts.Episode) {
	var b strings.Builder
	for _, e := range eps {
		if ep, err := s.a.lib.PCEpisode(e.Show(), e.UUID); err == nil && ep.OnCard {
			b.WriteString(ep.Path + "\n")
		}
	}
	s.mu.Lock()
	same := b.String() == s.stars && s.starsOut
	s.stars, s.starsOut = b.String(), true
	s.mu.Unlock()
	if !same { // an empty list too: its file says the account is signed in
		q2.WriteFile(s.a.opts.starred, []byte(b.String()))
	}
}

// star takes the firmware's request, if there is one: the episode starred or not, at once.
func (s *syncer) star() {
	b, err := os.ReadFile(s.a.opts.star)
	if err != nil {
		return
	}
	os.Remove(s.a.opts.star)
	on, path, ok := strings.Cut(strings.TrimSpace(string(b)), "\t")
	if !ok || !s.a.pc.SignedIn() {
		return
	}
	eps, _ := s.a.lib.Downloaded()
	for _, e := range eps {
		if e.Path != path || e.PC == "" || e.Feed.PC == "" {
			continue
		}
		go func(e library.EpisodeInfo) {
			ctx, cancel := context.WithTimeout(s.a.ctx, 30*time.Second)
			defer cancel()
			if s.a.pc.Star(ctx, e.PC, e.Feed.PC, on == "1") != nil {
				return
			}
			if eps, err := s.a.pc.Starred(ctx); err == nil {
				s.setList("starred", eps, nil)
				s.writeStarred(eps)
			}
		}(e)
		return
	}
}

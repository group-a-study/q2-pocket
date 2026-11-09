package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"q2-pocket/internal/adfree"
	"q2-pocket/internal/clean"
	"q2-pocket/internal/library"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/q2"
	"q2-pocket/internal/q2spot"
)

// The Q2's pages of the Pocket Casts account (device.go has the rest): Up Next, In Progress, New
// Releases, Starred and History, each episode a row as a show's are (on the card it plays; else a
// press downloads it), Play/Pause held adding it to Up Next (in Up Next, held twice, taking it out);
// Discover's lists; and Ads & Sponsors, the cleaning's settings and what it has cut.
//
// Ops: upnext, inprog, new, starred, history [cursor]; discover; dlist <name>; dshow <uuid>;
// adfree; files. Actions: pcget <podcast:uuid>; upq:<podcast>:<uuid> (held); unq:<podcast>:<uuid>
// (held twice); dsub <uuid>; set <name>; sync; fget <uuid>.

var listNames = map[string]string{"inprog": "In Progress", "new": "New Releases", "starred": "Starred", "history": "History"}

// pcID is an episode's row argument: "<podcast>:<uuid>".
func pcID(e pocketcasts.Episode) string { return e.Show() + ":" + e.UUID }

// pcRow is a Pocket Casts episode's row: hold is the op Play/Pause held sends; ctx the context a
// row on the card plays in.
func (a *app) pcRow(e pocketcasts.Episode, hold, ctx string, places map[uint32]int) q2spot.Row {
	show := a.sy.title(e.Show())
	ep, err := a.lib.PCEpisode(e.Show(), e.UUID)
	if err == nil && (ep.OnCard || ep.Job != nil) {
		r := episodeRow(ep, true, places[placeKey(ep.Path)], a.opts.dir)
		if r.Kind == "local" {
			r.Context = ctx
		}
		r.Position = hold
		return r
	}
	when := join(show, day(e.Date()), length(int(e.Duration)))
	if e.PlayedUpTo > 0 && int(e.Status) != pocketcasts.Played && e.Duration > e.PlayedUpTo {
		when = join(show, length(int(e.Duration-e.PlayedUpTo))+" left")
	}
	if int(e.Status) == pocketcasts.Played {
		when = join(when, "Played")
	}
	a.pending.Lock()
	_, getting := a.getting[e.UUID]
	a.pending.Unlock()
	if getting {
		return q2spot.Row{Kind: "info", Title: e.Title, Subtitle: join(show, "Getting it ready…"), Position: hold}
	}
	return q2spot.Row{Kind: "action", URI: "pcget", Context: pcID(e), Title: e.Title, Subtitle: join(when, "Download"), Position: hold}
}

// pcPage is a list of episodes as rows, from off, and the cursor for More….
func (a *app) pcPage(eps []pocketcasts.Episode, off int, hold func(pocketcasts.Episode) string, ctx string) ([]q2spot.Row, string) {
	places := a.places()
	var rows []q2spot.Row
	end := min(off+pageSize, len(eps))
	for i := off; i < end; i++ {
		rows = append(rows, a.pcRow(eps[i], hold(eps[i]), ctx, places))
	}
	if end < len(eps) {
		return rows, strconv.Itoa(end)
	}
	return rows, ""
}

func (a *app) signInRows() []q2spot.Row {
	sub := "Open " + a.URL(false) + " on your phone"
	if a.addr() == "" {
		sub = "Connect the Q2 to Wi-Fi first"
	}
	return []q2spot.Row{info("Not signed in", "Sign in to Pocket Casts on the phone page"),
		{Kind: "remote", Title: "Sign In on Your Phone", Subtitle: sub}}
}

func (a *app) upNextPage(off int) q2spot.Reply {
	if !a.pc.SignedIn() {
		return q2spot.Reply{Rows: a.signInRows()}
	}
	up, at := a.sy.upNext()
	if at.IsZero() {
		a.sy.kick()
		return live([]q2spot.Row{info("Loading…", "Asking Pocket Casts")}, "", true)
	}
	if len(up) == 0 {
		return q2spot.Reply{Rows: []q2spot.Row{info("Up Next is empty", "Hold Play/Pause on an episode to add it")}}
	}
	ctx := "album"
	if q2.HasCap("lists") { // Q2 Pod plays on through Up Next's downloaded episodes, in order
		ctx = "list:" + a.opts.upnext
	}
	var eps []pocketcasts.Episode
	for _, e := range up {
		if e.Show() != pocketcasts.FilesPodcast {
			eps = append(eps, e)
		}
	}
	rows, next := a.pcPage(eps, off, func(e pocketcasts.Episode) string { return "unq:" + pcID(e) }, ctx)
	return live(rows, next, a.anyMoving(eps))
}

// anyMoving is whether an episode of eps is downloading or waiting to, so its page is asked again.
func (a *app) anyMoving(eps []pocketcasts.Episode) bool {
	for _, e := range eps[:min(len(eps), pageSize)] {
		if ep, err := a.lib.PCEpisode(e.Show(), e.UUID); err == nil && ep.Job != nil && ep.Job.State != library.Failed {
			return true
		}
	}
	a.pending.Lock()
	defer a.pending.Unlock()
	return len(a.getting) > 0
}

func (a *app) listPage(name string, off int) q2spot.Reply {
	if !a.pc.SignedIn() {
		return q2spot.Reply{Rows: a.signInRows()}
	}
	eps, err, loading := a.sy.list(name)
	if eps == nil {
		if err != nil && !loading {
			return q2spot.Fail(q2spot.Offline, say(err))
		}
		return live([]q2spot.Row{info("Loading…", "Asking Pocket Casts")}, "", true)
	}
	if len(eps) == 0 {
		return q2spot.Reply{Rows: []q2spot.Row{info("Nothing here", listNames[name]+" is empty")}}
	}
	rows, next := a.pcPage(eps, off, func(e pocketcasts.Episode) string { return "upq:" + pcID(e) }, "album")
	return live(rows, next, loading || a.anyMoving(eps))
}

// find is an episode of the account's lists or the library, by its row argument.
func (a *app) findPC(id string) (pocketcasts.Episode, bool) {
	pod, uuid, _ := strings.Cut(id, ":")
	if ep, err := a.lib.PCEpisode(pod, uuid); err == nil {
		return asServed(ep), true
	}
	up, _ := a.sy.upNext()
	for _, e := range up {
		if e.UUID == uuid {
			return e, true
		}
	}
	for name := range listNames {
		eps, _, _ := a.sy.list(name)
		for _, e := range eps {
			if e.UUID == uuid {
				return e, true
			}
		}
	}
	return pocketcasts.Episode{}, false
}

// pcGet downloads an episode of the account, its show added (hidden) first if need be.
func (a *app) pcGet(id string) q2spot.Reply {
	e, ok := a.findPC(id)
	if !ok {
		return q2spot.Fail(q2spot.Unavailable, "That episode isn't in the list any more")
	}
	if ep, err := a.lib.PCEpisode(e.Show(), e.UUID); err == nil {
		msg, err := a.lib.DL.Queue(ep.Key, ep.EID)
		if err != nil {
			return q2spot.Fail(q2spot.Unavailable, say(err))
		}
		return q2spot.Reply{Message: msg}
	}
	a.pending.Lock()
	if a.getting == nil {
		a.getting = map[string]bool{}
	}
	a.getting[e.UUID] = true
	a.pending.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		defer cancel()
		if ep, err := a.sy.episode(ctx, e); err == nil {
			a.lib.DL.Queue(ep.Key, ep.EID)
		}
		a.pending.Lock()
		delete(a.getting, e.UUID)
		a.pending.Unlock()
	}()
	return q2spot.Reply{Message: "Downloading"}
}

// upNextAdd puts an episode at the end of Up Next.
func (a *app) upNextAdd(id string) q2spot.Reply {
	if !a.pc.SignedIn() {
		return q2spot.Fail(q2spot.Login, "Sign in to Pocket Casts first")
	}
	up, _ := a.sy.upNext()
	_, uuid, _ := strings.Cut(id, ":")
	for _, e := range up {
		if e.UUID == uuid {
			return q2spot.Reply{Message: "It's in Up Next already"}
		}
	}
	e, ok := a.findPC(id)
	if !ok {
		return q2spot.Fail(q2spot.Unavailable, "That episode isn't in the list any more")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	defer cancel()
	if err := a.pc.UpNextAdd(ctx, "last", e); err != nil {
		return q2spot.Fail(q2spot.Offline, say(err))
	}
	a.sy.kick()
	return q2spot.Reply{Message: "Added to Up Next"}
}

func (a *app) upNextRemove(id string) q2spot.Reply {
	if !a.confirm("unq:" + id) {
		return q2spot.Reply{Message: "Hold again to take it out of Up Next"}
	}
	_, uuid, _ := strings.Cut(id, ":")
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	defer cancel()
	if err := a.pc.UpNextRemove(ctx, uuid); err != nil {
		return q2spot.Fail(q2spot.Offline, say(err))
	}
	a.sy.kick()
	return q2spot.Reply{Message: "Taken out of Up Next"}
}

// Discover: Pocket Casts' lists, each show previewed from Pocket Casts' copy of it.

var discoverLists = []struct{ name, title, sub string }{
	{"trending", "Trending", "What people are starting this week"},
	{"popular", "Popular", "The most listened to"},
	{"featured", "Featured", "Pocket Casts' picks"},
}

type cached struct {
	at   time.Time
	pods []pocketcasts.Podcast
}

func (a *app) discover() q2spot.Reply {
	var rows []q2spot.Row
	for _, l := range discoverLists {
		rows = append(rows, list("dlist", l.name, l.title, l.sub))
	}
	return q2spot.Reply{Rows: rows}
}

func (a *app) dlist(name string) q2spot.Reply {
	a.pending.Lock()
	c, ok := a.lists[name]
	a.pending.Unlock()
	if !ok || time.Since(c.at) > 30*time.Minute {
		ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
		pods, err := a.pc.List(ctx, name)
		cancel()
		if err != nil {
			return q2spot.Fail(q2spot.Offline, "Can't reach Pocket Casts: check the Wi-Fi")
		}
		c = cached{time.Now(), pods}
		a.pending.Lock()
		if a.lists == nil {
			a.lists = map[string]cached{}
		}
		a.lists[name] = c
		a.pending.Unlock()
	}
	var rows []q2spot.Row
	for _, p := range c.pods[:min(len(c.pods), q2spot.MaxRows)] {
		sub := p.Author
		if si, ok := a.lib.ByPC(p.UUID); ok && !si.Hidden {
			sub = join("Subscribed", p.Author)
		}
		a.remember("pc:"+p.UUID, p.Feed)
		rows = append(rows, list("dshow", p.UUID, p.Title, sub))
	}
	return q2spot.Reply{Rows: rows}
}

// dshow is a Discover show: its episodes and Subscribe, from Pocket Casts' copy; once subscribed,
// its own page.
func (a *app) dshow(uuid string, off int) q2spot.Reply {
	if si, ok := a.lib.ByPC(uuid); ok && !si.Hidden {
		return a.show(si.Key, off)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	sh, _, _, err := a.pc.Full(ctx, uuid, "")
	cancel()
	if err != nil {
		return q2spot.Fail(q2spot.Offline, "Can't reach Pocket Casts: check the Wi-Fi")
	}
	rows := []q2spot.Row{act("dsub", uuid, "Subscribe", join(plural(len(sh.Episodes), "episode"), sh.Author))}
	for i, e := range sh.Episodes {
		if i == pageSize {
			break
		}
		when, _ := time.Parse(time.RFC3339, e.Published)
		rows = append(rows, info(e.Title, join(day(when), length(int(e.Duration)))))
	}
	return q2spot.Reply{Rows: rows}
}

// dsub subscribes to a Discover show: in Pocket Casts when signed in, else by its feed.
func (a *app) dsub(uuid string) q2spot.Reply {
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	defer cancel()
	if !a.pc.SignedIn() {
		a.mu.Lock()
		feed := a.found["pc:"+uuid]
		a.mu.Unlock()
		if feed == "" {
			return q2spot.Fail(q2spot.Unavailable, "Search for it by name instead")
		}
		si, err := a.lib.Subscribe(ctx, feed)
		if err != nil {
			return q2spot.Fail(q2spot.Error, say(err))
		}
		return q2spot.Reply{Message: "Subscribed to " + si.Title}
	}
	if err := a.pc.Subscribe(ctx, uuid); err != nil {
		return q2spot.Fail(q2spot.Offline, say(err))
	}
	sh, _, _, _ := a.pc.Full(ctx, uuid, "")
	title := ""
	if sh != nil {
		title = sh.Title
	}
	si := a.lib.AdoptPC(pocketcasts.Podcast{UUID: uuid, Title: title}, false)
	go a.lib.Refresh(a.ctx, si.Key)
	return q2spot.Reply{Message: "Subscribed to " + si.Title}
}

// Ads & Sponsors.

var cutStats struct {
	sync.Mutex
	at                time.Time
	eps, ads, sponsor int
	sec               float64
	known             int
}

// cutSoFar is what the cleaning has cut, from its reports, counted at most once a minute.
func (a *app) cutSoFar() (eps, ads, sponsor int, sec float64, known int) {
	c := &cutStats
	c.Lock()
	defer c.Unlock()
	if time.Since(c.at) > time.Minute {
		c.eps, c.ads, c.sponsor, c.sec = 0, 0, 0, 0
		ents, _ := os.ReadDir(filepath.Join(a.opts.dir, "cuts"))
		for _, d := range ents {
			key, rest, _ := strings.Cut(strings.TrimSuffix(d.Name(), ".json"), "-")
			r := clean.Load(a.opts.dir, key, rest)
			if n, s, sec := r.Removed(); n+s > 0 {
				c.eps++
				c.ads += n
				c.sponsor += s
				c.sec += sec
			}
		}
		c.known = adfree.LoadLibrary(filepath.Join(a.opts.dir, "ads.bin")).Len()
		c.at = time.Now()
	}
	return c.eps, c.ads, c.sponsor, c.sec, c.known
}

func onOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

func (a *app) adfreePage() q2spot.Reply {
	s := a.settings()
	eps, ads, sp, sec, known := a.cutSoFar()
	sb := "Show notes' YouTube and Spotify links"
	var keys []string
	if s.YouTubeKey != "" {
		keys = append(keys, "YouTube")
	}
	if s.SpotifyID != "" && s.SpotifySecret != "" {
		keys = append(keys, "Spotify")
	}
	if len(keys) > 0 {
		sb += ", and finds them with your " + strings.Join(keys, " and ") + " key"
	} else {
		sb += " (add YouTube or Spotify keys on the phone page)"
	}
	upn := "Off"
	if s.UpNext > 0 {
		upn = fmt.Sprintf("The First %d", s.UpNext)
	}
	rows := []q2spot.Row{
		act("set", "ads", "Remove Ads: "+onOff(s.Ads), "The ads a podcast's host puts in as it serves it"),
		act("set", "sponsors", "Remove Sponsor Reads: "+onOff(s.Sponsors), "From SponsorBlock, transcripts and chapters"),
		act("set", "selfpromo", "Also Remove Self-Promotion: "+onOff(s.SelfPromo), "A show's plugs for itself, where SponsorBlock has them"),
		act("set", "copy", "Compare with a Second Copy: "+onOff(s.Copy), "Downloads one when the host would send other ads"),
		info("SponsorBlock", sb),
		info("So Far", join(plural(ads, "ad")+" and "+plural(sp, "sponsor read")+" cut", length(int(sec)), "from "+plural(eps, "episode"))),
		info("Ads Learned", fmt.Sprintf("%d, recognised wherever they play again", known)),
	}
	if a.pc.SignedIn() {
		rows = append(rows, act("set", "upnext", "Download Up Next: "+upn, "Kept downloaded, in order"),
			act("set", "deleteplayed", "Delete Played Episodes: "+onOff(s.DeletePlayed), "Once Pocket Casts marks them played"))
	}
	return q2spot.Reply{Rows: rows}
}

var upNextChoices = []int{0, 3, 5, 10, 20, 50}

func (a *app) setOption(name string) q2spot.Reply {
	msg := ""
	err := a.change(func(s *settings) {
		switch name {
		case "ads":
			s.Ads = !s.Ads
			msg = "Ads: " + onOff(s.Ads)
		case "sponsors":
			s.Sponsors = !s.Sponsors
			msg = "Sponsor reads: " + onOff(s.Sponsors)
		case "selfpromo":
			s.SelfPromo = !s.SelfPromo
			msg = "Self-promotion: " + onOff(s.SelfPromo)
		case "copy":
			s.Copy = !s.Copy
			msg = "Second copy: " + onOff(s.Copy)
		case "deleteplayed":
			s.DeletePlayed = !s.DeletePlayed
			msg = "Delete played: " + onOff(s.DeletePlayed)
		case "upnext":
			next := upNextChoices[0]
			for i, n := range upNextChoices {
				if n == s.UpNext && i+1 < len(upNextChoices) {
					next = upNextChoices[i+1]
				}
			}
			s.UpNext = next
			msg = fmt.Sprintf("Up Next: the first %d downloaded", next)
			if next == 0 {
				msg = "Up Next isn't downloaded"
			}
		}
	})
	if err != nil {
		return q2spot.Fail(q2spot.Error, "Couldn't save the setting")
	}
	if name == "upnext" {
		a.sy.kick()
	}
	if msg == "" {
		return q2spot.Fail(q2spot.Bad, "No such setting")
	}
	return q2spot.Reply{Message: msg}
}

// syncRow is the menu's Sync Now: when the last sync worked, or why it didn't.
func (a *app) syncRow() q2spot.Row {
	at, err, busy := a.sy.status()
	sub := "Synced " + ago(at)
	switch {
	case busy:
		sub = "Syncing…"
	case err != "":
		sub = err
	case at.IsZero():
		sub = "Not synced yet"
	}
	return act("sync", "", "Sync Now", sub)
}

// filesPage is the account's uploaded files (Pocket Casts Plus).
func (a *app) filesPage() q2spot.Reply {
	if !a.pc.SignedIn() {
		return q2spot.Reply{Rows: a.signInRows()}
	}
	files, err, loading := a.sy.uploaded()
	switch {
	case errors.Is(err, pocketcasts.ErrPlus):
		return q2spot.Reply{Rows: []q2spot.Row{info("Files need Pocket Casts Plus", "Uploaded files are a Plus feature")}}
	case files == nil && err != nil && !loading:
		return q2spot.Fail(q2spot.Offline, say(err))
	case files == nil:
		return live([]q2spot.Row{info("Loading…", "Asking Pocket Casts")}, "", true)
	case len(files) == 0:
		return q2spot.Reply{Rows: []q2spot.Row{info("No files", "Upload them in Pocket Casts")}}
	}
	var rows []q2spot.Row
	for _, f := range files[:min(len(files), q2spot.MaxRows)] {
		if p := a.filePath(f); fileThere(p) {
			rows = append(rows, q2spot.Row{Kind: "local", URI: p, Title: f.Title, Subtitle: join(length(int(f.Duration)), "Downloaded")})
			continue
		}
		state := a.fileState(f.UUID)
		rows = append(rows, act("fget", f.UUID, f.Title, join(length(int(f.Duration)), size(int64(f.Size)), cmpOr(state, "Download"))))
	}
	return live(rows, "", loading || a.filesMoving())
}

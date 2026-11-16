package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/library"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/sponsor"
)

// The phone page's Pocket Casts calls, /api/pc/…, behind the same key and X-Q2 header as the rest:
//
//	GET  account                 signed in, the email, the last sync
//	POST login {email, password} signs in (the password goes to Pocket Casts, never to the card)
//	POST logout, sync
//	GET  settings; POST settings {…}  the cleaning's and Up Next's settings; keys are write-only
//	GET  upnext?page=&filter=&q=; GET list?name=inprog|new|starred|history&page=&filter=&q=
//	     (a page of perPage; filter: oncard, notoncard, downloading, failed, notremoved; q: a
//	     search of titles and shows, matchName)
//	POST episode {podcast, uuid, action: next|last|remove|star|unstar|played|unplayed|download}
//	GET  files; POST file {uuid, action: download|delete}
//	GET  discover?name=; POST discover {uuid}
//	GET  report?key=&eid=        what was cut from an episode, and why
//	GET  notes?podcast=&uuid=    an episode's show notes, as text, and its chapters

type pcEpisodeJSON struct {
	Podcast    string    `json:"podcast"`
	UUID       string    `json:"uuid"`
	Title      string    `json:"title"`
	Show       string    `json:"show"`
	Published  time.Time `json:"published"`
	Duration   int       `json:"duration"`
	PlayedUpTo int       `json:"playedUpTo"`
	Status     int       `json:"status"`
	Starred    bool      `json:"starred"`
	Key        string    `json:"key,omitempty"`
	EID        string    `json:"eid,omitempty"`
	OnCard     bool      `json:"onCard"`
	Job        *jobJSON  `json:"job,omitempty"`
	Cut        string    `json:"cut,omitempty"`
	NotRemoved string    `json:"notRemoved,omitempty"` // on the Q2 with its ads: why (Ads Not Removed)
	UpNext     bool      `json:"upNext"`
	Getting    bool      `json:"getting,omitempty"`   // a press's lookup under way (its show being read)
	GetFailed  string    `json:"getFailed,omitempty"` // why a press couldn't download it
}

// pcEpisodes is a page of the account's episodes as the phone page shows them, the library looked
// up once for the page (each show's folder read once) and only the page's reports read.
func (w *web) pcEpisodes(eps []pocketcasts.Episode) []pcEpisodeJSON {
	a := w.a
	up, _ := a.sy.upNext()
	in := make(map[string]bool, len(up))
	for _, e := range up {
		in[e.UUID] = true
	}
	found, ok := a.lib.PCEpisodes(pcIDs(eps))
	var not map[string]string
	a.pending.Lock()
	getting, failed := map[string]bool{}, map[string]string{}
	for _, e := range eps {
		if a.getting[e.UUID] {
			getting[e.UUID] = true
		}
		if f := a.getFailed[e.UUID]; f != "" {
			failed[e.UUID] = f
		}
	}
	a.pending.Unlock()
	out := make([]pcEpisodeJSON, 0, len(eps))
	for i, e := range eps {
		j := pcEpisodeJSON{Podcast: e.Show(), UUID: e.UUID, Title: e.Title, Show: cmpOr(a.sy.title(e.Show()), e.PodcastTitle),
			Published: e.Date(), Duration: int(e.Duration), PlayedUpTo: int(e.PlayedUpTo), Status: int(e.Status),
			Starred: e.Starred, UpNext: in[e.UUID], Getting: getting[e.UUID], GetFailed: failed[e.UUID]}
		if ep := found[i]; ok[i] {
			j.Key, j.EID, j.OnCard = ep.Key, ep.EID, ep.OnCard
			if j.Duration == 0 {
				j.Duration = ep.Duration
			}
			if ep.Job != nil {
				jj := jobOut(*ep.Job)
				j.Job = &jj
			}
			if ep.OnCard {
				r := clean.Load(a.opts.dir, ep.Key, ep.EID)
				if r.Shown() {
					j.Cut = r.Summary()
				}
				if not == nil {
					not = a.onCard().not
				}
				j.NotRemoved = not[ep.Key+":"+ep.EID]
			}
		}
		out = append(out, j)
	}
	return out
}

// perPage is the episodes a page of the phone page's lists holds (the user's, 2026-11-16: Up Next's
// 1,372 were cut at 200, and each look at it read every one's state off the card).
const perPage = 50

// pageOf is page (from 0, clamped) of n items: its bounds, and how many pages there are.
func pageOf(n, page int) (from, to, at, pages int) {
	pages = max(1, (n+perPage-1)/perPage)
	at = min(max(page, 0), pages-1)
	from = at * perPage
	return from, min(from+perPage, n), at, pages
}

// The phone page's filters of a list of the account's episodes: each by its state on the Q2.
var pcFilters = map[string]func(ep library.EpisodeInfo, found bool, not map[string]string) bool{
	"oncard":    func(ep library.EpisodeInfo, found bool, _ map[string]string) bool { return found && ep.OnCard },
	"notoncard": func(ep library.EpisodeInfo, found bool, _ map[string]string) bool { return !found || !ep.OnCard },
	"downloading": func(ep library.EpisodeInfo, found bool, _ map[string]string) bool {
		return found && ep.Job != nil && ep.Job.State != library.Failed
	},
	"failed": func(ep library.EpisodeInfo, found bool, _ map[string]string) bool {
		return found && ep.Job != nil && ep.Job.State == library.Failed
	},
	"notremoved": func(ep library.EpisodeInfo, found bool, not map[string]string) bool {
		_, still := not[ep.Key+":"+ep.EID]
		return found && ep.OnCard && still
	},
}

// filtered is eps in filter (pcFilters; "" or unknown: all of them).
func (w *web) filtered(eps []pocketcasts.Episode, filter string) []pocketcasts.Episode {
	keep, ok := pcFilters[filter]
	if !ok {
		return eps
	}
	found, has := w.a.lib.PCEpisodes(pcIDs(eps))
	not := w.a.onCard().not
	var out []pocketcasts.Episode
	for i, e := range eps {
		if keep(found[i], has[i], not) {
			out = append(out, e)
		}
	}
	return out
}

// pcPageJSON is a page of a list of the account's episodes: filtered, then paged.
func (w *web) pcPageJSON(eps []pocketcasts.Episode, q url.Values) map[string]any {
	all := len(eps)
	if name := q.Get("q"); name != "" { // a search: the episodes named so
		var named []pocketcasts.Episode
		for _, e := range eps {
			if matchName(name, e.Title, cmpOr(w.a.sy.title(e.Show()), e.PodcastTitle)) {
				named = append(named, e)
			}
		}
		eps = named
	}
	eps = w.filtered(eps, q.Get("filter"))
	page, _ := strconv.Atoi(q.Get("page"))
	from, to, at, pages := pageOf(len(eps), page)
	return map[string]any{"episodes": w.pcEpisodes(eps[from:to]), "total": len(eps), "all": all, "page": at,
		"pages": pages, "perPage": perPage, "from": from}
}

// downloading is what the downloads are doing, for the top of the phone page's Up Next: the one
// under way (with its progress, or what it is doing once downloaded) and how many wait or failed.
func (w *web) downloading() map[string]any {
	var active []jobJSON
	waiting, failed := 0, 0
	for _, j := range w.a.lib.DL.Jobs() {
		switch j.State {
		case library.Active:
			active = append(active, jobOut(j))
		case library.Failed:
			failed++
		default:
			waiting++
		}
	}
	return map[string]any{"active": active, "waiting": waiting, "failed": failed}
}

type pcBody struct {
	Email, Password string
	Podcast, UUID   string
	Action          string
	Name            string

	Ads, Sponsors, SelfPromo, Copy, DeletePlayed, TwoWay *bool
	UpNext                                               *int
	NextCount                                            *int // Download Next's count, chosen on the page
	Count                                                int  // Download Next's (0: stop)
	YouTubeKey, SpotifyID, SpotifySecret                 *string
}

func (w *web) pc(rw http.ResponseWriter, r *http.Request, route string, post bool, raw []byte) {
	a := w.a
	var body pcBody
	if post && len(raw) > 0 && json.Unmarshal(raw, &body) != nil {
		refuse(rw, http.StatusBadRequest, "bad body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	fail := func(err error) {
		code := http.StatusBadGateway
		if errors.Is(err, pocketcasts.ErrSignIn) || errors.Is(err, pocketcasts.ErrWrong) {
			code = http.StatusForbidden
		}
		refuse(rw, code, say(err))
	}
	ok := func() { reply(rw, http.StatusOK, map[string]bool{"ok": true}) }
	switch {
	case route == "account" && !post:
		at, err, busy := a.sy.status()
		t := a.pc.Token()
		reply(rw, http.StatusOK, map[string]any{"signedIn": t.Access != "", "email": t.Email,
			"sync": map[string]any{"at": at, "error": err, "busy": busy}})
	case route == "login" && post:
		if strings.TrimSpace(body.Email) == "" || body.Password == "" {
			refuse(rw, http.StatusBadRequest, "Type your Pocket Casts email and password")
			return
		}
		t, err := a.pc.Login(ctx, body.Email, body.Password)
		if err != nil {
			fail(err)
			return
		}
		saveAccount(a.opts.dir, t)
		a.sy.kick()
		ok()
	case route == "logout" && post:
		a.pc.SignOut()
		saveAccount(a.opts.dir, pocketcasts.Token{})
		a.sy.signedOut()
		ok()
	case route == "sync" && post:
		a.sy.kick()
		ok()
	case route == "settings" && !post:
		s := a.settings()
		eps, ads, sp, sec, known := a.cutSoFar()
		reply(rw, http.StatusOK, map[string]any{"ads": true, "sponsors": true, "selfPromo": true,
			"copy": true, "upNext": s.UpNext, "deletePlayed": s.DeletePlayed, "twoWay": s.TwoWay, "youtube": s.YouTubeKey != "",
			"spotify": s.SpotifyID != "" && s.SpotifySecret != "", "idleMinutes": s.IdleMinutes,
			"cut": map[string]any{"episodes": eps, "ads": ads, "sponsors": sp, "seconds": sec, "learned": known}})
	case route == "settings" && post:
		err := a.change(func(s *settings) {
			set := func(p *bool, v *bool) {
				if v != nil {
					*p = *v
				}
			}
			set(&s.DeletePlayed, body.DeletePlayed)                                     // ad removal is always on: its switches are no more
			if body.NextCount != nil && slices.Contains(nextChoices, *body.NextCount) { // Download Next's count, chosen
				s.NextCount = *body.NextCount
			}
			set(&s.TwoWay, body.TwoWay)
			if body.UpNext != nil {
				s.UpNext = min(max(*body.UpNext, -1), 10000)
			}
			for _, kv := range []struct {
				p *string
				v *string
			}{{&s.YouTubeKey, body.YouTubeKey}, {&s.SpotifyID, body.SpotifyID}, {&s.SpotifySecret, body.SpotifySecret}} {
				if kv.v != nil {
					*kv.p = strings.TrimSpace(*kv.v)
				}
			}
		})
		if err != nil {
			refuse(rw, http.StatusInternalServerError, "Couldn't save the settings")
			return
		}
		if body.UpNext != nil {
			a.sy.kick()
		}
		ok()
	case route == "upnext" && !post:
		up, at := a.sy.upNext()
		if at.IsZero() && a.pc.SignedIn() {
			a.sy.kick()
		}
		out := w.pcPageJSON(up, r.URL.Query())
		out["loading"], out["downloadingAll"], out["nextLeft"] = at.IsZero(), a.sy.allPending(), a.sy.nextPending()
		out["nextCount"], out["nextChoices"], out["downloads"] = a.settings().nextCount(), nextChoices, w.downloading()
		reply(rw, http.StatusOK, out)
	case route == "downloadnext" && post: // Up Next's Download Next, as the Q2's: count, or 0 to stop
		if body.Count <= 0 {
			a.sy.setNext(0)
			reply(rw, http.StatusOK, map[string]any{"message": "Stopped downloading the next episodes"})
			return
		}
		body.Count = min(body.Count, 50)
		a.change(func(s *settings) {
			for _, c := range nextChoices {
				if c == body.Count {
					s.NextCount = c
				}
			}
		})
		a.sy.downloadNext(body.Count)
		reply(rw, http.StatusOK, map[string]any{"message": "Downloading the next " + plural(body.Count, "episode")})
	case route == "downloadall" && post: // Up Next's Download All, as the Q2's (no second press: the page asks)
		on := !a.sy.allPending()
		a.sy.downloadAll(on)
		msg := "Stopped downloading all of Up Next"
		if on {
			msg = "Downloading all of Up Next"
		}
		reply(rw, http.StatusOK, map[string]any{"message": msg, "downloadingAll": on})
	case route == "list" && !post:
		name := r.URL.Query().Get("name")
		if _, known := listNames[name]; !known {
			refuse(rw, http.StatusNotFound, "no such list")
			return
		}
		eps, err, loading := a.sy.list(name)
		out := w.pcPageJSON(eps, r.URL.Query())
		out["loading"] = loading || eps == nil && err == nil
		if err != nil {
			out["error"] = say(err)
		}
		reply(rw, http.StatusOK, out)
	case route == "episode" && post:
		w.pcAction(rw, ctx, body)
	case route == "files" && !post:
		files, err, loading := a.sy.uploaded()
		out := []map[string]any{}
		for _, f := range files {
			out = append(out, map[string]any{"uuid": f.UUID, "title": f.Title, "duration": int(f.Duration),
				"size": int64(f.Size), "onCard": fileThere(a.filePath(f)), "state": a.fileState(f.UUID)})
		}
		res := map[string]any{"loading": loading || files == nil && err == nil, "files": out}
		if err != nil {
			res["error"] = say(err)
		}
		reply(rw, http.StatusOK, res)
	case route == "file" && post:
		switch body.Action {
		case "download":
			reply(rw, http.StatusOK, map[string]string{"message": a.fileGet(body.UUID)})
		case "delete":
			if err := a.pc.DeleteFile(ctx, body.UUID); err != nil {
				fail(err)
				return
			}
			a.sy.dropFiles()
			ok()
		default:
			refuse(rw, http.StatusBadRequest, "bad action")
		}
	case route == "discover" && !post:
		pods, err := a.pc.List(ctx, r.URL.Query().Get("name"))
		if err != nil {
			fail(errors.New("can't reach Pocket Casts"))
			return
		}
		out := []map[string]any{}
		for _, p := range pods {
			si, sub := a.lib.ByPC(p.UUID)
			out = append(out, map[string]any{"uuid": p.UUID, "title": p.Title, "author": p.Author,
				"image": pocketcasts.ArtURL(p.UUID), "subscribed": sub && !si.Hidden, "feed": p.Feed})
		}
		reply(rw, http.StatusOK, out)
	case route == "discover" && post:
		res := a.dsub(body.UUID)
		if res.Status != "" && res.Status != "ok" {
			refuse(rw, http.StatusBadGateway, res.Message)
			return
		}
		reply(rw, http.StatusOK, map[string]string{"message": res.Message})
	case route == "notes" && !post:
		q := r.URL.Query()
		n, _, err := a.pc.EpisodeNotes(ctx, q.Get("podcast"), q.Get("uuid"))
		if err != nil {
			fail(errors.New("can't reach Pocket Casts"))
			return
		}
		out := map[string]any{"text": clean.NotesText(n.Text), "link": n.Link}
		if n.ChaptersURL != "" {
			if b, err := a.pc.Get(ctx, n.ChaptersURL, 1<<20); err == nil {
				var ch []map[string]any
				for _, c := range sponsor.JSONChapters(b) {
					ch = append(ch, map[string]any{"start": c.From, "title": c.Title})
				}
				out["chapters"] = ch
			}
		}
		reply(rw, http.StatusOK, out)
	case route == "report" && !post:
		q := r.URL.Query()
		rep := clean.Load(a.opts.dir, q.Get("key"), q.Get("eid"))
		if rep == nil {
			refuse(rw, http.StatusNotFound, "Not checked")
			return
		}
		reply(rw, http.StatusOK, rep)
	default:
		http.NotFound(rw, r)
	}
}

// pcAction is an episode's action from the phone page.
func (w *web) pcAction(rw http.ResponseWriter, ctx context.Context, b pcBody) {
	a := w.a
	id := b.Podcast + ":" + b.UUID
	var err error
	msg := ""
	switch b.Action {
	case "next", "last":
		e, found := a.findPC(id)
		if !found {
			refuse(rw, http.StatusNotFound, "That episode isn't in a list any more")
			return
		}
		err, msg = a.pc.UpNextAdd(ctx, b.Action, e), "Added to Up Next"
		if err == nil { // in the Q2's Up Next now, where asked
			a.sy.placed(e, b.Action, a.sy.playingUUID())
			a.sy.save()
		}
	case "remove":
		err, msg = a.pc.UpNextRemove(ctx, b.UUID), "Taken out of Up Next"
		if err == nil {
			a.sy.unplaced(b.UUID)
			a.sy.save()
		}
	case "star", "unstar":
		err, msg = a.pc.Star(ctx, b.Podcast, b.UUID, b.Action == "star"), "Starred"
		if b.Action == "unstar" {
			msg = "Star taken away"
		}
	case "played":
		err, msg = a.pc.SetStatus(ctx, b.Podcast, b.UUID, pocketcasts.Played), "Marked played"
	case "unplayed":
		err, msg = a.pc.SetStatus(ctx, b.Podcast, b.UUID, pocketcasts.Unplayed), "Marked unplayed"
	case "download":
		res := a.pcGet(id)
		if res.Status != "" && res.Status != "ok" {
			refuse(rw, http.StatusBadGateway, res.Message)
			return
		}
		msg = res.Message
	default:
		refuse(rw, http.StatusBadRequest, "bad action")
		return
	}
	if err != nil {
		refuse(rw, http.StatusBadGateway, say(err))
		return
	}
	a.sy.forget()
	a.sy.kick()
	reply(rw, http.StatusOK, map[string]string{"message": msg})
}

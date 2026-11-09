package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/sponsor"
)

// The phone page's Pocket Casts calls, /api/pc/…, behind the same key and X-Q2 header as the rest:
//
//	GET  account                 signed in, the email, the last sync
//	POST login {email, password} signs in (the password goes to Pocket Casts, never to the card)
//	POST logout, sync
//	GET  settings; POST settings {…}  the cleaning's and Up Next's settings; keys are write-only
//	GET  upnext; GET list?name=inprog|new|starred|history
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
	UpNext     bool      `json:"upNext"`
}

func (w *web) pcEpisodes(eps []pocketcasts.Episode) []pcEpisodeJSON {
	a := w.a
	up, _ := a.sy.upNext()
	in := map[string]bool{}
	for _, e := range up {
		in[e.UUID] = true
	}
	out := make([]pcEpisodeJSON, 0, len(eps))
	for _, e := range eps {
		j := pcEpisodeJSON{Podcast: e.Show(), UUID: e.UUID, Title: e.Title, Show: cmpOr(a.sy.title(e.Show()), e.PodcastTitle),
			Published: e.Date(), Duration: int(e.Duration), PlayedUpTo: int(e.PlayedUpTo), Status: int(e.Status),
			Starred: e.Starred, UpNext: in[e.UUID]}
		if ep, err := a.lib.PCEpisode(e.Show(), e.UUID); err == nil {
			j.Key, j.EID, j.OnCard = ep.Key, ep.EID, ep.OnCard
			if j.Duration == 0 {
				j.Duration = ep.Duration
			}
			if ep.Job != nil {
				jj := jobOut(*ep.Job)
				j.Job = &jj
			}
			if ep.OnCard {
				if r := clean.Load(a.opts.dir, ep.Key, ep.EID); r != nil {
					if ads, sp, _ := r.Removed(); ads+sp > 0 {
						j.Cut = r.Summary()
					}
				}
			}
		}
		out = append(out, j)
	}
	return out
}

type pcBody struct {
	Email, Password string
	Podcast, UUID   string
	Action          string
	Name            string

	Ads, Sponsors, SelfPromo, Copy, DeletePlayed *bool
	UpNext                                       *int
	YouTubeKey, SpotifyID, SpotifySecret         *string
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
		reply(rw, http.StatusOK, map[string]any{"ads": s.Ads, "sponsors": s.Sponsors, "selfPromo": s.SelfPromo,
			"copy": s.Copy, "upNext": s.UpNext, "deletePlayed": s.DeletePlayed, "youtube": s.YouTubeKey != "",
			"spotify": s.SpotifyID != "" && s.SpotifySecret != "", "idleMinutes": s.IdleMinutes,
			"cut": map[string]any{"episodes": eps, "ads": ads, "sponsors": sp, "seconds": sec, "learned": known}})
	case route == "settings" && post:
		err := a.change(func(s *settings) {
			set := func(p *bool, v *bool) {
				if v != nil {
					*p = *v
				}
			}
			set(&s.Ads, body.Ads)
			set(&s.Sponsors, body.Sponsors)
			set(&s.SelfPromo, body.SelfPromo)
			set(&s.Copy, body.Copy)
			set(&s.DeletePlayed, body.DeletePlayed)
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
		reply(rw, http.StatusOK, map[string]any{"loading": at.IsZero(), "episodes": w.pcEpisodes(up)})
	case route == "list" && !post:
		name := r.URL.Query().Get("name")
		if _, known := listNames[name]; !known {
			refuse(rw, http.StatusNotFound, "no such list")
			return
		}
		eps, err, loading := a.sy.list(name)
		out := map[string]any{"loading": loading || eps == nil && err == nil, "episodes": w.pcEpisodes(eps)}
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
		notes, err := a.pc.ShowNotes(ctx, q.Get("podcast"))
		if err != nil {
			fail(errors.New("can't reach Pocket Casts"))
			return
		}
		n := notes[q.Get("uuid")]
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
	case "remove":
		err, msg = a.pc.UpNextRemove(ctx, b.UUID), "Taken out of Up Next"
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

package main

import (
	"strings"
	"sync"
	"time"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/library"
	"q2-pocket/internal/pocketcasts"
)

// The episodes on the Q2, and which of them still have their ads (the user's rule, 2026-11-16: every
// episode is checked for ads as fully as can be, or, where it can't be (too little memory, Pocket
// crashed checking it), it goes on the Q2 as it came, marked Ads Not Removed, found by a filter on
// the phone page and on the Q2 and fetched again with Remove Ads Again when the user asks).
//
// Reading every show's folder and every episode's report takes the card a while, so the list is
// kept a few seconds; the pages poll often.

const cardFor = 10 * time.Second

type cardList struct {
	at  time.Time
	eps []library.EpisodeInfo // on the Q2, newest first
	not map[string]string     // "<key>:<eid>": why its ads weren't removed
}

var card struct {
	sync.Mutex
	l cardList
}

// notRemoved is whether an episode on the Q2 still has its ads, and why: no report (it was never
// checked) or one marked unchecked.
func notRemoved(r *clean.Report) (string, bool) {
	switch {
	case r == nil:
		return "Ads not removed: it wasn't checked for ads", true
	case r.Unchecked:
		if !strings.HasPrefix(r.Note, "Ads not removed") {
			return "Ads not removed: " + r.Note, true
		}
		return r.Note, true
	}
	return "", false
}

// onCard is the episodes on the Q2 and those whose ads weren't removed, at most cardFor old.
func (a *app) onCard() cardList {
	card.Lock()
	defer card.Unlock()
	if time.Since(card.l.at) < cardFor && card.l.eps != nil {
		return card.l
	}
	eps, _ := a.lib.Downloaded()
	not := map[string]string{}
	for _, e := range eps {
		if why, ok := notRemoved(clean.Load(a.opts.dir, e.Key, e.EID)); ok {
			not[e.Key+":"+e.EID] = why
		}
	}
	if eps == nil {
		eps = []library.EpisodeInfo{}
	}
	card.l = cardList{at: time.Now(), eps: eps, not: not}
	return card.l
}

// cardChanged lets the next onCard read the card again.
func cardChanged() {
	card.Lock()
	card.l.at = time.Time{}
	card.Unlock()
}

// notRemovedEps is the episodes on the Q2 whose ads weren't removed, newest first, each with why.
func (a *app) notRemovedEps() ([]library.EpisodeInfo, map[string]string) {
	c := a.onCard()
	var out []library.EpisodeInfo
	for _, e := range c.eps {
		if _, ok := c.not[e.Key+":"+e.EID]; ok {
			out = append(out, e)
		}
	}
	return out, c.not
}

// redoAll is Remove Ads Again for every episode whose ads weren't removed (one being fetched again
// already left as it is): how many were asked for.
func (a *app) redoAll() int {
	eps, _ := a.notRemovedEps()
	n := 0
	for _, e := range eps {
		if a.lib.DL.Job(e.Key, e.EID) != nil {
			continue
		}
		if _, err := a.lib.DL.Redo(e.Key, e.EID); err == nil {
			n++
		}
	}
	cardChanged()
	return n
}

// pcIDs is episodes as the library's batch lookup takes them.
func pcIDs(eps []pocketcasts.Episode) []library.PCID {
	out := make([]library.PCID, len(eps))
	for i, e := range eps {
		out[i] = library.PCID{Podcast: e.Show(), UUID: e.UUID}
	}
	return out
}

// left is how many of Up Next's episodes aren't on the Q2 or on their way, counted at most every
// cardFor (the Q2's Up Next page asks at every refresh).
var leftCount struct {
	sync.Mutex
	at time.Time
	n  int
	of int
}

func (a *app) leftOf(eps []pocketcasts.Episode) int {
	leftCount.Lock()
	defer leftCount.Unlock()
	if time.Since(leftCount.at) < cardFor && leftCount.of == len(eps) {
		return leftCount.n
	}
	found, ok := a.lib.PCEpisodes(pcIDs(eps))
	n := 0
	for i, ep := range found {
		if !ok[i] || !ep.OnCard && ep.Job == nil {
			n++
		}
	}
	leftCount.at, leftCount.n, leftCount.of = time.Now(), n, len(eps)
	return n
}

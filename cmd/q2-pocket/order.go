package main

import (
	"slices"

	"q2-pocket/internal/pocketcasts"
)

// The Q2's own Up Next (the user's, 2026-11-15: no two-way sync, the Q2 only takes the account's
// changes; an episode new to Up Next goes to the top of the Q2's). A sync takes Pocket Casts' Up Next
// and sends nothing back: an episode new to it goes first in the Q2's (after the episode the Q2
// started last, while that is first), one taken out of it elsewhere leaves the Q2's, and one the Q2
// played to its end leaves the Q2's only, staying in the account's. The Up Next page, its downloads
// and its playing all go by the Q2's. sync.json keeps the order (q2_up_next), what the account's
// had at the last sync (up_next_seen) and those the Q2 finished (finished_on_q2).

// reorder is the Q2's Up Next for the account's (server, in its order), the state kept for the next
// sync. The first sync with none kept takes the account's order as it is. playing is the episode
// the Q2 started last ("" none): new ones go after it while it is first.
func (s *syncer) reorder(server []pocketcasts.Episode, playing string) []pocketcasts.Episode {
	s.mu.Lock()
	defer s.mu.Unlock()
	by := make(map[string]pocketcasts.Episode, len(server))
	for _, e := range server {
		by[e.UUID] = e
	}
	first := s.st.Seen == nil
	var kept []string
	in := map[string]bool{}
	for _, u := range s.st.Order { // still in the account's, not finished here
		if _, ok := by[u]; ok && !s.st.Finished[u] && !in[u] {
			kept, in[u] = append(kept, u), true
		}
	}
	var fresh, late []string
	for _, e := range server {
		switch {
		case in[e.UUID] || s.st.Finished[e.UUID]:
		case first || s.st.Seen[e.UUID]: // the account's own order (seen, but not in the Q2's yet)
			late = append(late, e.UUID)
		default: // new to the account's Up Next since the last sync: the top of the Q2's
			fresh = append(fresh, e.UUID)
		}
	}
	at := 0
	if len(kept) > 0 && kept[0] == playing {
		at = 1
	}
	order := slices.Concat(kept[:at], fresh, kept[at:], late)
	seen := make(map[string]bool, len(server))
	for _, e := range server {
		seen[e.UUID] = true
	}
	for u := range s.st.Finished { // forgotten once the account's Up Next lets it go too
		if !seen[u] {
			delete(s.st.Finished, u)
		}
	}
	s.st.Order, s.st.Seen = order, seen
	out := make([]pocketcasts.Episode, 0, len(order))
	for _, u := range order {
		out = append(out, by[u])
	}
	return out
}

// finishedHere takes an episode the Q2 played to its end out of the Q2's Up Next (the account's
// keeps it: nothing is sent).
func (s *syncer) finishedHere(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Finished == nil {
		s.st.Finished = map[string]bool{}
	}
	s.st.Finished[uuid] = true
	if s.st.Playing == uuid {
		s.st.Playing = ""
	}
	s.st.Order = slices.DeleteFunc(s.st.Order, func(u string) bool { return u == uuid })
	s.upnext = slices.DeleteFunc(slices.Clone(s.upnext), func(e pocketcasts.Episode) bool { return e.UUID == uuid })
}

// startedHere puts the episode the Q2 started first in the Q2's Up Next, as the apps put what plays
// at the top of theirs; one not in it is left out.
func (s *syncer) startedHere(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.Index(s.st.Order, uuid); i > 0 {
		s.st.Order = slices.Insert(slices.Delete(s.st.Order, i, i+1), 0, uuid)
	}
	if i := slices.IndexFunc(s.upnext, func(e pocketcasts.Episode) bool { return e.UUID == uuid }); i > 0 {
		e := s.upnext[i]
		s.upnext = slices.Insert(slices.Delete(slices.Clone(s.upnext), i, i+1), 0, e)
	}
}

// placed is an episode the user put in the account's Up Next from Pocket (here or on the phone
// page): in the Q2's at once, first (after the one playing) for "next" or "now", else last, and taken
// as seen, so the next sync leaves it there.
func (s *syncer) placed(e pocketcasts.Episode, where string, playing string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Seen == nil {
		s.st.Seen = map[string]bool{}
	}
	s.st.Seen[e.UUID] = true
	delete(s.st.Finished, e.UUID)
	s.st.Order = slices.DeleteFunc(s.st.Order, func(u string) bool { return u == e.UUID })
	list := slices.DeleteFunc(slices.Clone(s.upnext), func(x pocketcasts.Episode) bool { return x.UUID == e.UUID })
	at := len(s.st.Order)
	if where != "last" {
		at = 0
		if len(s.st.Order) > 0 && s.st.Order[0] == playing {
			at = 1
		}
	}
	s.st.Order = slices.Insert(s.st.Order, at, e.UUID)
	if at > len(list) {
		at = len(list)
	}
	s.upnext = slices.Insert(list, at, e)
}

// unplaced is an episode the user took out of the account's Up Next from Pocket: out of the Q2's at
// once.
func (s *syncer) unplaced(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Order = slices.DeleteFunc(s.st.Order, func(u string) bool { return u == uuid })
	s.upnext = slices.DeleteFunc(slices.Clone(s.upnext), func(e pocketcasts.Episode) bool { return e.UUID == uuid })
	delete(s.st.Seen, uuid)
}

// playingUUID is the episode the Q2 started last (nowPlaying), as Pocket Casts names it, or "".
func (s *syncer) playingUUID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Playing
}

package main

import (
	"testing"

	"q2-pocket/internal/pocketcasts"
)

// An account's episode in its show's list: by its id; else, published again under a new id, by its
// title within a month of its date, the nearest; never a same-titled one from another time.
func TestSameEpisode(t *testing.T) {
	list := []pocketcasts.CacheEpisode{
		{UUID: "new5", Title: "Chapter 5", URL: "https://x/new5.mp3", Published: "2026-03-26T02:05:00Z"},
		{UUID: "w2", Title: "Why Employee Wellness Programs Don’t Work", URL: "https://x/w2.mp3", Published: "2022-03-16T10:00:00Z"},
		{UUID: "old5", Title: "Chapter 5", URL: "https://x/2019-5.mp3", Published: "2019-01-01T00:00:00Z"},
		{UUID: "same", Title: "Anything", URL: "https://x/same.mp3", Published: "2020-01-01T00:00:00Z"},
		{UUID: "s2", Title: "A Life Without Problems with Ryan Holiday [S2 Ep.35]", URL: "https://x/s2.mp3", Published: "2021-10-29T10:00:00Z"},
		{UUID: "orig", Title: "238: What if you were tortured in an Iranian prison?", URL: "https://x/o.mp3", Published: "2022-06-07T10:00:00Z"},
	}
	for _, c := range []struct {
		e    pocketcasts.Episode
		want string
	}{
		{pocketcasts.Episode{UUID: "same", Title: "Renamed", Published: "2024-01-01T00:00:00Z"}, "same"},
		{pocketcasts.Episode{UUID: "9fef", Title: "Chapter 5", Published: "2026-03-26T02:05:00Z"}, "new5"},
		{pocketcasts.Episode{UUID: "w1", Title: "Why employee wellness programs don't work", Published: "2022-03-07T10:00:00Z"}, "w2"},
		{pocketcasts.Episode{UUID: "c9", Title: "Chapter 5", Published: "2023-06-01T00:00:00Z"}, ""},
		{pocketcasts.Episode{UUID: "r1", Title: "A Life Without Problems with Ryan Holiday", Published: "2021-10-30T10:00:00Z"}, "s2"},
		{pocketcasts.Episode{UUID: "r2", Title: "What if you were tortured in an Iranian prison? - [Rebroadcast - #238]", Published: "2022-12-27T10:00:00Z"}, "orig"},
		{pocketcasts.Episode{UUID: "r3", Title: "What if you were tortured in an Iranian prison?", Published: "2022-12-27T10:00:00Z"}, ""},
		{pocketcasts.Episode{UUID: "z", Title: "Not There", Published: "2026-03-26T02:05:00Z"}, ""},
	} {
		got := ""
		if m := sameEpisode(c.e, list); m != nil {
			got = m.UUID
		}
		if got != c.want {
			t.Errorf("%s %q: %q, want %q", c.e.UUID, c.e.Title, got, c.want)
		}
	}
}

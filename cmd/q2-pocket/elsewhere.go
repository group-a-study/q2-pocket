package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/library"
)

// An episode whose audio is gone at its address (the publisher took it down, or the link in front
// of it died) is often still somewhere else: published again in its own show (under a new title's
// season mark, or as the original of a rebroadcast), in another show's feed (a trailer for a new
// show runs in several), or on its host's own site, which keeps episodes its feed has dropped. The
// user's, 2026-11-16: of 16 episodes in Up Next gone at their addresses, these places had 7.

// Places looked in, in order: the show's own list on Pocket Casts; Apple's podcast directory, every
// podcast's episodes by title (its public Search API); and for a show on Libsyn, its Libsyn site,
// whose episode pages link their audio however old (<show>.libsyn.com/<title in dashes>). Podchaser
// had one more of the user's but turns away apps that say what they are, so it isn't asked.
var (
	appleSearch = "https://itunes.apple.com/search"
	libsynSite  = "https://%s.libsyn.com/%s" // the show's name on Libsyn, the episode's title in dashes
)

const (
	elsewhereMax = 3 << 20 // bytes of a page read
	lengthTol    = 0.05    // a copy's length may differ by this share from the episode's
)

// elsewhere is a live address for e's audio somewhere other than its own, "" for none.
func (a *app) elsewhere(ctx context.Context, e library.EpisodeInfo) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	key := coreKey(e.Title)
	if key == "" {
		return ""
	}
	type cand struct {
		u      string
		length float64 // seconds, 0 unknown
		near   float64 // how far its date is from the episode's, in days (lower first)
	}
	try := func(where string, cs []cand) string {
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].near < cs[j].near })
		for i, c := range cs {
			if i == 5 || ctx.Err() != nil {
				break
			}
			if c.u == "" || c.u == e.Feed.URL || !sameLength(float64(e.Duration), c.length) {
				continue
			}
			if a.answers(ctx, c.u) {
				logf("download %q: found in %s", e.Title, where)
				return c.u
			}
		}
		return ""
	}
	days := func(published string) float64 {
		t, err := time.Parse(time.RFC3339, published)
		if err != nil || e.Date.IsZero() {
			return 1e6
		}
		return math.Abs(t.Sub(e.Date).Hours() / 24)
	}
	// its own show, as Pocket Casts has it now
	if e.PC != "" {
		done := clean.Exclusive()
		sh, _, _, err := a.pc.Full(ctx, e.PC, "")
		done()
		if err == nil && sh != nil {
			var cs []cand
			for _, c := range sh.Episodes {
				if c.UUID != e.Feed.PC && coreKey(c.Title) == key {
					cs = append(cs, cand{c.URL, float64(c.Duration), days(c.Published)})
				}
			}
			if u := try("its show's list", cs); u != "" {
				return u
			}
		}
	}
	// Apple's directory: every podcast's episodes with its title
	q := url.Values{"term": {strings.TrimSpace(marks.ReplaceAllString(e.Title, " "))}, "media": {"podcast"},
		"entity": {"podcastEpisode"}, "limit": {"25"}}
	if b := a.webPage(ctx, appleSearch+"?"+q.Encode()); b != nil {
		var r struct {
			Results []struct {
				TrackName, EpisodeURL, ReleaseDate, CollectionName string
				TrackTimeMillis                                    float64
			} `json:"results"`
		}
		if json.Unmarshal(b, &r) == nil {
			var cs []cand
			for _, x := range r.Results {
				if coreKey(x.TrackName) == key {
					near := days(x.ReleaseDate)
					if titleKey(x.CollectionName) != titleKey(e.Show) {
						near += 1e5 // its own show's copy first
					}
					cs = append(cs, cand{x.EpisodeURL, x.TrackTimeMillis / 1000, near})
				}
			}
			if u := try("Apple's directory", cs); u != "" {
				return u
			}
		}
	}
	// the show's Libsyn site: its episode's page (an episode put up again gets "-0", "-1"), its audio
	if m := libsynFile.FindStringSubmatch(e.Feed.URL); m != nil {
		slug := strings.Join(strings.Fields(key), "-")
		for _, page := range []string{slug, slug + "-0", slug + "-1"} {
			b := a.webPage(ctx, fmt.Sprintf(libsynSite, m[1], page))
			var cs []cand
			for _, f := range libsynAudio.FindAllString(string(b), 20) {
				if !strings.Contains(f, "/"+m[1]+"/") {
					continue // another show's, in a sidebar
				}
				cs = append(cs, cand{f, 0, 0})
			}
			if u := try("its Libsyn site", cs); u != "" {
				return u
			}
		}
	}
	return ""
}

// libsynFile is an episode's audio on Libsyn, its show's name there; libsynAudio, such an address
// in a page.
var (
	libsynFile  = regexp.MustCompile(`^https?://(?:[^/]+/)*?traffic\.libsyn\.com/(?:secure/)?([a-z0-9_-]+)/`)
	libsynAudio = regexp.MustCompile(`https?://traffic\.libsyn\.com/[^"'\s<>]+\.(?:mp3|m4a)(?:\?[^"'\s<>]*)?`)
)

// webPage is a page's bytes (at most elsewhereMax), nil when it can't be had.
func (a *app) webPage(ctx context.Context, u string) []byte {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, elsewhereMax))
	return b
}

// answers is whether u gives audio now (its first byte asked for).
func (a *app) answers(ctx context.Context, u string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := a.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	typ := strings.ToLower(resp.Header.Get("Content-Type"))
	return (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) &&
		(strings.HasPrefix(typ, "audio/") || strings.Contains(typ, "octet-stream") || typ == "")
}

// sameLength is whether a copy of length seconds can be an episode of want seconds (either unknown:
// yes).
func sameLength(want, length float64) bool {
	return want <= 0 || length <= 0 || math.Abs(length-want) <= lengthTol*want
}

// marks are what a show adds to an episode's title when it publishes it again: a rebroadcast's or
// a season's mark, in brackets ("[Rebroadcast - #238]", "[S2 Ep.35]"); and a leading number
// ("238: ", "105 - ").
var (
	marks   = regexp.MustCompile(`(?i)\s*[-–|]?\s*[\[(](?:rebroadcast|re-?release|replay|encore|rerun|classic|best of|s\d+\s*ep|season\s*\d|ep(?:isode)?\.?\s*\d)[^\])]*[\])]`)
	leading = regexp.MustCompile(`^\s*#?\d+\s*[:.\-–]\s+`)
	rerun   = regexp.MustCompile(`(?i)[\[(](?:rebroadcast|re-?release|replay|encore|rerun|classic)`)
)

// coreKey is a title as compared across copies: its marks set aside, then as titleKey.
func coreKey(t string) string {
	return titleKey(leading.ReplaceAllString(marks.ReplaceAllString(t, " "), ""))
}

// rebroadcast is whether a title marks its episode as published before.
func rebroadcast(t string) bool { return rerun.MatchString(t) }

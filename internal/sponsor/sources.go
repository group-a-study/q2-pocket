package sponsor

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Addresses; tests point them elsewhere.
var (
	SponsorBlockBase = "https://sponsor.ajay.app"
	YouTubeBase      = "https://www.googleapis.com/youtube/v3"
	SpotifyAccounts  = "https://accounts.spotify.com/api/token"
	SpotifyBase      = "https://api.spotify.com/v1"
)

// Categories SponsorBlock may be asked for: paid promotion always, unpaid self-promotion when the
// settings say.
const (
	CatSponsor   = "sponsor"
	CatSelfPromo = "selfpromo"
)

func getJSON(ctx context.Context, c *http.Client, u, agent string, hdr map[string]string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", agent)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, json.Unmarshal(b, out)
}

// SponsorBlock asks SponsorBlock's database (sponsor.ajay.app) for a video's or Spotify episode's
// segments, by the first characters of its id's SHA-256 so the id itself isn't sent.
type SponsorBlock struct {
	HTTP  *http.Client
	Agent string
}

// Segments is what SponsorBlock has for a video (service "YouTube") or a Spotify episode ("Spotify")
// in cats, skippable ones only, and the length the video had when they were submitted (0 none).
func (sb *SponsorBlock) Segments(ctx context.Context, service, id string, cats []string) ([]Segment, float64, error) {
	sum := sha256.Sum256([]byte(id))
	cj, _ := json.Marshal(cats)
	q := url.Values{"categories": {string(cj)}, "actionTypes": {`["skip"]`}, "service": {service}}
	u := SponsorBlockBase + "/api/skipSegments/" + hex.EncodeToString(sum[:])[:4] + "?" + q.Encode()
	var r []struct {
		VideoID  string `json:"videoID"`
		Segments []struct {
			Segment  [2]float64 `json:"segment"`
			Category string     `json:"category"`
			Action   string     `json:"actionType"`
			Duration float64    `json:"videoDuration"`
			Votes    int        `json:"votes"`
			Locked   int        `json:"locked"`
		} `json:"segments"`
	}
	code, err := getJSON(ctx, sb.HTTP, u, sb.Agent, nil, &r)
	if code == http.StatusNotFound { // none for that prefix
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var out []Segment
	length := 0.0
	for _, v := range r {
		if v.VideoID != id {
			continue
		}
		for _, s := range v.Segments {
			if s.Action != "" && s.Action != "skip" || s.Votes < 0 && s.Locked == 0 || s.Segment[1] <= s.Segment[0] {
				continue
			}
			why := "a sponsor segment (SponsorBlock)"
			if s.Category == CatSelfPromo {
				why = "self-promotion (SponsorBlock)"
			}
			if service == "Spotify" {
				why = strings.Replace(why, "SponsorBlock", "Spot SponsorBlock", 1)
			}
			out = append(out, Segment{s.Segment[0], s.Segment[1], why})
			if s.Duration > 0 {
				length = s.Duration
			}
		}
	}
	return merge(out), length, nil
}

// Match is a video or a Spotify episode found for an episode, and its length.
type Match struct {
	ID     string
	Length float64
}

// YouTube finds an episode's video with the YouTube Data API (a key of the user's own; a search
// spends 100 of its 10,000 units a day).
type YouTube struct {
	Key   string
	HTTP  *http.Client
	Agent string
}

// Find is the video of a show's episode: titled alike, published within a week of it, as long
// within a tenth (length: the episode's, 0 unknown).
func (y *YouTube) Find(ctx context.Context, show, title string, date time.Time, length float64) (Match, error) {
	q := url.Values{"part": {"snippet"}, "type": {"video"}, "maxResults": {"8"}, "q": {show + " " + title}, "key": {y.Key}}
	var s struct {
		Items []struct {
			ID      struct{ VideoID string } `json:"id"`
			Snippet struct {
				Title     string `json:"title"`
				Channel   string `json:"channelTitle"`
				Published string `json:"publishedAt"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if _, err := getJSON(ctx, y.HTTP, YouTubeBase+"/search?"+q.Encode(), y.Agent, nil, &s); err != nil {
		return Match{}, err
	}
	var ids []string
	for _, it := range s.Items {
		t, _ := time.Parse(time.RFC3339, it.Snippet.Published)
		if !date.IsZero() && !t.IsZero() && math.Abs(t.Sub(date).Hours()) > 7*24 {
			continue
		}
		if Alike(title, it.Snippet.Title) >= 0.6 {
			ids = append(ids, it.ID.VideoID)
		}
	}
	if len(ids) == 0 {
		return Match{}, nil
	}
	var v struct {
		Items []struct {
			ID      string `json:"id"`
			Details struct {
				Duration string `json:"duration"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	q = url.Values{"part": {"contentDetails"}, "id": {strings.Join(ids, ",")}, "key": {y.Key}}
	if _, err := getJSON(ctx, y.HTTP, YouTubeBase+"/videos?"+q.Encode(), y.Agent, nil, &v); err != nil {
		return Match{}, err
	}
	for _, id := range ids { // in the search's order
		for _, it := range v.Items {
			d := isoDuration(it.Details.Duration)
			if it.ID == id && (length == 0 || math.Abs(d-length) <= 0.1*length) {
				return Match{id, d}, nil
			}
		}
	}
	return Match{}, nil
}

var isoRE = regexp.MustCompile(`^P(?:(\d+)D)?T?(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?$`)

func isoDuration(s string) float64 {
	m := isoRE.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n := func(x string) float64 { v, _ := strconv.Atoi(x); return float64(v) }
	return n(m[1])*86400 + n(m[2])*3600 + n(m[3])*60 + n(m[4])
}

// Spotify finds an episode on Spotify with the Web API (an app of the user's own, its client id and
// secret: developer.spotify.com).
type Spotify struct {
	ID, Secret string
	HTTP       *http.Client
	Agent      string

	mu    sync.Mutex
	token string
	until time.Time
	shows map[string]string // a show's title: its Spotify id
}

func (s *Spotify) auth(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.until) {
		return s.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SpotifyAccounts, strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(s.ID+":"+s.Secret)))
	req.Header.Set("User-Agent", s.Agent)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&r) != nil || r.Token == "" {
		return "", errors.New("Spotify refused the client id and secret")
	}
	s.token, s.until = r.Token, time.Now().Add(time.Duration(max(r.Expires-60, 60))*time.Second)
	return s.token, nil
}

// Find is a show's episode on Spotify: the show found by its name, then its episode titled alike and
// published within two days.
func (s *Spotify) Find(ctx context.Context, show, title string, date time.Time, length float64) (Match, error) {
	tok, err := s.auth(ctx)
	if err != nil {
		return Match{}, err
	}
	hdr := map[string]string{"Authorization": "Bearer " + tok}
	s.mu.Lock()
	sid, known := s.shows[show]
	s.mu.Unlock()
	if !known {
		var r struct {
			Shows struct {
				Items []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"items"`
			} `json:"shows"`
		}
		q := url.Values{"q": {show}, "type": {"show"}, "limit": {"5"}, "market": {"US"}}
		if _, err := getJSON(ctx, s.HTTP, SpotifyBase+"/search?"+q.Encode(), s.Agent, hdr, &r); err != nil {
			return Match{}, err
		}
		for _, it := range r.Shows.Items {
			if Alike(show, it.Name) >= 0.8 {
				sid = it.ID
				break
			}
		}
		s.mu.Lock()
		if s.shows == nil {
			s.shows = map[string]string{}
		}
		s.shows[show] = sid
		s.mu.Unlock()
	}
	if sid == "" {
		return Match{}, nil
	}
	var r struct {
		Items []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Duration int    `json:"duration_ms"`
			Release  string `json:"release_date"`
		} `json:"items"`
	}
	q := url.Values{"limit": {"50"}, "market": {"US"}}
	if _, err := getJSON(ctx, s.HTTP, SpotifyBase+"/shows/"+url.PathEscape(sid)+"/episodes?"+q.Encode(), s.Agent, hdr, &r); err != nil {
		return Match{}, err
	}
	for _, it := range r.Items {
		t, _ := time.Parse("2006-01-02", it.Release)
		if !date.IsZero() && !t.IsZero() && math.Abs(t.Sub(date.UTC().Truncate(24*time.Hour)).Hours()) > 48 {
			continue
		}
		if Alike(title, it.Name) >= 0.7 {
			return Match{it.ID, float64(it.Duration) / 1000}, nil
		}
	}
	return Match{}, nil
}

var (
	ytLink = regexp.MustCompile(`(?:youtube\.com/(?:watch\?(?:[^"'\s<>]*&(?:amp;)?)?v=|live/|embed/)|youtu\.be/)([A-Za-z0-9_-]{11})`)
	spLink = regexp.MustCompile(`open\.spotify\.com/(?:[a-z-]+/)?episode/([A-Za-z0-9]{22})`)
)

// Links is the YouTube videos and Spotify episodes show notes link to, in order, each once.
func Links(notes string) (youtube, spotify []string) {
	seen := map[string]bool{}
	for _, m := range ytLink.FindAllStringSubmatch(notes, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			youtube = append(youtube, m[1])
		}
	}
	for _, m := range spLink.FindAllStringSubmatch(notes, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			spotify = append(spotify, m[1])
		}
	}
	return youtube, spotify
}

// Alike is how alike two titles are, 0 to 1: their words in common over the shorter's words
// (case, accents of punctuation and "Ep. 12"-style numbering aside, but a number that differs
// counts against).
func Alike(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	if len(wa) > len(wb) {
		wa, wb = wb, wa
	}
	in := map[string]bool{}
	for _, w := range wb {
		in[w] = true
	}
	n := 0
	for _, w := range wa {
		if in[w] {
			n++
		} else if w[0] >= '0' && w[0] <= '9' {
			return 0 // another number: another episode
		}
	}
	return float64(n) / float64(len(wa))
}

var filler = map[string]bool{"the": true, "a": true, "an": true, "and": true, "of": true, "ep": true, "episode": true,
	"with": true, "podcast": true, "feat": true, "ft": true, "to": true, "in": true, "on": true}

func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !filler[w] {
			out = append(out, w)
		}
	}
	return out
}

// Chapter is a chapter's start (and end, 0 when the next chapter's start is it) and title.
type Chapter struct {
	From, To float64
	Title    string
}

var adChapter = regexp.MustCompile(`(?i)^\W*(ads?|adverts?|advertisements?|advertising|commercials?|ad break|paid promotion|promo(tion)?s?|sponsors?|sponsored( by)?|sponsor (break|message|read|segment|spot)s?|a word from our sponsors?|message from our sponsors?)\b`)

// AdChapters is the chapters titled as adverts or sponsors, as segments; length ends the last.
func AdChapters(ch []Chapter, length float64) []Segment {
	sort.Slice(ch, func(i, j int) bool { return ch[i].From < ch[j].From })
	var out []Segment
	for i, c := range ch {
		if !adChapter.MatchString(strings.TrimSpace(c.Title)) {
			continue
		}
		to := c.To
		if to <= c.From {
			to = length
			if i+1 < len(ch) {
				to = ch[i+1].From
			}
		}
		if to > c.From && to-c.From <= readMax*2 {
			out = append(out, Segment{c.From, to, "a chapter named " + strconvQuote(c.Title)})
		}
	}
	return merge(out)
}

func strconvQuote(s string) string { return "“" + strings.TrimSpace(s) + "”" }

// JSONChapters reads Podcasting 2.0 chapters.
func JSONChapters(b []byte) []Chapter {
	var r struct {
		Chapters []struct {
			Start float64 `json:"startTime"`
			End   float64 `json:"endTime"`
			Title string  `json:"title"`
			TOC   *bool   `json:"toc"`
		} `json:"chapters"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	var out []Chapter
	for _, c := range r.Chapters {
		out = append(out, Chapter{c.Start, c.End, c.Title})
	}
	return out
}

// Timelines a source's segments may be on.
const (
	Unplaced = iota // neither: its segments can't be used
	Clean           // the episode as cut of its adverts
	Original        // the episode as downloaded
)

// Fit is which of the episode's timelines a source of length ref (seconds) is on: the one it is
// within tol of (the cut episode's first). transcript: ref is where its last words end, which may
// come up to tail seconds before the audio does.
func Fit(ref, clean, original float64, transcript bool) int {
	tol := max(3, 0.003*clean)
	fits := func(d float64) bool {
		if transcript {
			return ref <= d+tol && ref >= d-tail
		}
		return math.Abs(ref-d) <= tol
	}
	switch {
	case ref <= 0:
		return Unplaced
	case fits(clean):
		return Clean
	case fits(original):
		return Original
	}
	return Unplaced
}

// tail is how long an episode may play on after its transcript's last words: music, credits.
const tail = 45.0

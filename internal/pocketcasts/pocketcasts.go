// Package pocketcasts talks to Pocket Casts as its web player does, the API pocketcli uses: sign in,
// the podcasts subscribed, In Progress, New Releases, Starred, Up Next, the listening history, where
// each episode was left and whether it was played, uploaded files, and the public podcast cache,
// show notes and Discover lists.
package pocketcasts

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Addresses; tests point them elsewhere.
var (
	Base  = "https://api.pocketcasts.com"
	Cache = "https://podcast-api.pocketcasts.com"
	Lists = "https://lists.pocketcasts.com"
	Art   = "https://static.pocketcasts.com/discover/images/400/"
)

// FilesPodcast is the podcast id Pocket Casts files an uploaded file under in Up Next.
const FilesPodcast = "da7aba5e-f11e-f11e-f11e-da7aba5ef11e"

// Playing statuses.
const (
	Unplayed   = 1
	InProgress = 2
	Played     = 3
)

var (
	ErrSignIn = errors.New("Pocket Casts wants you to sign in again")
	ErrWrong  = errors.New("that email and password aren't right")
)

// Token is a signed-in session, kept on the card.
type Token struct {
	Access  string    `json:"access"`
	Refresh string    `json:"refresh,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
	Email   string    `json:"email,omitempty"`
	User    string    `json:"uuid,omitempty"`
}

// Client is one account's.
type Client struct {
	HTTP  *http.Client
	Agent string
	Saved func(Token) // a refreshed token, to keep

	mu  sync.Mutex
	tok Token
}

// New is a client for a session (Token{} when signed out).
func New(c *http.Client, agent string, t Token) *Client {
	return &Client{HTTP: c, Agent: agent, tok: t}
}

// Token is the session as it stands.
func (c *Client) Token() Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tok
}

// SignedIn is whether there is a session.
func (c *Client) SignedIn() bool { return c.Token().Access != "" }

// SignOut forgets the session.
func (c *Client) SignOut() {
	c.mu.Lock()
	c.tok = Token{}
	c.mu.Unlock()
}

// StatusError is a reply that wasn't a success.
type StatusError struct{ Code int }

func (e StatusError) Error() string { return fmt.Sprintf("Pocket Casts answered HTTP %d", e.Code) }

func (c *Client) do(ctx context.Context, method, u string, body any, auth bool) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.Agent)
	req.Header.Set("Accept-Encoding", "gzip")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.Token().Access)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	r := io.Reader(resp.Body)
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		r = gz
	}
	b, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, err
	}
	b = gunzip(b)
	if resp.StatusCode/100 != 2 {
		return b, StatusError{resp.StatusCode}
	}
	return b, nil
}

// gunzip is b unzipped when it is gzipped without saying so (the cache's files are).
func gunzip(b []byte) []byte {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return b
	}
	if u, err := io.ReadAll(io.LimitReader(gz, 64<<20)); err == nil {
		return u
	}
	return b
}

// call is an API call as the signed-in user: a refused session is refreshed once, if it can be.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	if !c.SignedIn() {
		return ErrSignIn
	}
	b, err := c.do(ctx, method, Base+path, body, true)
	var se StatusError
	if errors.As(err, &se) && se.Code == http.StatusUnauthorized {
		if c.refresh(ctx) != nil {
			return ErrSignIn
		}
		b, err = c.do(ctx, method, Base+path, body, true)
		if errors.As(err, &se) && se.Code == http.StatusUnauthorized {
			return ErrSignIn
		}
	}
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(b)) > 0 { // saving a file's place answers with nothing
		return json.Unmarshal(b, out)
	}
	return nil
}

type loginReply struct {
	Token        string `json:"token"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	UUID         string `json:"uuid"`
	Email        string `json:"email"`
}

func (r loginReply) token(email string) Token {
	t := Token{Access: r.AccessToken, Refresh: r.RefreshToken, Email: email, User: r.UUID}
	if t.Access == "" {
		t.Access = r.Token
	}
	if r.ExpiresIn > 0 {
		t.Expires = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return t
}

// Login signs in with an email and password, as the web player does; the password isn't kept.
func (c *Client) Login(ctx context.Context, email, password string) (Token, error) {
	body := map[string]string{"email": strings.TrimSpace(email), "password": password, "scope": "webplayer"}
	var r loginReply
	b, err := c.do(ctx, http.MethodPost, Base+"/user/login_pocket_casts", body, false)
	var se StatusError
	if errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusMethodNotAllowed) {
		b, err = c.do(ctx, http.MethodPost, Base+"/user/login", body, false) // the older sign-in
	}
	if errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusBadRequest || se.Code == http.StatusForbidden) {
		return Token{}, ErrWrong
	}
	if err != nil {
		return Token{}, err
	}
	if json.Unmarshal(b, &r) != nil {
		return Token{}, errors.New("Pocket Casts sent something unexpected")
	}
	t := r.token(body["email"])
	if t.Access == "" {
		return Token{}, ErrWrong
	}
	c.mu.Lock()
	c.tok = t
	c.mu.Unlock()
	return t, nil
}

// refresh trades the refresh token for a new session.
func (c *Client) refresh(ctx context.Context) error {
	t := c.Token()
	if t.Refresh == "" {
		return ErrSignIn
	}
	b, err := c.do(ctx, http.MethodPost, Base+"/user/token", map[string]string{"grant_type": "refresh_token", "refresh_token": t.Refresh}, false)
	if err != nil {
		return err
	}
	var r loginReply
	if json.Unmarshal(b, &r) != nil {
		return ErrSignIn
	}
	nt := r.token(t.Email)
	if nt.Access == "" {
		return ErrSignIn
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	if nt.User == "" {
		nt.User = t.User
	}
	c.mu.Lock()
	c.tok = nt
	c.mu.Unlock()
	if c.Saved != nil {
		c.Saved(nt)
	}
	return nil
}

// Podcast is a show as Pocket Casts lists it.
type Podcast struct {
	UUID        string `json:"uuid"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`  // often the show's site, not its feed
	Feed        string `json:"feed,omitempty"` // the Discover lists' and search's feed address
}

// ArtURL is a podcast's cover, a JPEG.
func ArtURL(uuid string) string { return Art + uuid + ".jpg" }

// Podcasts is the podcasts subscribed.
func (c *Client) Podcasts(ctx context.Context) ([]Podcast, error) {
	var r struct {
		Podcasts []Podcast `json:"podcasts"`
	}
	err := c.call(ctx, http.MethodPost, "/user/podcast/list", map[string]int{"v": 1}, &r)
	return r.Podcasts, err
}

func (c *Client) Subscribe(ctx context.Context, uuid string) error {
	return c.call(ctx, http.MethodPost, "/user/podcast/subscribe", map[string]string{"uuid": uuid}, nil)
}

func (c *Client) Unsubscribe(ctx context.Context, uuid string) error {
	return c.call(ctx, http.MethodPost, "/user/podcast/unsubscribe", map[string]string{"uuid": uuid}, nil)
}

// Resolve is Pocket Casts' id for the podcast at a feed address, "" when it doesn't know it.
func (c *Client) Resolve(ctx context.Context, feed string) (string, error) {
	var r struct {
		Podcasts []Podcast `json:"podcasts"`
	}
	if err := c.call(ctx, http.MethodPost, "/discover/search", map[string]string{"term": feed}, &r); err != nil {
		return "", err
	}
	if len(r.Podcasts) > 0 {
		return r.Podcasts[0].UUID, nil
	}
	return "", nil
}

// Search is Pocket Casts' own podcast search.
func (c *Client) Search(ctx context.Context, term string) ([]Podcast, error) {
	var r struct {
		Podcasts []Podcast `json:"podcasts"`
	}
	err := c.call(ctx, http.MethodPost, "/discover/search", map[string]string{"term": term}, &r)
	return r.Podcasts, err
}

// Episode is an episode as the lists name it, with where the user is in it.
type Episode struct {
	UUID         string `json:"uuid"`
	Podcast      string `json:"podcast"`
	PodcastUUID  string `json:"podcastUuid"`
	PodcastTitle string `json:"podcastTitle"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	Published    string `json:"published"`
	Duration     flex   `json:"duration"`
	Size         flex   `json:"size"`
	FileType     string `json:"fileType"`
	PlayedUpTo   flex   `json:"playedUpTo"`
	Status       flex   `json:"playingStatus"`
	Starred      bool   `json:"starred"`
	Deleted      bool   `json:"isDeleted"`
}

// Show is the podcast an episode is of.
func (e Episode) Show() string {
	if e.PodcastUUID != "" {
		return e.PodcastUUID
	}
	return e.Podcast
}

// Date is its publication time.
func (e Episode) Date() time.Time { return parseTime(e.Published) }

// flex is a number Pocket Casts sends as a number or a string.
type flex int64

func (f *flex) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flex(v)
	return nil
}

func parseTime(s string) time.Time {
	for _, l := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (c *Client) episodes(ctx context.Context, path string, body any) ([]Episode, error) {
	var r struct {
		Episodes []Episode `json:"episodes"`
	}
	err := c.call(ctx, http.MethodPost, path, body, &r)
	out := r.Episodes[:0]
	for _, e := range r.Episodes {
		if e.UUID != "" {
			out = append(out, e)
		}
	}
	return out, err
}

func (c *Client) InProgress(ctx context.Context) ([]Episode, error) {
	return c.episodes(ctx, "/user/in_progress", map[string]any{})
}

func (c *Client) NewReleases(ctx context.Context) ([]Episode, error) {
	return c.episodes(ctx, "/user/new_releases", map[string]any{})
}

func (c *Client) Starred(ctx context.Context) ([]Episode, error) {
	return c.episodes(ctx, "/user/starred", map[string]any{})
}

// History is the episodes listened to lately, most recent first.
func (c *Client) History(ctx context.Context) ([]Episode, error) {
	return c.episodes(ctx, "/user/history", map[string]any{})
}

// State is where the user is in an episode.
type State struct {
	PlayedUpTo int
	Status     int
	Starred    bool
	Duration   int
}

// States is the user's place in each episode of a podcast they have one in, by episode id.
func (c *Client) States(ctx context.Context, podcast string) (map[string]State, error) {
	eps, err := c.episodes(ctx, "/user/podcast/episodes", map[string]string{"uuid": podcast})
	out := make(map[string]State, len(eps))
	for _, e := range eps {
		out[e.UUID] = State{int(e.PlayedUpTo), int(e.Status), e.Starred, int(e.Duration)}
	}
	return out, err
}

// UpNext is the Up Next queue in order, the first what's playing.
func (c *Client) UpNext(ctx context.Context) ([]Episode, error) {
	eps, err := c.episodes(ctx, "/up_next/list", map[string]any{"version": 2, "model": "webplayer", "showPlayStatus": true})
	var se StatusError
	if errors.As(err, &se) && se.Code != http.StatusUnauthorized { // a broken entry fails the play-status form
		eps, err = c.episodes(ctx, "/up_next/list", map[string]any{"version": 2})
	}
	return eps, err
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID is whether s looks like a Pocket Casts id: Up Next keeps whatever it is sent, and an entry
// without one breaks reading it back.
func IsUUID(s string) bool { return uuidRE.MatchString(s) }

// upNextEpisode is the episode as the Up Next calls take it, every field set.
func upNextEpisode(e Episode) (map[string]string, error) {
	p := e.Published
	if len(p) == 10 {
		p += "T00:00:00Z"
	}
	m := map[string]string{"uuid": e.UUID, "title": e.Title, "url": e.URL, "podcast": e.Show(), "published": p}
	for _, v := range m {
		if strings.TrimSpace(v) == "" {
			return nil, errors.New("that episode is missing something Up Next needs")
		}
	}
	if !IsUUID(e.UUID) || !IsUUID(e.Show()) {
		return nil, errors.New("Pocket Casts doesn't know that episode")
	}
	return m, nil
}

// UpNextAdd puts an episode in Up Next: where is "now" (what's playing), "next" or "last".
func (c *Client) UpNextAdd(ctx context.Context, where string, e Episode) error {
	if where != "now" && where != "next" && where != "last" {
		return errors.New("pocketcasts: where?")
	}
	ep, err := upNextEpisode(e)
	if err != nil {
		return err
	}
	return c.call(ctx, http.MethodPost, "/up_next/play_"+where, map[string]any{"version": 2, "episode": ep}, nil)
}

// UpNextRemove takes episodes out of Up Next.
func (c *Client) UpNextRemove(ctx context.Context, uuids ...string) error {
	var ok []string
	for _, u := range uuids {
		if IsUUID(u) {
			ok = append(ok, u)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	return c.call(ctx, http.MethodPost, "/up_next/remove", map[string]any{"version": 2, "uuids": ok}, nil)
}

// Listened records a play in the listening history, as the apps do when an episode starts.
func (c *Client) Listened(ctx context.Context, e Episode) error {
	ep, err := upNextEpisode(e)
	if err != nil {
		return err
	}
	return c.call(ctx, http.MethodPost, "/history/do", map[string]any{"action": 1, "podcast": ep["podcast"],
		"episode": ep["uuid"], "title": ep["title"], "url": ep["url"], "published": ep["published"]}, nil)
}

// SetPlace saves where the user is in an episode, in seconds.
func (c *Client) SetPlace(ctx context.Context, podcast, episode string, sec int) error {
	return c.call(ctx, http.MethodPost, "/sync/update_episode", map[string]any{"uuid": episode, "podcast": podcast,
		"position": max(sec, 0), "status": InProgress}, nil)
}

// SetStatus marks an episode played or unplayed.
func (c *Client) SetStatus(ctx context.Context, podcast, episode string, status int) error {
	body := map[string]any{"uuid": episode, "podcast": podcast, "status": status}
	if status == Unplayed {
		body["position"] = 0
	}
	return c.call(ctx, http.MethodPost, "/sync/update_episode", body, nil)
}

// Star stars an episode, or takes its star away.
func (c *Client) Star(ctx context.Context, podcast, episode string, on bool) error {
	return c.call(ctx, http.MethodPost, "/sync/update_episode_star", map[string]any{"uuid": episode, "podcast": podcast, "star": on}, nil)
}

// File is a file the user uploaded to Pocket Casts.
type File struct {
	UUID        string `json:"uuid"`
	Title       string `json:"title"`
	Duration    flex   `json:"duration"`
	PlayedUpTo  flex   `json:"playedUpTo"`
	Status      flex   `json:"playingStatus"`
	Size        flex   `json:"size"`
	ContentType string `json:"contentType"`
	Modified    string `json:"modifiedAt"`
	Published   string `json:"published"`
}

func (c *Client) Files(ctx context.Context) ([]File, error) {
	var r struct {
		Files []File `json:"files"`
	}
	err := c.call(ctx, http.MethodGet, "/files?include_bookmarks=true", nil, &r)
	return r.Files, err
}

// FileURL is where a file downloads from (a signed address, good for a while).
func (c *Client) FileURL(ctx context.Context, uuid string) (string, error) {
	var r struct {
		URL string `json:"url"`
	}
	err := c.call(ctx, http.MethodGet, "/files/play/"+url.PathEscape(uuid), nil, &r)
	if err == nil && r.URL == "" {
		err = errors.New("Pocket Casts has no address for that file")
	}
	return r.URL, err
}

// SetFilePlace saves where the user is in a file, and whether it is played.
func (c *Client) SetFilePlace(ctx context.Context, uuid string, sec, status int) error {
	return c.call(ctx, http.MethodPost, "/files", map[string]any{"files": []map[string]any{{"uuid": uuid,
		"playedUpTo": max(sec, 0), "playingStatus": status}}}, nil)
}

// DeleteFile deletes an uploaded file from Pocket Casts.
func (c *Client) DeleteFile(ctx context.Context, uuid string) error {
	return c.call(ctx, http.MethodDelete, "/files/"+url.PathEscape(uuid), nil, nil)
}

// CacheEpisode is an episode as the public cache has it.
type CacheEpisode struct {
	UUID      string `json:"uuid"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	FileType  string `json:"file_type"`
	FileSize  flex   `json:"file_size"`
	Duration  flex   `json:"duration"`
	Published string `json:"published"`
	Type      string `json:"type"`
	Season    flex   `json:"season"`
	Number    flex   `json:"number"`
}

// Show is a podcast as the public cache has it.
type Show struct {
	UUID        string         `json:"uuid"`
	Title       string         `json:"title"`
	Author      string         `json:"author"`
	Description string         `json:"description"`
	URL         string         `json:"url"`
	Episodes    []CacheEpisode `json:"episodes"`
}

// Full is a podcast and all its episodes, from the public cache (no sign-in). etag: the last
// fetch's, to ask only for a change; unchanged true when there is none.
func (c *Client) Full(ctx context.Context, uuid, etag string) (show *Show, tag string, unchanged bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Cache+"/podcast/full/"+url.PathEscape(uuid), nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("User-Agent", c.Agent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	// the cache answers with the current version's address; that address is the version's tag
	tag = resp.Request.URL.String()
	if et := resp.Header.Get("ETag"); et != "" {
		tag = et
	}
	if resp.StatusCode == http.StatusNotModified || etag != "" && tag == etag {
		return nil, tag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, StatusError{resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, "", false, err
	}
	b = gunzip(b)
	var out struct {
		Podcast Show `json:"podcast"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, "", false, errors.New("Pocket Casts' copy of the podcast is broken")
	}
	return &out.Podcast, tag, false, nil
}

// Notes is an episode's show notes as the public cache has them: the transcripts and chapters
// its publisher gives, and Pocket Casts' own transcript.
type Notes struct {
	UUID        string       `json:"uuid"`
	Text        string       `json:"show_notes"`
	Image       string       `json:"image"`
	Link        string       `json:"link"`
	Chapters    any          `json:"chapters"`
	ChaptersURL string       `json:"chapters_url"`
	Transcripts []Transcript `json:"transcripts"`
	Generated   []Transcript `json:"pocket_casts_transcripts"`
}

// Transcript is a transcript's address and kind (text/vtt, application/x-subrip, application/json).
type Transcript struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

// ShowNotes is a podcast's episodes' show notes, by episode id.
func (c *Client) ShowNotes(ctx context.Context, podcast string) (map[string]Notes, error) {
	b, err := c.do(ctx, http.MethodGet, Cache+"/mobile/show_notes/full/"+url.PathEscape(podcast), nil, false)
	if err != nil {
		return nil, err
	}
	var r struct {
		Podcast struct {
			Episodes []Notes `json:"episodes"`
		} `json:"podcast"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	out := make(map[string]Notes, len(r.Podcast.Episodes))
	for _, n := range r.Podcast.Episodes {
		out[n.UUID] = n
	}
	return out, nil
}

// List is one of Discover's lists: "trending", "popular" or "featured".
func (c *Client) List(ctx context.Context, name string) ([]Podcast, error) {
	b, err := c.do(ctx, http.MethodGet, Lists+"/"+url.PathEscape(name)+".json", nil, false)
	if err != nil {
		return nil, err
	}
	var r struct {
		Podcasts []Podcast `json:"podcasts"`
	}
	return r.Podcasts, json.Unmarshal(b, &r)
}

// Get fetches an address (a transcript, chapters) with the client's agent, at most max bytes.
func (c *Client) Get(ctx context.Context, u string, max int64) ([]byte, error) {
	b, err := c.do(ctx, http.MethodGet, u, nil, false)
	if int64(len(b)) > max {
		b = b[:max]
	}
	return b, err
}

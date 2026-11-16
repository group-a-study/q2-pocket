// Package youtube fetches a YouTube video's details, captions and audio the way yt-dlp does when it
// has no JavaScript runtime (yt-dlp's _DEFAULT_JSLESS_CLIENTS): the video's watch page gives a
// visitor id, then YouTube's player API, asked as its visionOS client, gives the video's formats at
// plain addresses (no signature to work out, no proof-of-origin token). The audio comes in pieces
// of chunkSize, as yt-dlp's http_chunk_size has it.
package youtube

import (
	"bytes"
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

// Base is YouTube's address; tests point it elsewhere.
var Base = "https://www.youtube.com"

// The visionOS client, as yt-dlp's INNERTUBE_CLIENTS["visionos"] has it (REQUIRE_JS_PLAYER false,
// no PO token policy).
const (
	clientName    = "VISIONOS"
	clientNumber  = "101"
	clientVersion = "1.02"
	clientAgent   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_7_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15"
)

// ChunkSize is the bytes of audio asked for at a time (yt-dlp's CHUNK_SIZE for YouTube).
const ChunkSize = 10 << 20

// visitorFor is how long a visitor id is used before a watch page is read for a new one.
const visitorFor = 6 * time.Hour

// Client asks YouTube for videos. Agent is the app's own, sent for the watch page; the player API
// and the audio go as the visionOS client.
type Client struct {
	HTTP  *http.Client
	Agent string

	mu      sync.Mutex
	visitor string
	at      time.Time
	refused time.Time // YouTube's last refusal: none asked for until refuseFor has passed
}

// refuseFor is how long YouTube is let be after it refuses a video (a check that the asker is no
// bot, say), so each download doesn't ask again.
const refuseFor = 30 * time.Minute

// ErrRefused is YouTube refusing the video to this client: a check that the asker is no bot, a
// video for signed-in viewers only, one made for children.
var ErrRefused = errors.New("YouTube refused it")

// Video is what the player API says of a video.
type Video struct {
	ID, Title, Channel, Description string
	Length                          float64  // seconds
	Audio                           []Format // AAC in MP4 only, the best first
	Captions                        []Track
}

// Format is one of a video's audio streams.
type Format struct {
	Itag       int
	URL        string
	Size       int64
	Codec      string // "mp4a.40.2" (AAC-LC), "mp4a.40.5" (HE-AAC)
	SampleRate int
	Bitrate    int
}

// Track is a video's captions in one language: Auto when YouTube's speech recognition made them.
type Track struct {
	Lang, Name string
	Auto       bool
	URL        string
}

var visitorRE = regexp.MustCompile(`"VISITOR_DATA":"([^"]+)"`)

// visitorID is a visitor id from a watch page (only its start is read: the id is in its first
// part), kept a while.
func (c *Client) visitorID(ctx context.Context, id string) (string, error) {
	c.mu.Lock()
	if c.visitor != "" && time.Since(c.at) < visitorFor {
		v := c.visitor
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()
	u := Base + "/watch?" + url.Values{"v": {id}, "bpctr": {"9999999999"}, "has_verified": {"1"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.Agent)
	req.Header.Set("Accept-Language", "en")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", errors.New("couldn't reach YouTube")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("YouTube answered HTTP %d", resp.StatusCode)
	}
	// read in pieces, each searched with the last's tail, until the id turns up
	var buf []byte
	piece := make([]byte, 32<<10)
	for read := 0; read < 1<<20; {
		n, err := resp.Body.Read(piece)
		read += n
		buf = append(buf, piece[:n]...)
		if m := visitorRE.FindSubmatch(buf); m != nil {
			v, _ := url.QueryUnescape(string(m[1]))
			c.mu.Lock()
			c.visitor, c.at = v, time.Now()
			c.mu.Unlock()
			return v, nil
		}
		if len(buf) > 4<<10 {
			buf = append(buf[:0], buf[len(buf)-4<<10:]...)
		}
		if err != nil {
			break
		}
	}
	return "", errors.New("YouTube's page had no visitor id")
}

// forget drops the visitor id, so the next video reads a page for a new one.
func (c *Client) forget() {
	c.mu.Lock()
	c.visitor = ""
	c.mu.Unlock()
}

// Video is a video's details, audio formats and captions: ErrRefused when YouTube won't give
// them to this client.
func (c *Client) Video(ctx context.Context, id string) (*Video, error) {
	c.mu.Lock()
	wait := !c.refused.IsZero() && time.Since(c.refused) < refuseFor
	c.mu.Unlock()
	if wait {
		return nil, fmt.Errorf("%w lately: asked again later", ErrRefused)
	}
	v, err := c.player(ctx, id)
	if errors.Is(err, ErrRefused) { // perhaps the visitor id: once more with a new one
		c.forget()
		if v, err = c.player(ctx, id); errors.Is(err, ErrRefused) {
			c.mu.Lock()
			c.refused = time.Now()
			c.mu.Unlock()
		}
	}
	return v, err
}

func (c *Client) player(ctx context.Context, id string) (*Video, error) {
	visitor, err := c.visitorID(ctx, id)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName": clientName, "clientVersion": clientVersion, "deviceMake": "Apple",
			"deviceModel": "RealityDevice17,1", "userAgent": clientAgent, "osName": "visionOS",
			"osVersion": "26.5.23O471", "hl": "en", "visitorData": visitor}},
		"videoId":         id,
		"playbackContext": map[string]any{"contentPlaybackContext": map[string]any{"html5Preference": "HTML5_PREF_WANTS"}},
		"contentCheckOk":  true,
		"racyCheckOk":     true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Base+"/youtubei/v1/player?prettyPrint=false", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", clientAgent)
	req.Header.Set("X-YouTube-Client-Name", clientNumber)
	req.Header.Set("X-YouTube-Client-Version", clientVersion)
	req.Header.Set("X-Goog-Visitor-Id", visitor)
	req.Header.Set("Origin", Base)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("couldn't reach YouTube")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("YouTube answered HTTP %d", resp.StatusCode)
	}
	return parsePlayer(io.LimitReader(resp.Body, 8<<20), id)
}

// parsePlayer reads a player API answer.
func parsePlayer(r io.Reader, id string) (*Video, error) {
	var p struct {
		Playability struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"playabilityStatus"`
		Details struct {
			ID          string `json:"videoId"`
			Title       string `json:"title"`
			Length      string `json:"lengthSeconds"`
			Author      string `json:"author"`
			Description string `json:"shortDescription"`
			Upcoming    bool   `json:"isUpcoming"`
		} `json:"videoDetails"`
		Streaming struct {
			Adaptive []struct {
				Itag       int    `json:"itag"`
				URL        string `json:"url"`
				Cipher     string `json:"signatureCipher"`
				MIME       string `json:"mimeType"`
				Size       string `json:"contentLength"`
				Bitrate    int    `json:"bitrate"`
				SampleRate string `json:"audioSampleRate"`
				DRC        bool   `json:"isDrc"`
				Track      *struct {
					Default bool `json:"audioIsDefault"`
				} `json:"audioTrack"`
			} `json:"adaptiveFormats"`
		} `json:"streamingData"`
		Captions struct {
			List struct {
				Tracks []struct {
					URL  string `json:"baseUrl"`
					Lang string `json:"languageCode"`
					Kind string `json:"kind"`
					Name struct {
						Runs []struct {
							Text string `json:"text"`
						} `json:"runs"`
						Simple string `json:"simpleText"`
					} `json:"name"`
				} `json:"captionTracks"`
			} `json:"playerCaptionsTracklistRenderer"`
		} `json:"captions"`
	}
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return nil, errors.New("YouTube's answer was broken")
	}
	switch p.Playability.Status {
	case "OK":
	case "LOGIN_REQUIRED", "AGE_CHECK_REQUIRED", "CONTENT_CHECK_REQUIRED":
		return nil, fmt.Errorf("%w: %s", ErrRefused, cleanReason(p.Playability.Reason))
	default:
		why := cleanReason(p.Playability.Reason)
		if why == "" {
			why = strings.ToLower(p.Playability.Status)
		}
		return nil, fmt.Errorf("the video can't be played: %s", why)
	}
	if p.Details.ID != "" && p.Details.ID != id {
		return nil, errors.New("YouTube answered for another video")
	}
	if p.Details.Upcoming {
		return nil, errors.New("the video isn't out yet")
	}
	v := &Video{ID: id, Title: p.Details.Title, Channel: p.Details.Author, Description: p.Details.Description}
	v.Length, _ = strconv.ParseFloat(p.Details.Length, 64)
	for _, f := range p.Streaming.Adaptive {
		// AAC in MP4, at a plain address, the main language's track, without YouTube's dynamic
		// range compression
		if f.URL == "" || f.DRC || f.Track != nil && !f.Track.Default || !strings.HasPrefix(f.MIME, "audio/mp4") {
			continue
		}
		codec := ""
		if i := strings.Index(f.MIME, `codecs="`); i >= 0 {
			codec = strings.TrimSuffix(f.MIME[i+len(`codecs="`):], `"`)
		}
		if !strings.HasPrefix(codec, "mp4a.40.") {
			continue
		}
		size, _ := strconv.ParseInt(f.Size, 10, 64)
		rate, _ := strconv.Atoi(f.SampleRate)
		v.Audio = append(v.Audio, Format{Itag: f.Itag, URL: f.URL, Size: size, Codec: codec, SampleRate: rate, Bitrate: f.Bitrate})
	}
	for _, t := range p.Captions.List.Tracks {
		if t.URL == "" {
			continue
		}
		name := t.Name.Simple
		for _, r := range t.Name.Runs {
			name += r.Text
		}
		u := t.URL
		if strings.HasPrefix(u, "/") {
			u = Base + u
		}
		v.Captions = append(v.Captions, Track{Lang: t.Lang, Name: name, Auto: t.Kind == "asr", URL: u})
	}
	return v, nil
}

func cleanReason(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '’' {
			return '\''
		}
		return r
	}, s))
}

// Pick is the audio format for a copy of length seconds the Q2 can play: AAC-LC (itag 140) unless
// its index would be too big for the Q2's memory (Q2 Podcast's mp4_fits: 28 bytes a sample, with
// 16 MB to spare beside it, and about 45 MB free with Pocket running), then HE-AAC (itag 139),
// whose samples come half as often. ok false: none it can play.
func (v *Video) Pick() (Format, bool) {
	byItag := map[int]Format{}
	for _, f := range v.Audio {
		if _, dup := byItag[f.Itag]; !dup {
			byItag[f.Itag] = f
		}
	}
	lc, hasLC := byItag[140]
	he, hasHE := byItag[139]
	if hasLC && (!hasHE || Index(v.Length, lc) <= MaxIndex) {
		return lc, true
	}
	if hasHE {
		return he, true
	}
	for _, f := range v.Audio { // another AAC (a video with only some formats)
		if f.Codec == "mp4a.40.2" || f.Codec == "mp4a.40.5" {
			return f, true
		}
	}
	return Format{}, false
}

// MaxIndex is the most bytes the Q2's player may take indexing a copy (Q2 Podcast's
// MP4_SAMPLE_BYTES a sample) for it to play with Pocket running.
const MaxIndex = 22 << 20

// Index is about the bytes the Q2's player takes to index a copy of f, length seconds long.
func Index(length float64, f Format) int64 {
	rate := f.SampleRate
	if rate == 0 {
		rate = 44100
	}
	per := 1024.0 // AAC-LC: a sample (frame) is 1024 of its samples
	if f.Codec == "mp4a.40.5" {
		per = 2048 // HE-AAC: its rate is SBR's, its core's frames half as many
	}
	return int64(length*float64(rate)/per) * 28
}

// Caption is the captions to read for sponsor reads and to keep as the transcript: the uploader's
// own in English, else YouTube's speech recognition in English, else the uploader's in any
// language; ok false for none.
func (v *Video) Caption() (Track, bool) {
	best, score := Track{}, -1
	for _, t := range v.Captions {
		s := 0
		en := t.Lang == "en" || strings.HasPrefix(t.Lang, "en-")
		switch {
		case en && !t.Auto:
			s = 3
		case en:
			s = 2
		case !t.Auto:
			s = 1
		}
		if s > score {
			best, score = t, s
		}
	}
	return best, score >= 0
}

// Captions fetches a caption track as json3: its lines, and each word with its own time (a speech
// recognition track's WebVTT repeats each line in the next, and has no word's time).
func (c *Client) Captions(ctx context.Context, t Track) ([]byte, error) {
	u := t.URL
	if strings.Contains(u, "fmt=") {
		u = regexp.MustCompile(`fmt=[^&]*`).ReplaceAllString(u, "fmt=json3")
	} else {
		u += "&fmt=json3"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", clientAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("couldn't reach YouTube")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("YouTube answered HTTP %d for its captions", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(b[:min(len(b), 200)], []byte(`"events"`)) && !bytes.Contains(b[:min(len(b), 200)], []byte(`"wireMagic"`)) {
		return nil, errors.New("YouTube's captions weren't json3")
	}
	return b, nil
}

// Get asks for bytes [from, to] of a format's audio (inclusive, as a Range header), sent as the
// visionOS client.
func Get(ctx context.Context, h *http.Client, f Format, from, to int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", clientAgent)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
	return h.Do(req)
}

var embedRE = regexp.MustCompile(`(?:youtube(?:-nocookie)?\.com/(?:embed/|watch\?(?:[^"'\s<>]*&(?:amp;)?)?v=|live/|shorts/)|youtu\.be/)([A-Za-z0-9_-]{11})`)

// PageVideos is the videos a web page embeds (an episode's own page, say), each once: those in
// embeds first, then those it links to.
func PageVideos(page []byte) []string {
	var embeds, links []string
	seen := map[string]bool{}
	for _, m := range embedRE.FindAllSubmatchIndex(page, -1) {
		id := string(page[m[2]:m[3]])
		if seen[id] {
			continue
		}
		seen[id] = true
		if bytes.Contains(page[m[0]:m[2]], []byte("/embed/")) {
			embeds = append(embeds, id)
		} else {
			links = append(links, id)
		}
	}
	return append(embeds, links...)
}

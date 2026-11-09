// Package sponsor finds sponsor reads recorded into an episode itself, which aren't stitched in and
// so aren't adfree's to find: from SponsorBlock (the episode's YouTube video, or its Spotify
// episode through Spot SponsorBlock), from a transcript (the publisher's Podcasting 2.0 one, or
// Pocket Casts' own), and from chapters named as adverts. Each source has its own timeline; Fit
// says whether it is the episode's.
package sponsor

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Segment is seconds [From, To) of a source's timeline to cut, and what found it.
type Segment struct {
	From, To float64
	Why      string
}

// Cue is a transcript's line.
type Cue struct {
	From, To float64
	Text     string
}

// Transcript reads a transcript: WebVTT, SubRip or Podcasting 2.0 JSON (typ its MIME type; the
// bytes decide when it says nothing useful).
func Transcript(b []byte, typ string) []Cue {
	s := strings.TrimPrefix(string(b), "\ufeff")
	t := strings.TrimSpace(s)
	switch {
	case strings.Contains(typ, "json") || strings.HasPrefix(t, "{"):
		return jsonCues(b)
	case strings.Contains(typ, "html"):
		return nil
	}
	return timedCues(s)
}

var stamp = regexp.MustCompile(`((?:\d+:)?\d{1,2}:\d{2}[.,]\d{1,3})\s*-->\s*((?:\d+:)?\d{1,2}:\d{2}[.,]\d{1,3})`)
var tags = regexp.MustCompile(`<[^>]*>`)

// timedCues reads WebVTT and SubRip alike: a line with "-->" starts a cue, its text the lines up to
// a blank one.
func timedCues(s string) []Cue {
	var out []Cue
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		m := stamp.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		c := Cue{From: clock(m[1]), To: clock(m[2])}
		var text []string
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			text = append(text, strings.TrimSpace(tags.ReplaceAllString(lines[i], "")))
		}
		c.Text = strings.Join(text, " ")
		if c.To >= c.From && c.Text != "" {
			out = append(out, c)
		}
	}
	return out
}

func clock(s string) float64 {
	s = strings.ReplaceAll(s, ",", ".")
	parts := strings.Split(s, ":")
	v := 0.0
	for _, p := range parts {
		f, _ := strconv.ParseFloat(p, 64)
		v = v*60 + f
	}
	return v
}

func jsonCues(b []byte) []Cue {
	var r struct {
		Segments []struct {
			Start float64 `json:"startTime"`
			End   float64 `json:"endTime"`
			Body  string  `json:"body"`
		} `json:"segments"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	var out []Cue
	for _, s := range r.Segments {
		if t := strings.TrimSpace(s.Body); t != "" && s.End >= s.Start {
			out = append(out, Cue{s.Start, s.End, t})
		}
	}
	return out
}

// End is when a transcript's last line ends.
func End(cues []Cue) float64 {
	e := 0.0
	for _, c := range cues {
		e = max(e, c.To)
	}
	return e
}

// What a sponsor read says. A read starts with one of starts, says some of offers (an address, a
// code, a discount), and ends at one of backs or where offers stop.
var (
	starts = regexp.MustCompile(`(?i)\b(` +
		`(this|today'?s|the) (episode|show|podcast|program|programme|video)( is| was| has been)? (brought to you|sponsored|supported|made possible|presented|powered|underwritten) by|` +
		`support (for|of) (this|the|today'?s|our) (show|podcast|episode|program|programme) (comes?|is coming) from|` +
		`(this|the following) (message|podcast) (comes from|is supported by|is sponsored by)|` +
		`(is|are|was) (proudly )?(sponsored|brought to you) by|` +
		`(a|this|today'?s) (word|message) from (our|this week'?s|today'?s|one of our) sponsors?|` +
		`thanks? (you )?to (our sponsor|.{2,40}? for (sponsoring|supporting) (this|the|today'?s))|` +
		`(today'?s|this week'?s|our) (episode )?sponsor (is|this week)|` +
		`(let'?s|we'?ll|we'?re going to|gonna) take a (quick |short )?(break|pause)|` +
		`we'?ll be right back|after (the|this|a quick) break` +
		`)`)
	domain = `[a-z0-9-]{2,}(\.| dot )(com|co|io|net|org|ai|app|fm|tv|us|uk)\b`
	offers = regexp.MustCompile(`(?i)(` +
		`\b(promo|discount|offer|coupon) code\b|\b(use|with|enter) (the |my )?code\b|\bcode at checkout\b|` +
		`\b\d+ ?(%|percent) off\b|\bfree (trial|shipping|month)\b|\bfirst (month|order|box)\b|money[- ]back guarantee|risk[- ]free|` +
		`\b` + domain + `|` +
		`\bterms (and conditions )?apply\b|\blimited time\b|\bexclusive offer\b|\blink (is )?in the (show notes|description)\b` +
		`)`)
	backs = regexp.MustCompile(`(?i)\b(` +
		`(and )?(now|so|ok(ay)?|alright|all right),? (back to|let'?s get back to|on with) (the|our|today'?s) (show|episode|conversation|interview|program)|` +
		`back to the (show|episode|interview|conversation)|and we'?re back|welcome back|` +
		`now,? back to|let'?s get back (into it|to it)` +
		`)`)
)

const (
	readMin = 8.0   // seconds: a sponsor read is at least this long...
	readMax = 180.0 // ...and at most this
	quiet   = 25.0  // seconds without an offer after one end a read that says no "back to"
)

// Reads is the sponsor reads a transcript has: a line that introduces one ("this episode is brought
// to you by", "support for the show comes from", "let's take a quick break"), then up to a "back to
// the show", or to the end of its last line that offers something (an address, a code, a discount)
// once quiet seconds pass with no other; never past readMax or the next read's start. A read that
// offers nothing ("we'll be right back", with the episode after) isn't one.
func Reads(cues []Cue) []Segment {
	var out []Segment
	for i := 0; i < len(cues); i++ {
		if !starts.MatchString(cues[i].Text) {
			continue
		}
		from := cues[i].From
		end, last := -1.0, -1.0
		if offers.MatchString(cues[i].Text) {
			last = cues[i].To
		}
		for j := i + 1; j < len(cues) && cues[j].From-from <= readMax; j++ {
			c := cues[j]
			if backs.MatchString(c.Text) {
				end = c.To
				break
			}
			if starts.MatchString(c.Text) && last >= 0 { // the next read: this one ended
				break
			}
			if offers.MatchString(c.Text) {
				last = c.To
			} else if last >= 0 && c.From-last > quiet {
				break
			}
		}
		if last < 0 {
			continue // nothing offered: not a read
		}
		if end < 0 {
			end = last
		}
		if end-from < readMin || end-from > readMax {
			continue
		}
		out = append(out, Segment{from, end, "a sponsor read in the transcript"})
		for i+1 < len(cues) && cues[i+1].From < end {
			i++
		}
	}
	return merge(out)
}

// merge is segments sorted and joined where they overlap.
func merge(s []Segment) []Segment {
	sort.Slice(s, func(i, j int) bool { return s[i].From < s[j].From })
	var out []Segment
	for _, x := range s {
		if n := len(out); n > 0 && x.From <= out[n-1].To {
			out[n-1].To = max(out[n-1].To, x.To)
			continue
		}
		out = append(out, x)
	}
	return out
}

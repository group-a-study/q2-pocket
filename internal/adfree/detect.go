package adfree

import (
	"math"
	"sort"
	"time"

	"q2-pocket/internal/mp3"
)

// What found a cut, for the report on the Q2.
const (
	WhyCopies = "differs between copies"
	WhyHeard  = "a known advert"
	WhyPeer   = "also in another episode"
	WhyTwice  = "plays twice"
	WhyMarked = "marked by its encoder"
	WhyBreak  = "an ad break"
	WhyEnd    = "added after the episode"
	WhyLength = "the feed's length"
)

const (
	maxAd     = 180.0 // seconds: a longer stitched part is the episode
	maxRun    = 300.0 // seconds: a longer match is the same episode published twice, not an advert
	guessCap  = 0.35  // of the episode: breaks cut on their shape alone, at most
	budgetTol = 3.0   // seconds the feed's length may be out by
	learnMin  = 5.0   // seconds: adverts kept in the library are this long...
	learnMax  = 150.0 // ...to this
)

// Evidence is what Detect weighs besides the stream.
type Evidence struct {
	Copy   []uint64   // another copy of the episode, its frames' folded hashes; nil for none
	Peers  [][]uint64 // other episodes' folded hashes
	Lib    *Library   // adverts found before; nil for none
	Budget float64    // seconds the feed gives the episode, 0 if it gives none
	Now    time.Time
}

// Cut is frames [From, To) of the stream to leave out, and what found them.
type Cut struct {
	From, To int
	Why      string
}

// Result is Detect's answer.
type Result struct {
	Cuts  []Cut
	Learn [][]uint64 // adverts for the library (folded hashes), the other copy's among them
	Parts int        // how many stitched parts the episode is in
}

// Detect finds the adverts stitched into s.
func Detect(s *mp3.Stream, ev Evidence) Result {
	h := FoldAll(s.Hashes())
	f := fps(s)
	secs := func(n int) float64 { return float64(n) / f }
	frames := func(sec float64) int { return int(math.Round(sec * f)) }
	total := len(h) - s.Info
	var res Result
	var found []Cut // exact matches: cut whatever the parts say

	if ev.Copy != nil {
		chain := Chain(Runs(h, ev.Copy, frames(2), false))
		covered := 0
		for _, r := range chain {
			covered += r.N
		}
		if covered*2 >= total { // the same episode: what it lacks is advertising
			for _, g := range gaps(chain, len(h), frames(1), false) {
				if g.From < s.Info {
					g.From = s.Info
				}
				if g.To-g.From >= frames(1) && secs(g.To-g.From) <= maxRun {
					found = append(found, Cut{g.From, g.To, WhyCopies})
				}
			}
			for _, g := range gaps(chain, len(ev.Copy), frames(learnMin), true) {
				if secs(g.To-g.From) <= learnMax {
					res.Learn = append(res.Learn, ev.Copy[g.From:g.To])
				}
			}
		}
	}
	if ev.Lib != nil {
		for _, sp := range ev.Lib.Find(h, frames(3), ev.Now) {
			found = append(found, Cut{sp.From, sp.To, WhyHeard})
		}
	}
	for _, p := range ev.Peers {
		runs := Runs(h, p, frames(3), false)
		same := false
		for _, r := range runs {
			same = same || r.N*2 > total
		}
		if same { // the same audio in another feed: it says nothing about adverts
			continue
		}
		for _, r := range runs {
			if secs(r.N) <= maxRun {
				found = append(found, Cut{r.A, r.A + r.N, WhyPeer})
			}
		}
	}
	for _, r := range Runs(h, h, frames(5), true) {
		if secs(r.N) <= maxAd {
			found = append(found, Cut{r.A, r.A + r.N, WhyTwice})
		}
	}

	parts := Parts(s)
	res.Parts = len(parts)
	sig := contentSig(s, parts, f)
	var guessed []Cut // parts cut on their shape alone
	var cuts []Cut
	first := -1 // a short first part nothing has settled yet
	long := func(k int) bool { return k >= 0 && k < len(parts) && secs(parts[k].To-parts[k].From) > maxAd }
	anyLong := false
	for k := range parts {
		anyLong = anyLong || long(k)
	}
	for k, p := range parts {
		if long(k) || len(parts) == 1 {
			continue
		}
		if c, ok := cover(found, p); ok {
			cuts = append(cuts, c)
			continue
		}
		if partSig(s, p.Span) != sig {
			cuts = append(cuts, Cut{p.From, p.To, WhyMarked})
			continue
		}
		if !anyLong { // no long part: nothing says which short ones are the episode
			continue
		}
		// the break k is in: the short parts between long ones (or an end)
		i, j := k, k
		for i > 0 && !long(i-1) {
			i--
		}
		for j < len(parts)-1 && !long(j+1) {
			j++
		}
		switch {
		case i > 0 && j < len(parts)-1:
			guessed = append(guessed, Cut{p.From, p.To, WhyBreak})
		case i > 0:
			guessed = append(guessed, Cut{p.From, p.To, WhyEnd})
		case k > i: // the episode's first break, after its first part
			guessed = append(guessed, Cut{p.From, p.To, WhyBreak})
		default: // the first part: an advert, or the episode's cold open
			first = k
		}
	}
	g := 0
	for _, c := range guessed {
		g += c.To - c.From
	}
	if float64(g) <= guessCap*float64(total) {
		cuts = append(cuts, guessed...)
	}
	cuts = append(cuts, found...)
	cuts = union(cuts)
	if first >= 0 && ev.Budget > 0 { // the feed's length accounts for the first part: an advert too
		p := parts[first]
		left := secs(total - length(cuts))
		if math.Abs(left-secs(p.To-p.From)-ev.Budget) <= budgetTol && math.Abs(left-ev.Budget) > budgetTol {
			cuts = union(append(cuts, Cut{p.From, p.To, WhyLength}))
		}
	}
	res.Cuts = cuts
	for _, c := range cuts {
		if d := secs(c.To - c.From); d >= learnMin && d <= learnMax && c.Why != WhyHeard {
			res.Learn = append(res.Learn, h[c.From:c.To])
		}
	}
	return res
}

// cover is part p cut, when exact matches cover at least half of it: to its joins where those are
// exact, and to the matches' ends where a join is soft. ok false when they don't.
func cover(found []Cut, p Part) (Cut, bool) {
	n, why := 0, ""
	lo, hi := p.To, p.From // the matches' extent in the part
	for _, c := range found {
		a, b := max(c.From, p.From), min(c.To, p.To)
		if b > a {
			n += b - a
			lo, hi = min(lo, a), max(hi, b)
			if why == "" {
				why = c.Why
			}
		}
	}
	if n*2 < p.To-p.From {
		return Cut{}, false
	}
	out := Cut{p.From, p.To, why}
	if p.SoftFrom {
		out.From = lo
	}
	if p.SoftTo {
		out.To = hi
	}
	return out, true
}

// contentSig is the header bits of the episode's own frames: those of its long parts, or of most
// frames when no part is long.
func contentSig(s *mp3.Stream, parts []Part, f float64) uint32 {
	n := map[uint32]int{}
	long := false
	for _, p := range parts {
		if float64(p.To-p.From)/f > maxAd {
			long = true
		}
	}
	for _, p := range parts {
		if long && float64(p.To-p.From)/f <= maxAd {
			continue
		}
		for _, fr := range s.Frames[p.From:p.To] {
			n[fr.Sig()]++
		}
	}
	return top(n)
}

func partSig(s *mp3.Stream, p Span) uint32 {
	n := map[uint32]int{}
	for _, fr := range s.Frames[p.From:p.To] {
		n[fr.Sig()]++
	}
	return top(n)
}

func top(n map[uint32]int) uint32 {
	best, k := uint32(0), -1
	for v, c := range n {
		if c > k || c == k && v < best {
			best, k = v, c
		}
	}
	return best
}

// union is cuts merged where they meet or overlap, in order; a merged cut keeps the first one's
// reason.
func union(cuts []Cut) []Cut {
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].From < cuts[j].From })
	var out []Cut
	for _, c := range cuts {
		if n := len(out); n > 0 && c.From <= out[n-1].To {
			out[n-1].To = max(out[n-1].To, c.To)
			continue
		}
		out = append(out, c)
	}
	return out
}

func length(cuts []Cut) int {
	n := 0
	for _, c := range cuts {
		n += c.To - c.From
	}
	return n
}

// Ranges is cuts as mp3.Write takes them.
func Ranges(cuts []Cut) []mp3.Range {
	out := make([]mp3.Range, len(cuts))
	for i, c := range cuts {
		out[i] = mp3.Range{From: c.From, To: c.To}
	}
	return out
}

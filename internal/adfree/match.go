// Package adfree finds the adverts a podcast's host stitched into an episode as it served it
// (dynamic ad insertion), from the frames alone (package mp3). Four things give one away:
//
//   - Another copy of the episode, fetched again, carries other adverts: the frames the copies
//     share are the episode, the rest adverts.
//   - An advert is the same bytes wherever it is put: frames this episode shares with another
//     episode, or with an advert found before (Library), or that it carries twice, are adverts. An
//     episode's own audio is encoded once, as a whole, so it never matches anything byte for byte
//     (silence aside, which is left out).
//   - Each stitched part starts with a fresh encoder, which shows in its frames: the padding
//     pattern starts again, the bit reservoir is empty, and often the header's bits differ (splice.go).
//     The episode is cut into a few long parts at the ad breaks; the short parts between them are
//     the adverts.
//   - The feed's length for the episode is often the episode's without its adverts.
//
// Sponsor reads recorded into the episode itself aren't stitched; package sponsor finds those.
package adfree

import (
	"sort"
)

const (
	shingle = 4 // frames hashed together to find a match's seed
	sample  = 4 // one shingle in this many (by its hash) is indexed: a run of 3 s has ~28
)

// Run is n frames of a starting at A that are the same as n frames of b starting at B.
type Run struct{ A, B, N int }

type entry struct {
	h   uint64
	pos int32
}

// index is b's sampled shingles, sorted by hash.
type index struct {
	e []entry
}

func shingleAt(h []uint64, i int) uint64 {
	x := uint64(0x9e3779b97f4a7c15)
	for k := 0; k < shingle; k++ {
		x = (x ^ h[i+k]) * 0x100000001b3
		x ^= x >> 29
	}
	return x
}

func sampled(s uint64) bool { return s%sample == 0 }

func newIndex(h []uint64) *index {
	ix := &index{}
	for i := 0; i+shingle <= len(h); i++ {
		if s := shingleAt(h, i); sampled(s) {
			ix.e = append(ix.e, entry{s, int32(i)})
		}
	}
	sort.Slice(ix.e, func(i, j int) bool {
		if ix.e[i].h != ix.e[j].h {
			return ix.e[i].h < ix.e[j].h
		}
		return ix.e[i].pos < ix.e[j].pos
	})
	return ix
}

// find is the positions of s in the index, at most max.
func (ix *index) find(s uint64, max int) []int32 {
	i := sort.Search(len(ix.e), func(i int) bool { return ix.e[i].h >= s })
	var out []int32
	for ; i < len(ix.e) && ix.e[i].h == s && len(out) < max; i++ {
		out = append(out, ix.e[i].pos)
	}
	return out
}

// distinct is whether frames [from, from+n) of h are varied enough to be sound rather than
// silence, which encodes the same anywhere: more than 3 in 5 of its frames unlike the one before.
func distinct(h []uint64, from, n int) bool {
	same := 0
	for i := from + 1; i < from+n; i++ {
		if h[i] == h[i-1] {
			same++
		}
	}
	return same*5 < n*2
}

// Runs is every stretch of at least min frames of a that b also has, longest first where they
// overlap in a. self: a and b are the same file, and a run must not be matched with itself.
func Runs(a, b []uint64, min int, self bool) []Run {
	if len(a) < shingle || len(b) < shingle {
		return nil
	}
	ix := newIndex(b)
	var runs []Run
	for i := 0; i+shingle <= len(a); i++ {
		s := shingleAt(a, i)
		if !sampled(s) {
			continue
		}
		best := Run{}
		for _, p := range ix.find(s, 8) {
			j := int(p)
			if self && j == i {
				continue
			}
			x, y := i, j
			for x > 0 && y > 0 && a[x-1] == b[y-1] {
				x, y = x-1, y-1
			}
			n := 0
			for x+n < len(a) && y+n < len(b) && a[x+n] == b[y+n] {
				n++
			}
			if d := max(x-y, y-x); self && n > d { // a stretch overlapping its own copy (silence)
				n = d
			}
			if n > best.N {
				best = Run{x, y, n}
			}
		}
		if best.N >= min && distinct(a, best.A, best.N) {
			runs = append(runs, best)
			i = max(i, best.A+best.N-shingle) // past it
		}
	}
	return merge(runs)
}

// merge drops runs contained in a longer one (in a) and sorts them by A.
func merge(runs []Run) []Run {
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].A != runs[j].A {
			return runs[i].A < runs[j].A
		}
		return runs[i].N > runs[j].N
	})
	var out []Run
	for _, r := range runs {
		if n := len(out); n > 0 && r.A+r.N <= out[n-1].A+out[n-1].N {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Chain is the runs, in order in both a and b, that cover the most of a: two copies of one episode,
// lined up. Runs out of order (an advert that is in both, at other places) are left out of it.
func Chain(runs []Run) []Run {
	if len(runs) == 0 {
		return nil
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].A < runs[j].A })
	best := make([]int, len(runs))
	prev := make([]int, len(runs))
	top := 0
	for i, r := range runs {
		best[i], prev[i] = r.N, -1
		for k := 0; k < i; k++ {
			q := runs[k]
			if q.A+q.N <= r.A && q.B+q.N <= r.B && best[k]+r.N > best[i] {
				best[i], prev[i] = best[k]+r.N, k
			}
		}
		if best[i] > best[top] {
			top = i
		}
	}
	var out []Run
	for i := top; i >= 0; i = prev[i] {
		out = append(out, runs[i])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Span is frames [From, To).
type Span struct{ From, To int }

// gaps is the frames of [0, n) that no run covers (side a, or b when inB), each at least min long.
func gaps(chain []Run, n, min int, inB bool) []Span {
	var out []Span
	at := 0
	add := func(to int) {
		if to-at >= min {
			out = append(out, Span{at, to})
		}
	}
	for _, r := range chain {
		start := r.A
		if inB {
			start = r.B
		}
		add(start)
		at = start + r.N
	}
	add(n)
	return out
}

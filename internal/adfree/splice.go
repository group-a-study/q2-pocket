package adfree

import (
	"sort"

	"q2-pocket/internal/mp3"
)

// A stitched part starts with a fresh encoder, and its frames show it.
//
// Padding: a constant-bitrate frame at 44.1 kHz is a fraction of a byte longer than a whole number
// of bytes, so the encoder pads some frames with a byte to keep the bitrate. LAME (as every encoder
// that keeps the rate exactly) decides with a counter: it takes f (the rate's remainder) from it at
// each frame and pads when it goes below zero, adding the sample rate back. Which frames are padded
// is then set, for all an encoder writes, by where its counter started. A stitched part's encoder
// started anywhere, so at a join the padding stops fitting any one counter: Splices follows every
// counter value the frames so far allow and marks a join where none is left.
//
// The bit reservoir: a fresh encoder's first frame has none (main_data_begin 0). The header: the
// parts of another encoder often differ in its bits (the "original" bit, in AdsWizz's adverts). And a
// stitched part may bring its own Xing/Info frame. These place a join exactly. Where the episode
// carries on after an advert, its own encoder's frames resume with none of them; the join is then
// only known to the frames between two padding changes, and is marked soft.

const (
	joinWithin = 1.0 // seconds: splices this close together are one
	minPart    = 2.0 // seconds: a shorter part is the join's and goes with the next
	maxBreaks  = 30  // seconds of stream a padding break may come every, at the most: more, and
	// the encoder pads by some other rule, which says nothing (a burst of breaks counts once)
)

// Splice is where a stitched part starts; Soft when that is only known to a few frames, the part
// placed at the earliest of them.
type Splice struct {
	At   int
	Soft bool
}

// counters is the counter values a run of frames allows: intervals [lo, hi) of [0, mod).
type counters struct {
	iv     [][2]int
	f, mod int
}

func (c *counters) reset() { c.iv = [][2]int{{0, c.mod}} }

// step takes a frame: false when no counter value allows its padding (and the counters start over
// from it).
func (c *counters) step(pad bool) bool {
	keep := func() [][2]int {
		var out [][2]int
		lo, hi := c.f, c.mod // unpadded: the counter was f or more
		if pad {
			lo, hi = 0, c.f
		}
		for _, v := range c.iv {
			a, b := max(v[0], lo), min(v[1], hi)
			if a < b {
				out = append(out, [2]int{a, b})
			}
		}
		return out
	}
	ok := true
	next := keep()
	if len(next) == 0 {
		ok = false
		c.reset()
		next = keep()
	}
	// the counter after the frame: f less, the sample rate added back below zero
	c.iv = c.iv[:0]
	for _, v := range next {
		a, b := v[0]-c.f, v[1]-c.f
		switch {
		case b <= 0:
			c.iv = append(c.iv, [2]int{a + c.mod, b + c.mod})
		case a >= 0:
			c.iv = append(c.iv, [2]int{a, b})
		default:
			c.iv = append(c.iv, [2]int{a + c.mod, c.mod}, [2]int{0, b})
		}
	}
	return ok
}

// rate is the padding counter's step and modulus for a frame's header: 0 when the bitrate needs no
// padding.
func rate(s *mp3.Stream, fr mp3.Frame) (int, int) {
	coef := 144000
	if s.Layer == 3 && s.Version != 1 {
		coef = 72000
	}
	kbps := kbpsOf(s, fr.Bitrate())
	if kbps == 0 {
		return 0, 0
	}
	return coef * kbps % s.SampleRate, s.SampleRate
}

func kbpsOf(s *mp3.Stream, i int) int {
	l3v1 := [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	l2v1 := [16]int{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0}
	lsf := [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	switch {
	case s.Version != 1:
		return lsf[i]
	case s.Layer == 2:
		return l2v1[i]
	}
	return l3v1[i]
}

// Splices is where the stream's stitched parts start, in order.
func Splices(s *mp3.Stream) []Splice {
	f := s.Frames
	lo := s.Info
	if len(f)-lo < 100 {
		return nil
	}
	var at []Splice
	if steady(f[lo:]) { // padding says something only where the bitrate holds
		var c *counters
		var breaks []int
		for i := lo; i < len(f); i++ {
			if i == lo || f[i].Bitrate() != f[i-1].Bitrate() {
				c = nil
				if step, mod := rate(s, f[i]); step > 0 {
					c = &counters{f: step, mod: mod}
					c.reset()
				}
				if i > lo { // another bitrate: another encoder
					at = append(at, Splice{At: i})
				}
			}
			if c != nil && !c.step(f[i].Pad()) && i > lo {
				breaks = append(breaks, i)
			}
		}
		bursts := 0 // breaks a second or more after the one before: frame-by-frame junk is one
		for k, i := range breaks {
			if k == 0 || s.Seconds(breaks[k-1], i) >= joinWithin {
				bursts++
			}
		}
		step, mod := rate(s, f[lo])
		if secs := s.Seconds(lo, len(f)); step > 0 && (bursts == 0 || secs/float64(bursts) >= maxBreaks) {
			period := mod / min(step, mod-step) // frames between padding changes, about
			for _, i := range breaks {
				at = append(at, exact(f, max(lo+1, i-period-1), i))
			}
		}
	}
	for i := lo + 1; i < len(f); i++ {
		// a reservoir emptied where the header changes, or a part's own Info frame
		if f[i].MDB == 0 && f[i-1].MDB != 0 && f[i].Sig() != f[i-1].Sig() || f[i].Tag {
			at = append(at, Splice{At: i})
		}
	}
	sort.Slice(at, func(i, j int) bool { return at[i].At < at[j].At })
	gap := int(joinWithin * fps(s))
	var out []Splice
	for _, sp := range at {
		if sp.At <= lo {
			continue
		}
		if n := len(out); n > 0 && sp.At-out[n-1].At < gap {
			if out[n-1].Soft && !sp.Soft { // the exact one of two for the same join
				out[n-1] = sp
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}

func fps(s *mp3.Stream) float64 { return float64(s.SampleRate) / float64(s.Samples) }

// steady is whether nine frames in ten or more have one bitrate: a constant-bitrate episode, maybe
// with adverts of another bitrate in it.
func steady(f []mp3.Frame) bool {
	n := map[int]int{}
	top := 0
	for _, fr := range f {
		n[fr.Bitrate()]++
		top = max(top, n[fr.Bitrate()])
	}
	return top*10 >= len(f)*9
}

// exact is where in [from, to] a part started, the padding having broken at to: where the header
// changes, else at the last run of frames with an empty reservoir; else it is soft, at from.
func exact(f []mp3.Frame, from, to int) Splice {
	for i := from; i <= to; i++ {
		if f[i].Sig() != f[i-1].Sig() {
			return Splice{At: i}
		}
	}
	for i := to; i >= from; i-- {
		if f[i].MDB == 0 {
			for i > from && f[i-1].MDB == 0 {
				i--
			}
			return Splice{At: i}
		}
	}
	return Splice{At: from, Soft: true}
}

// Part is a stitched part's frames; SoftFrom and SoftTo when a join is only known to a few frames.
type Part struct {
	Span
	SoftFrom, SoftTo bool
}

// Parts is the stream's audio frames cut at its splices, a part shorter than minPart going with the
// next (the last with the one before).
func Parts(s *mp3.Stream) []Part {
	var out []Part
	at, soft := s.Info, false
	min := int(minPart * fps(s))
	for _, sp := range append(Splices(s), Splice{At: len(s.Frames)}) {
		c := sp.At
		if c-at < min && c != len(s.Frames) {
			continue // joins the next
		}
		if c-at < min && len(out) > 0 {
			out[len(out)-1].To = c
		} else {
			out = append(out, Part{Span{at, c}, soft, sp.Soft})
		}
		at, soft = c, sp.Soft
	}
	return out
}

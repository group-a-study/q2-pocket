package sponsor

import "sort"

// A transcript made from another copy of the episode than the one downloaded (Pocket Casts makes
// its own from the copy its server was sent) carries that copy's adverts: where the host put other
// adverts in, or none, its timeline isn't the download's and Fit can't place it (the user's,
// 2026-11-16: an episode's transcript ran four minutes longer than the download, two sponsor reads
// the host had put in for Pocket Casts' copy and left empty in the user's, and the transcript's
// reads were all passed over). Place lines it up through the download's ad slots.

// Slot is a stretch of the download, in its seconds, where the host may have put an advert in: one
// cut as an advert, or a digital silence it leaves an empty slot as.
type Slot struct {
	From, To float64
	Ad       bool // cut as an advert: the transcript's copy may lack it
}

const (
	anchorBefore = 4.0  // seconds a read may start before where its slot falls in the transcript...
	anchorAfter  = 15.0 // ...or after it (the slot before ended later than its read's last words)
	chainGap     = 6.0  // seconds between two reads one slot holds
	placeTol     = 12.0 // seconds the transcript's copy may end short of its last words, the last
	// slot's read having ended before its slot did
	maxBranches = 10 // slots cut as adverts that the transcript's copy may lack, tried both ways
)

// block is a stretch where the two copies differ: the download's [OFrom, OTo) is the transcript's
// [TFrom, TTo).
type block struct{ TFrom, TTo, OFrom, OTo float64 }

// Placed is a transcript lined up with the download.
type Placed struct {
	blocks []block
	Reads  []Segment // the transcript's reads the download has, in the download's seconds
	Slots  int       // the download's slots the transcript's copy has a read in
}

// At is the download's second for the transcript's t; false where the download lacks it (an
// advert only the transcript's copy has).
func (p *Placed) At(t float64) (float64, bool) {
	at := t
	for _, b := range p.blocks {
		switch {
		case t < b.TFrom:
			return at, true
		case t < b.TTo:
			return 0, false
		}
		at = b.OTo + (t - b.TTo)
	}
	return at, true
}

// Place lines up a transcript (its cues) with a download of length seconds through its slots: the
// transcript's copy is taken to be the download with some slots' contents swapped for its own. A
// slot where the transcript has a read starting (once the slots before it are accounted for) held
// that read in the transcript's copy; a slot cut as an advert may hold nothing there. It is placed
// when one way of reading the slots puts a read at one at least and makes the transcript's copy as
// long as its words say (ending after its last words, by at most tail); nil when none does, or two
// disagree about what to cut.
func Place(cues []Cue, slots []Slot, length float64) *Placed {
	reads := Reads(cues)
	end := End(cues)
	if len(reads) == 0 || end <= 0 || length <= 0 {
		return nil
	}
	// the episode's start and end are slots too: a pre-roll or post-roll only the transcript's copy has
	slots = append(append([]Slot{{From: 0, To: 0}}, slots...), Slot{From: length, To: length})
	sort.Slice(slots, func(i, j int) bool { return slots[i].From < slots[j].From })
	var merged []Slot // one slot of overlapping ones (an empty slot cut and its silence)
	for _, s := range slots {
		if n := len(merged); n > 0 && s.From <= merged[n-1].To {
			merged[n-1].To = max(merged[n-1].To, s.To)
			merged[n-1].Ad = merged[n-1].Ad || s.Ad
			continue
		}
		merged = append(merged, s)
	}
	slots = merged
	branchy := 0
	for _, s := range slots {
		if s.Ad {
			branchy++
		}
	}
	if branchy > maxBranches {
		return nil
	}
	var found []*Placed
	var try func(k int, shift float64, used []bool, blocks []block, anchors int)
	try = func(k int, shift float64, used []bool, blocks []block, anchors int) {
		if k == len(slots) {
			if total := length + shift; anchors > 0 && total >= end-placeTol && total <= end+tail {
				found = append(found, finish(reads, used, blocks, anchors))
			}
			return
		}
		s := slots[k]
		q := s.From + shift // where the slot falls in the transcript's copy
		for i, r := range reads {
			if used[i] || r.From < q-anchorBefore || r.From > q+anchorAfter {
				continue
			}
			// the transcript's copy holds reads here: from the slot to the last read's end
			u := append([]bool(nil), used...)
			to := r.To
			u[i] = true
			for j := i + 1; j < len(reads) && reads[j].From-to <= chainGap; j++ {
				to, u[j] = reads[j].To, true
			}
			lead := r.From - s.From // the shift up to here, from the read itself
			b := block{TFrom: s.From + lead, TTo: to, OFrom: s.From, OTo: s.To}
			if n := len(blocks); n > 0 && blocks[n-1].TTo > blocks[n-1].TFrom && blocks[n-1].OTo+lead > blocks[n-1].TFrom {
				// the read block before ended where this read says, not where its words did
				blocks = append(blocks[:n-1:n-1], block{blocks[n-1].TFrom, blocks[n-1].OTo + lead, blocks[n-1].OFrom, blocks[n-1].OTo})
			}
			try(k+1, to-s.To, u, append(blocks[:len(blocks):len(blocks)], b), anchors+1)
			return // a read at the slot: it is the slot's
		}
		try(k+1, shift, used, blocks, anchors) // the same in both copies
		if s.Ad {                              // or an advert the transcript's copy lacks
			try(k+1, shift-(s.To-s.From), used, append(blocks[:len(blocks):len(blocks)], block{q, q, s.From, s.To}), anchors)
		}
	}
	try(0, 0, make([]bool, len(reads)), nil, 0)
	if len(found) == 0 {
		return nil
	}
	best := found[0]
	for _, p := range found[1:] {
		if p.Slots > best.Slots {
			best = p
		}
	}
	for _, p := range found { // as many slots read another way: only if it cuts the same
		if p != best && p.Slots == best.Slots && !sameReads(p.Reads, best.Reads) {
			return nil
		}
	}
	return best
}

// finish is a way of reading the slots made a placing: the reads at no slot, which the download has
// too, in its seconds.
func finish(reads []Segment, used []bool, blocks []block, anchors int) *Placed {
	p := &Placed{blocks: blocks, Slots: anchors}
	for i, r := range reads {
		if used[i] {
			continue
		}
		from, ok1 := p.At(r.From)
		to, ok2 := p.At(r.To)
		if ok1 && ok2 && to > from {
			p.Reads = append(p.Reads, Segment{from, to, r.Why})
		}
	}
	return p
}

func sameReads(a, b []Segment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if d := a[i].From - b[i].From; d > 2 || d < -2 {
			return false
		}
		if d := a[i].To - b[i].To; d > 2 || d < -2 {
			return false
		}
	}
	return true
}

package main

import "q2-pocket/internal/pocketcasts"

// What a sync does about one downloaded episode, from where it is on each side now and where it was
// at the last sync. Pocket Casts gives no time a place changed, so a change is told by comparing with
// the last sync, and when both sides changed (or the episode is new to the sync) the further place
// wins on both: listening goes forward, so nothing heard is lost and neither side is set back. A place
// of 0 is never sent (a song begun again near its start, or its place forgotten); a place that goes
// near the end is the episode finished, and played on either side beats a place on the other.

type move int

const (
	none   move = iota
	push        // send the Q2's place (as served) to Pocket Casts
	pull        // put Pocket Casts' place (as cut) in Q2 Pod's ring
	played      // the Q2 finished it: mark it played, out of Up Next
	forget      // played elsewhere: forget the Q2's place
	settle      // nothing to send either way; note what each side has
)

// side is one sync's view of an episode. Places are seconds as served (Pocket Casts' timeline), the
// Q2's mapped from the cut episode; -1 for "not seen yet".
type side struct {
	Q2, Q2Was int  // the ring's place now (0: none), and at the last sync
	PC, PCWas int  // Pocket Casts' place now, and at the last sync
	Status    int  // Pocket Casts' playing status now
	Known     bool // Pocket Casts' state is known this sync (it is in a list asked for)
	Done      bool // played, as of the last sync
	NearEnd   bool // the Q2's place at the last sync was near the episode's end
}

// decide is the move, and the place it sends (push) or puts in the ring (pull, as served).
func decide(s side) (move, int) {
	q2Moved := s.Q2 != s.Q2Was
	pcMoved := s.Known && (s.PC != s.PCWas || s.Status == pocketcasts.Played && !s.Done)
	switch {
	case s.Q2 == 0 && s.Q2Was > 0 && s.NearEnd: // the place went at the end: finished on the Q2
		if s.Known && s.Status == pocketcasts.Played {
			return settle, 0
		}
		return played, 0
	case pcMoved && s.Status == pocketcasts.Played: // played elsewhere: the end is further than any place
		if s.Q2 > 0 {
			return forget, 0
		}
		return settle, 0
	case s.Q2 > 0 && q2Moved && pcMoved: // both moved (or new to the sync): the further wins
		switch {
		case abs(s.PC-s.Q2) < moved:
			return settle, 0
		case s.PC > s.Q2:
			return pull, s.PC
		}
		return push, s.Q2
	case s.Q2 > 0 && q2Moved: // only the Q2: sent, unless Pocket Casts is there already
		if s.Known && abs(s.Q2-s.PC) < moved {
			return settle, 0
		}
		return push, s.Q2
	case pcMoved && s.PC > 0 && (s.Q2 == 0 || abs(s.PC-s.Q2) >= moved): // only Pocket Casts moved
		return pull, s.PC
	case pcMoved && s.PC == 0 && s.Status != pocketcasts.Played && s.Q2 > 0 && !q2Moved: // marked unplayed elsewhere
		return forget, 0
	case q2Moved || pcMoved:
		return settle, 0
	}
	return none, 0
}

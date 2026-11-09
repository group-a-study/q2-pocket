package main

import "testing"

func TestDecide(t *testing.T) {
	const inprog, playedS, unplayed = 2, 3, 1
	for _, c := range []struct {
		name string
		s    side
		want move
		at   int
	}{
		{"new, nothing anywhere", side{Q2: 0, Q2Was: -1, PCWas: -1}, settle, 0},
		{"new, only the Q2 has a place, Pocket Casts not asked", side{Q2: 300, Q2Was: -1, PCWas: -1}, push, 300},
		{"new, Pocket Casts further", side{Q2: 300, Q2Was: -1, PC: 500, PCWas: -1, Status: inprog, Known: true}, pull, 500},
		{"new, the Q2 further", side{Q2: 500, Q2Was: -1, PC: 300, PCWas: -1, Status: inprog, Known: true}, push, 500},
		{"new, only Pocket Casts has a place", side{Q2: 0, Q2Was: -1, PC: 500, PCWas: -1, Status: inprog, Known: true}, pull, 500},
		{"played on the Q2", side{Q2: 400, Q2Was: 300, PC: 300, PCWas: 300, Status: inprog, Known: true}, push, 400},
		{"played on the Q2, Pocket Casts not asked", side{Q2: 400, Q2Was: 300, PCWas: 300}, push, 400},
		{"played elsewhere", side{Q2: 300, Q2Was: 300, PC: 600, PCWas: 300, Status: inprog, Known: true}, pull, 600},
		{"both, the Q2 further", side{Q2: 400, Q2Was: 300, PC: 350, PCWas: 300, Status: inprog, Known: true}, push, 400},
		{"both, Pocket Casts further", side{Q2: 400, Q2Was: 300, PC: 450, PCWas: 300, Status: inprog, Known: true}, pull, 450},
		{"both, the same place", side{Q2: 400, Q2Was: 300, PC: 405, PCWas: 300, Status: inprog, Known: true}, settle, 0},
		{"finished on the Q2", side{Q2: 0, Q2Was: 1700, PC: 1600, PCWas: 1600, Status: inprog, Known: true, NearEnd: true}, played, 0},
		{"finished on the Q2, Pocket Casts not asked", side{Q2: 0, Q2Was: 1700, PCWas: 1600, NearEnd: true}, played, 0},
		{"finished on both", side{Q2: 0, Q2Was: 1700, PC: 1800, PCWas: 1600, Status: playedS, Known: true, NearEnd: true}, settle, 0},
		{"played elsewhere, the Q2 mid-way", side{Q2: 300, Q2Was: 300, PC: 1800, PCWas: 300, Status: playedS, Known: true}, forget, 0},
		{"played elsewhere while the Q2 went on", side{Q2: 500, Q2Was: 300, PC: 1800, PCWas: 300, Status: playedS, Known: true}, forget, 0},
		{"begun again near its start on the Q2: no 0 sent", side{Q2: 0, Q2Was: 300, PC: 300, PCWas: 300, Status: inprog, Known: true}, settle, 0},
		{"a few seconds on: not worth a call", side{Q2: 310, Q2Was: 300, PC: 300, PCWas: 300, Status: inprog, Known: true}, settle, 0},
		{"marked unplayed elsewhere", side{Q2: 300, Q2Was: 300, PC: 0, PCWas: 300, Status: unplayed, Known: true}, forget, 0},
		{"played before, played again on the Q2", side{Q2: 100, Q2Was: 0, PC: 1800, PCWas: 1800, Status: playedS, Known: true, Done: true}, push, 100},
		{"nothing new", side{Q2: 300, Q2Was: 300, PC: 300, PCWas: 300, Status: inprog, Known: true}, none, 0},
		{"nothing new, Pocket Casts not asked", side{Q2: 300, Q2Was: 300, PCWas: 300}, none, 0},
	} {
		if got, at := decide(c.s); got != c.want || at != c.at {
			t.Errorf("%s: %v %d, want %v %d", c.name, got, at, c.want, c.at)
		}
	}
}

package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"q2-pocket/internal/q2"
)

// Q2 Pod keeps its place in each podcast it plays (navigation.c's resume ring, /mnt/data/ringnav-resume):
// 512 slots of {the path's FNV-1a hash, seconds in}, little-endian, most recent first; a place within
// 30 s of either end is forgotten. An episode's row shows how far in it is.

// placeKey is Q2 Pod's key for a song's path (coverflow.c fnv over FNV_SEED, never 0).
func placeKey(path string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(path); i++ {
		h = (h ^ uint32(path[i])) * 16777619
	}
	if h *= 16777619; h == 0 {
		h = 1
	}
	return h
}

// places is the resume ring by key, read again only when the file changes.
func (a *app) places() map[uint32]int {
	st, err := os.Stat(a.opts.resume)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if st.ModTime().Equal(a.placesAt) && a.placeMap != nil {
		return a.placeMap
	}
	b, err := os.ReadFile(a.opts.resume)
	if err != nil {
		return nil
	}
	m := map[uint32]int{}
	for i := 0; i+8 <= len(b); i += 8 {
		key, sec := binary.LittleEndian.Uint32(b[i:]), int32(binary.LittleEndian.Uint32(b[i+4:]))
		if _, seen := m[key]; key != 0 && sec > 0 && !seen {
			m[key] = int(sec)
		}
	}
	a.placesAt, a.placeMap = st.ModTime(), m
	return m
}

// recentPlace is the key of the place Q2 Pod saved last (the ring's first), 0 when there is none,
// and when the ring was last written.
func (a *app) recentPlace() (uint32, time.Time) {
	st, err := os.Stat(a.opts.resume)
	if err != nil {
		return 0, time.Time{}
	}
	f, err := os.Open(a.opts.resume)
	if err != nil {
		return 0, st.ModTime()
	}
	defer f.Close()
	var b [8]byte
	if _, err := f.ReadAt(b[:], 0); err != nil || int32(binary.LittleEndian.Uint32(b[4:])) <= 0 {
		return 0, st.ModTime()
	}
	return binary.LittleEndian.Uint32(b[:]), st.ModTime()
}

// setPlaces asks Q2 Pod to keep these places (seconds by song path; 0 forgets one) in its resume
// ring, so a song starts there: a file Q2 Pod reads and removes (addon.c), merged with one it hasn't
// read yet. false when the running Q2 Pod can't (it lists no "places" in its caps).
func (a *app) setPlaces(m map[string]int) bool {
	if !q2.HasCap("places") || len(m) == 0 {
		return false
	}
	all := map[string]int{}
	if b, err := os.ReadFile(a.opts.places); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			sec, path, ok := strings.Cut(line, "\t")
			if n, err := strconv.Atoi(sec); ok && err == nil && path != "" {
				all[path] = n
			}
		}
	}
	for p, n := range m {
		all[p] = max(n, 0)
	}
	var b strings.Builder
	for p, n := range all {
		fmt.Fprintf(&b, "%d\t%s\n", n, p)
	}
	return q2.WriteFile(a.opts.places, []byte(b.String())) == nil
}

// replaced is an episode on the card replaced by a copy cut again (Remove Ads Again), cuts the
// seconds of the old copy the new one leaves out (clean.Recut): the place Q2 Pod keeps in it moves
// to the same moment of the new copy, and while it plays Q2 Pod goes on there from the new copy
// (the .recut note, caps "recut"; the user's, 2026-11-16: the old copy played on, sponsor reads and
// all, though the new one was in place).
func (a *app) replaced(path string, cuts [][2]float64) {
	if len(cuts) == 0 {
		return
	}
	if sec, ok := a.places()[placeKey(path)]; ok {
		to := float64(sec)
		for _, c := range cuts {
			switch t := float64(sec); {
			case t >= c[1]:
				to -= c[1] - c[0]
			case t > c[0]:
				to -= t - c[0]
			}
		}
		a.setPlaces(map[string]int{path: int(math.Max(to, 1))})
	}
	if q2.HasCap("recut") {
		var b strings.Builder
		b.WriteString(path + "\n")
		for _, c := range cuts {
			fmt.Fprintf(&b, "%d\t%d\n", int(math.Round(c[0])), int(math.Round(c[1])))
		}
		q2.WriteFile(a.opts.recut, []byte(b.String()))
	}
}

// moved is an episode on the card replaced by a copy under another name (its MP3 by its YouTube
// video's M4A, or back), at mapping a second of the old copy to the new: the place Q2 Pod keeps in
// the old one goes to the new one, at the same moment.
func (a *app) moved(old, path string, at func(float64) float64) {
	if sec, ok := a.places()[placeKey(old)]; ok && sec > 0 {
		a.setPlaces(map[string]int{path: int(math.Max(at(float64(sec)), 1))})
	}
}

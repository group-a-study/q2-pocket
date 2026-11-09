package main

import (
	"encoding/binary"
	"fmt"
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

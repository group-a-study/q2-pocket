package adfree

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sort"
	"time"
)

// sentinel stands between two adverts in Library.all, so no match runs from one into the next: never
// a frame's hash as Fold leaves it.
const sentinel = ^uint32(0)

// Fold is a frame's hash as compared (mp3.Frame.Hash, 32 bits, which a run of a few seconds' frames
// makes certain enough), the sentinel's one value taken for its neighbour. Every hash Detect
// compares is folded.
func Fold(h uint32) uint32 {
	if h == sentinel {
		return sentinel - 1
	}
	return h
}

// FoldAll folds hashes in place.
func FoldAll(h []uint32) []uint32 {
	for i := range h {
		h[i] = Fold(h[i])
	}
	return h
}

// Library is the adverts found so far, kept on the card: a campaign runs for weeks, in many shows,
// so an advert heard once is known wherever it turns up again, even in an episode where every
// copy carries it. The least lately heard go once they pass maxFrames (about four hours of
// adverts). In memory it is the file's hashes in one row, 4 bytes a frame (2.4 MB full), and
// while it is matched against, an index of 2 bytes a frame (the user's, 2026-11-16: held as two
// copies of 8 bytes a frame, a full library took 15 MB of the Q2's memory in every check, and
// Pocket was killed for want of memory on episode after episode).
type Library struct {
	Path string
	all  []uint32 // every advert's hashes in a row, the sentinel after each: Runs matches against it
	ads  []ad     // in all's order
}

type ad struct {
	at, n      int32 // its hashes: all[at : at+n]
	seen, last int64 // unix seconds
}

func (a ad) h(all []uint32) []uint32 { return all[a.at : a.at+a.n] }

const (
	maxFrames = 600_000
	maxAdLen  = 1 << 16 // frames: a longer one is a file gone wrong
	libMagic  = "Q2ADS1\n"
)

// LoadLibrary reads the library at path; a missing or damaged one is empty (what was read whole
// before damage is kept).
func LoadLibrary(path string) *Library {
	l := &Library{Path: path}
	f, err := os.Open(path)
	if err != nil {
		return l
	}
	defer f.Close()
	r := bufio.NewReader(f)
	magic := make([]byte, len(libMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != libMagic {
		return l
	}
	if fi, err := f.Stat(); err == nil { // each advert's 12-byte head becomes its sentinel: room enough
		l.all = make([]uint32, 0, min(fi.Size()/4, maxFrames+maxAdLen+1<<12))
	}
	var head [12]byte
	b := make([]byte, 4<<10)
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			break
		}
		n := int(binary.LittleEndian.Uint32(head[:]))
		if n == 0 || n > maxAdLen {
			break
		}
		at := len(l.all)
		ok := true
		for left := n; left > 0; {
			k := min(left, len(b)/4)
			if _, err := io.ReadFull(r, b[:4*k]); err != nil {
				ok = false
				break
			}
			for i := 0; i < k; i++ {
				l.all = append(l.all, Fold(binary.LittleEndian.Uint32(b[4*i:])))
			}
			left -= k
		}
		if !ok {
			l.all = l.all[:at]
			break
		}
		l.all = append(l.all, sentinel)
		l.ads = append(l.ads, ad{int32(at), int32(n), int64(binary.LittleEndian.Uint32(head[4:])), int64(binary.LittleEndian.Uint32(head[8:]))})
	}
	return l
}

// CountLibrary is how many adverts the library at path knows, its heads read alone.
func CountLibrary(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	r := bufio.NewReader(f)
	magic := make([]byte, len(libMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != libMagic {
		return 0
	}
	count := 0
	var head [12]byte
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return count
		}
		n := int(binary.LittleEndian.Uint32(head[:]))
		if n == 0 || n > maxAdLen {
			return count
		}
		if k, err := r.Discard(4 * n); err != nil || k < 4*n {
			return count
		}
		count++
	}
}

// Len is how many adverts it knows.
func (l *Library) Len() int { return len(l.ads) }

// Frames is how many frames of adverts it holds.
func (l *Library) Frames() int { return len(l.all) - len(l.ads) }

// owner is the advert holding position p of all.
func (l *Library) owner(p int) int {
	return sort.Search(len(l.ads), func(i int) bool { return int(l.ads[i].at) > p }) - 1
}

// Find is the stretches of h (folded) that are known adverts: at least min frames, or nearly the
// whole of a shorter advert. Those found count as heard now.
func (l *Library) Find(h []uint32, min int, now time.Time) []Span {
	if len(l.ads) == 0 {
		return nil
	}
	var out []Span
	for _, r := range Runs(h, l.all, shortest, false) {
		a := &l.ads[l.owner(r.B)]
		if r.N >= min || r.N*10 >= int(a.n)*8 {
			out = append(out, Span{r.A, r.A + r.N})
			a.last = now.Unix()
		}
	}
	return out
}

// shortest is the fewest frames a match with a known advert may have.
const shortest = 40

// Add remembers an advert (folded hashes); one it knows already is marked heard.
func (l *Library) Add(h []uint32, now time.Time) { l.Learn([][]uint32{h}, now) }

// Learn remembers adverts (folded hashes), the library's index made once for them all; those it
// knows already (or that came earlier in hs) are marked heard.
func (l *Library) Learn(hs [][]uint32, now time.Time) {
	var ix *index
	from := len(l.ads) // the ones added here: matched against one by one, being few
	for _, h := range hs {
		if len(h) < shortest || len(h) > maxAdLen {
			continue
		}
		known := false
		if from > 0 {
			if ix == nil {
				ix = newIndex(l.all[:l.ads[from-1].at+l.ads[from-1].n+1])
			}
			for _, r := range runsIn(h, l.all, ix, shortest, false) {
				if r.N*10 >= len(h)*8 {
					l.ads[l.owner(r.B)].last = now.Unix()
					known = true
					break
				}
			}
		}
		for i := from; i < len(l.ads) && !known; i++ {
			for _, r := range Runs(h, l.ads[i].h(l.all), shortest, false) {
				if r.N*10 >= len(h)*8 {
					l.ads[i].last = now.Unix()
					known = true
					break
				}
			}
		}
		if known {
			continue
		}
		l.ads = append(l.ads, ad{int32(len(l.all)), int32(len(h)), now.Unix(), now.Unix()})
		l.all = append(l.all, h...)
		l.all = append(l.all, sentinel)
	}
	l.trim()
}

// trim lets the least lately heard adverts go until the rest fit maxFrames.
func (l *Library) trim() {
	if l.Frames() <= maxFrames {
		return
	}
	order := make([]int, len(l.ads))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return l.ads[order[i]].last > l.ads[order[j]].last })
	keep, n := make([]bool, len(l.ads)), 0
	for _, i := range order {
		if n += int(l.ads[i].n); n > maxFrames {
			break
		}
		keep[i] = true
	}
	all := make([]uint32, 0, n+len(l.ads))
	ads := l.ads[:0]
	for i, a := range l.ads {
		if !keep[i] {
			continue
		}
		at := len(all)
		all = append(all, a.h(l.all)...)
		all = append(all, sentinel)
		a.at = int32(at)
		ads = append(ads, a)
	}
	l.all, l.ads = all, ads
}

// Save writes the library aside and renames it into place.
func (l *Library) Save() error {
	if l.Path == "" {
		return errors.New("adfree: no path")
	}
	tmp := l.Path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 64<<10)
	w.WriteString(libMagic)
	var head [12]byte
	for _, a := range l.ads {
		binary.LittleEndian.PutUint32(head[:], uint32(a.n))
		binary.LittleEndian.PutUint32(head[4:], uint32(a.seen))
		binary.LittleEndian.PutUint32(head[8:], uint32(a.last))
		w.Write(head[:])
		var b [4]byte
		for _, v := range a.h(l.all) {
			binary.LittleEndian.PutUint32(b[:], uint32(v))
			w.Write(b[:])
		}
	}
	err = w.Flush()
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, l.Path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// SaveHashes keeps an episode's folded frame hashes at path, for later episodes to be compared with.
func SaveHashes(path string, h []uint32) error {
	b := make([]byte, 4*len(h))
	for i, v := range h {
		binary.LittleEndian.PutUint32(b[4*i:], uint32(v))
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// LoadHashes is SaveHashes' file read back, folded.
func LoadHashes(path string) ([]uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	h := make([]uint32, 0, fi.Size()/4) // read in pieces: the file's bytes never held whole beside it
	r := bufio.NewReaderSize(f, 16<<10)
	var b [4]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			break
		}
		h = append(h, Fold(binary.LittleEndian.Uint32(b[:])))
	}
	return h, nil
}

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

// Fold is a frame's hash as kept: 32 bits, which a run of a few seconds' frames makes certain
// enough. Every hash Detect compares is folded.
func Fold(h uint64) uint64 { return uint64(uint32(h ^ h>>32)) }

// FoldAll folds hashes in place.
func FoldAll(h []uint64) []uint64 {
	for i := range h {
		h[i] = Fold(h[i])
	}
	return h
}

// Library is the adverts found so far, kept on the card: a campaign runs for weeks, in many shows,
// so an advert heard once is known wherever it turns up again, even in an episode where every
// copy carries it. The least lately heard go once they pass maxFrames (about four hours of
// adverts, 5 MB of the Q2's memory while an episode is checked).
type Library struct {
	Path string
	ads  []ad
	all  []uint64 // every advert's hashes in a row, a sentinel between: Runs matches against it
	at   []int    // each advert's start in all
}

type ad struct {
	h          []uint64
	seen, last int64 // unix seconds
}

const (
	maxFrames = 600_000
	libMagic  = "Q2ADS1\n"
)

// LoadLibrary reads the library at path; a missing or damaged one is empty.
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
	var head [12]byte
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			break
		}
		n := binary.LittleEndian.Uint32(head[:])
		if n == 0 || n > 1<<16 {
			break
		}
		b := make([]byte, 4*n)
		if _, err := io.ReadFull(r, b); err != nil {
			break
		}
		h := make([]uint64, n)
		for i := range h {
			h[i] = uint64(binary.LittleEndian.Uint32(b[4*i:]))
		}
		l.ads = append(l.ads, ad{h, int64(binary.LittleEndian.Uint32(head[4:])), int64(binary.LittleEndian.Uint32(head[8:]))})
	}
	l.build()
	return l
}

// Len is how many adverts it knows.
func (l *Library) Len() int { return len(l.ads) }

func (l *Library) build() {
	l.all, l.at = l.all[:0], l.at[:0]
	for i, a := range l.ads {
		l.at = append(l.at, len(l.all))
		l.all = append(l.all, a.h...)
		l.all = append(l.all, sentinel(i)) // never a folded hash: those are under 1<<32
	}
}

func sentinel(i int) uint64 { return 1<<40 + uint64(i) }

// owner is the advert holding position p of all.
func (l *Library) owner(p int) int { return sort.SearchInts(l.at, p+1) - 1 }

// Find is the stretches of h (folded) that are known adverts: at least min frames, or nearly the
// whole of a shorter advert. Those found count as heard now.
func (l *Library) Find(h []uint64, min int, now time.Time) []Span {
	if len(l.ads) == 0 {
		return nil
	}
	var out []Span
	for _, r := range Runs(h, l.all, shortest, false) {
		a := &l.ads[l.owner(r.B)]
		if r.N >= min || r.N*10 >= len(a.h)*8 {
			out = append(out, Span{r.A, r.A + r.N})
			a.last = now.Unix()
		}
	}
	return out
}

// shortest is the fewest frames a match with a known advert may have.
const shortest = 40

// Add remembers an advert (folded hashes); one it knows already is marked heard.
func (l *Library) Add(h []uint64, now time.Time) {
	if len(h) < shortest || len(h) > 1<<16 {
		return
	}
	if len(l.ads) > 0 {
		for _, r := range Runs(h, l.all, shortest, false) {
			if r.N*10 >= len(h)*8 {
				l.ads[l.owner(r.B)].last = now.Unix()
				return
			}
		}
	}
	l.ads = append(l.ads, ad{append([]uint64(nil), h...), now.Unix(), now.Unix()})
	l.trim()
	l.build()
}

// trim lets the least lately heard adverts go until the rest fit maxFrames.
func (l *Library) trim() {
	sort.SliceStable(l.ads, func(i, j int) bool { return l.ads[i].last > l.ads[j].last })
	n := 0
	for k, a := range l.ads {
		if n += len(a.h); n > maxFrames {
			l.ads = l.ads[:k]
			return
		}
	}
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
		binary.LittleEndian.PutUint32(head[:], uint32(len(a.h)))
		binary.LittleEndian.PutUint32(head[4:], uint32(a.seen))
		binary.LittleEndian.PutUint32(head[8:], uint32(a.last))
		w.Write(head[:])
		var b [4]byte
		for _, v := range a.h {
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
func SaveHashes(path string, h []uint64) error {
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

// LoadHashes is SaveHashes' file read back.
func LoadHashes(path string) ([]uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	h := make([]uint64, len(b)/4)
	for i := range h {
		h[i] = uint64(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return h, nil
}

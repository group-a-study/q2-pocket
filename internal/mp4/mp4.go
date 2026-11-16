// Package mp4 turns YouTube's audio, a fragmented MP4 (a moov without samples, a sidx, then
// moof/mdat pairs), into a plain M4A the Q2 plays: the samples to keep written into one mdat, a moov
// that indexes them after it, the episode's tags (iTunes atoms) and its chapters (Nero's chpl, which
// ffmpeg reads) in that moov. It can be written over its own input (the mdat only ever behind what
// is still to be read), for a card without room for a copy. Taken from q2-applemusic's mp4 package
// (same authors), its decryption left out and its tables kept small: a 5-hour episode has about
// 800,000 samples, and the Q2 has little memory.
package mp4

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"unicode/utf8"
)

// ErrFormat is a file that isn't the fragmented MP4 audio expected.
var ErrFormat = errors.New("mp4: not a fragmented MP4 audio stream")

func u32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func u16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }

// boxAt reads the header of the box at b's start: its type, the header's length and the whole
// box's; size 0 runs to the end.
func boxAt(b []byte) (typ string, hdr, size int, ok bool) {
	if len(b) < 8 {
		return "", 0, 0, false
	}
	size, hdr, typ = int(u32(b)), 8, string(b[4:8])
	switch size {
	case 0:
		size = len(b)
	case 1:
		if len(b) < 16 {
			return "", 0, 0, false
		}
		big := binary.BigEndian.Uint64(b[8:])
		if big > uint64(len(b)) {
			return "", 0, 0, false
		}
		size, hdr = int(big), 16
	}
	if size < hdr || size > len(b) {
		return "", 0, 0, false
	}
	return typ, hdr, size, true
}

// child is the payload of the first box of type typ among the boxes in b.
func child(b []byte, typ string) []byte {
	for len(b) > 0 {
		t, hdr, size, ok := boxAt(b)
		if !ok {
			return nil
		}
		if t == typ {
			return b[hdr:size]
		}
		b = b[size:]
	}
	return nil
}

// path is the payload at the end of a path of boxes from b's.
func path(b []byte, types ...string) []byte {
	for _, t := range types {
		if b = child(b, t); b == nil {
			return nil
		}
	}
	return b
}

// each calls fn with each box in b: its type, its payload and the whole box.
func each(b []byte, fn func(typ string, payload, whole []byte)) {
	for len(b) > 0 {
		t, hdr, size, ok := boxAt(b)
		if !ok {
			return
		}
		fn(t, b[hdr:size], b[:size])
		b = b[size:]
	}
}

// track is the audio track of a fragmented file's moov.
type track struct {
	mvhd, tkhd, mdhd, hdlr, smhd, dinf []byte // copies, as the M4A has them
	entry                              []byte // the sample description, whole
	id, timescale                      uint32
	defDur, defSize                    uint32 // trex's defaults
}

func parseTrack(moov []byte) (*track, error) {
	var trak []byte
	each(moov, func(typ string, p, _ []byte) {
		if h := path(p, "mdia", "hdlr"); typ == "trak" && trak == nil && len(h) >= 12 && string(h[8:12]) == "soun" {
			trak = p
		}
	})
	if trak == nil {
		return nil, ErrFormat
	}
	cp := func(b []byte) []byte { return append([]byte(nil), b...) }
	tr := &track{mvhd: cp(child(moov, "mvhd")), tkhd: cp(child(trak, "tkhd"))}
	mdia := child(trak, "mdia")
	tr.mdhd, tr.hdlr = cp(child(mdia, "mdhd")), cp(child(mdia, "hdlr"))
	minf := child(mdia, "minf")
	tr.smhd, tr.dinf = cp(child(minf, "smhd")), cp(child(minf, "dinf"))
	stsd := path(minf, "stbl", "stsd")
	if len(tr.mvhd) < 20 || len(tr.tkhd) < 24 || len(tr.mdhd) < 24 || tr.hdlr == nil || len(stsd) < 8 {
		return nil, ErrFormat
	}
	if tr.tkhd[0] == 1 {
		tr.id = u32(tr.tkhd[20:])
	} else {
		tr.id = u32(tr.tkhd[12:])
	}
	if tr.mdhd[0] == 1 {
		tr.timescale = u32(tr.mdhd[20:])
	} else {
		tr.timescale = u32(tr.mdhd[12:])
	}
	each(stsd[8:], func(typ string, p, whole []byte) {
		if tr.entry == nil {
			tr.entry = cp(whole)
		}
	})
	if tr.entry == nil || tr.timescale == 0 {
		return nil, ErrFormat
	}
	each(child(moov, "mvex"), func(typ string, p, _ []byte) {
		if typ == "trex" && len(p) >= 24 && u32(p[4:]) == tr.id {
			tr.defDur, tr.defSize = u32(p[12:]), u32(p[16:])
		}
	})
	return tr, nil
}

// run is samples stored one after another in the file: where the first is, and its index.
type run struct {
	off   int64
	first int32
}

// Stream is a fragmented MP4's audio: its samples' sizes and durations, and where they are.
type Stream struct {
	tr    *track
	runs  []run
	sizes []uint16 // each sample's bytes (an AAC frame is never 64 KB)
	durs  []uint16 // each sample's duration, when they differ; nil when all are dur
	dur   uint32
	total uint64 // the samples' duration, in the timescale
}

// add appends a sample of size bytes and duration d.
func (st *Stream) add(size, d uint32) error {
	if size > math.MaxUint16 || d > math.MaxUint16 || len(st.sizes) >= math.MaxInt32 {
		return ErrFormat
	}
	if len(st.sizes) == 0 {
		st.dur = d
	}
	if st.durs == nil && d != st.dur { // the first that differs: every duration kept from here
		st.durs = make([]uint16, len(st.sizes), cap(st.sizes))
		for i := range st.durs {
			st.durs[i] = uint16(st.dur)
		}
	}
	st.sizes = append(st.sizes, uint16(size))
	if st.durs != nil {
		st.durs = append(st.durs, uint16(d))
	}
	st.total += uint64(d)
	return nil
}

// fragment reads a moof (at off in the file) for the track's samples.
func (st *Stream) fragment(moof []byte, off int64) error {
	tr := st.tr
	var err error
	each(moof, func(typ string, traf, _ []byte) {
		if typ != "traf" || err != nil {
			return
		}
		tfhd := child(traf, "tfhd")
		if len(tfhd) < 8 || u32(tfhd[4:]) != tr.id {
			return
		}
		flags := u32(tfhd) & 0xffffff
		base, dur, size := off, tr.defDur, tr.defSize
		p := 8
		take := func() uint32 {
			if p+4 > len(tfhd) {
				err = ErrFormat
				return 0
			}
			p += 4
			return u32(tfhd[p-4:])
		}
		if flags&1 != 0 {
			if p+8 > len(tfhd) {
				err = ErrFormat
				return
			}
			base = int64(binary.BigEndian.Uint64(tfhd[p:]))
			p += 8
		}
		if flags&2 != 0 {
			take() // its sample description: the M4A keeps the first
		}
		if flags&8 != 0 {
			dur = take()
		}
		if flags&0x10 != 0 {
			size = take()
		}
		pos := base
		each(traf, func(typ string, trun, _ []byte) {
			if typ != "trun" || err != nil {
				return
			}
			if len(trun) < 8 {
				err = ErrFormat
				return
			}
			f := u32(trun) & 0xffffff
			n := int(u32(trun[4:]))
			q := 8
			if f&1 != 0 {
				if q+4 > len(trun) {
					err = ErrFormat
					return
				}
				pos = base + int64(int32(u32(trun[q:])))
				q += 4
			}
			if f&4 != 0 {
				q += 4
			}
			per := 0
			for _, bit := range []uint32{0x100, 0x200, 0x400, 0x800} {
				if f&bit != 0 {
					per += 4
				}
			}
			if n < 0 || n > (len(trun)-q)/max(per, 1) && per > 0 {
				err = ErrFormat
				return
			}
			st.runs = append(st.runs, run{off: pos, first: int32(len(st.sizes))})
			for i := 0; i < n && err == nil; i++ {
				d, s := dur, size
				if f&0x100 != 0 {
					d = u32(trun[q:])
					q += 4
				}
				if f&0x200 != 0 {
					s = u32(trun[q:])
					q += 4
				}
				if f&0x400 != 0 {
					q += 4
				}
				if f&0x800 != 0 {
					q += 4
				}
				pos += int64(s)
				err = st.add(s, d)
			}
		})
	})
	return err
}

// Read indexes a fragmented MP4 of size bytes: its moov, then each moof's samples. Only the
// boxes' headers, the moov and the moofs are read.
func Read(r io.ReaderAt, size int64) (*Stream, error) {
	st := &Stream{}
	// about the samples a file this size holds (AAC at 128 kb/s: 370 bytes each), so the tables
	// don't grow by doubling
	st.sizes = make([]uint16, 0, size/300+16)
	hdr := make([]byte, 16)
	for off := int64(0); off < size; {
		if size-off < 8 {
			break
		}
		if _, err := r.ReadAt(hdr[:8], off); err != nil {
			return nil, err
		}
		n, h := int64(u32(hdr)), int64(8)
		typ := string(hdr[4:8])
		if n == 1 {
			if _, err := r.ReadAt(hdr[8:16], off+8); err != nil {
				return nil, err
			}
			n, h = int64(binary.BigEndian.Uint64(hdr[8:])), 16
		} else if n == 0 {
			n = size - off
		}
		if n < h || off+n > size {
			return nil, ErrFormat
		}
		if typ == "moov" || typ == "moof" {
			if n > 4<<20 {
				return nil, ErrFormat
			}
			b := make([]byte, n-h)
			if _, err := r.ReadAt(b, off+h); err != nil {
				return nil, err
			}
			if typ == "moov" {
				if child(b, "mvex") == nil {
					return nil, ErrFormat // a plain MP4, not a fragmented one
				}
				tr, err := parseTrack(b)
				if err != nil {
					return nil, err
				}
				st.tr = tr
			} else {
				if st.tr == nil {
					return nil, ErrFormat
				}
				if err := st.fragment(b, off); err != nil {
					return nil, err
				}
			}
		}
		off += n
	}
	if st.tr == nil || len(st.sizes) == 0 || st.dur == 0 && st.durs == nil {
		return nil, ErrFormat
	}
	for i, ru := range st.runs {
		end := int32(len(st.sizes))
		if i+1 < len(st.runs) {
			end = st.runs[i+1].first
		}
		bytes := int64(0)
		for _, s := range st.sizes[ru.first:end] {
			bytes += int64(s)
		}
		if ru.off < 0 || ru.off+bytes > size {
			return nil, ErrFormat
		}
	}
	return st, nil
}

// Samples is how many samples the stream has.
func (st *Stream) Samples() int { return len(st.sizes) }

// Duration is the stream's length in seconds.
func (st *Stream) Duration() float64 { return float64(st.total) / float64(st.tr.timescale) }

func (st *Stream) durOf(i int) uint32 {
	if st.durs != nil {
		return uint32(st.durs[i])
	}
	return st.dur
}

// SampleAt is the first sample starting at or after sec seconds (Samples() past the end).
func (st *Stream) SampleAt(sec float64) int {
	if sec <= 0 {
		return 0
	}
	t := uint64(math.Round(sec * float64(st.tr.timescale)))
	if st.durs == nil {
		return min(int((t+uint64(st.dur)-1)/uint64(st.dur)), len(st.sizes))
	}
	at := uint64(0)
	for i, d := range st.durs {
		if at >= t {
			return i
		}
		at += uint64(d)
	}
	return len(st.sizes)
}

// At is when sample i starts, in seconds.
func (st *Stream) At(i int) float64 {
	if st.durs == nil {
		return float64(uint64(i)*uint64(st.dur)) / float64(st.tr.timescale)
	}
	at := uint64(0)
	for _, d := range st.durs[:min(i, len(st.durs))] {
		at += uint64(d)
	}
	return float64(at) / float64(st.tr.timescale)
}

// Range is samples [From, To) to leave out.
type Range struct{ From, To int }

// Ranges is the samples seconds [from, to) of the stream cover, each from its first sample starting
// at or after from to the first at or after to; in order, joined where they meet.
func (st *Stream) Ranges(secs [][2]float64) []Range {
	var out []Range
	for _, s := range secs {
		r := Range{st.SampleAt(s[0]), st.SampleAt(s[1])}
		if r.To > r.From {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	var merged []Range
	for _, r := range out {
		if n := len(merged); n > 0 && r.From <= merged[n-1].To {
			merged[n-1].To = max(merged[n-1].To, r.To)
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

// Tags are an episode's iTunes atoms (moov/udta/meta/ilst), as TagLib reads them: the Q2's library
// and Now Playing take them from there. They match q2-pocket's ID3 tags (TIT2, TPE1, TALB, TCON).
type Tags struct {
	Title, Artist, Album, Date string
	Cover                      []byte // JPEG or PNG
}

// Chapter is a chapter's start (milliseconds of the M4A) and title.
type Chapter struct {
	Start uint32
	Title string
}

// node is a box to write: a payload, children, or a payload streamed by fn (n bytes).
type node struct {
	typ  string
	data []byte
	kids []*node
	n    int64
	fn   func(w io.Writer) error
}

func (b *node) size() int64 {
	s := int64(8 + len(b.data))
	for _, k := range b.kids {
		s += k.size()
	}
	return s + b.n
}

func (b *node) write(w io.Writer) error {
	var h [8]byte
	binary.BigEndian.PutUint32(h[:], uint32(b.size()))
	copy(h[4:], b.typ)
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	if _, err := w.Write(b.data); err != nil {
		return err
	}
	for _, k := range b.kids {
		if err := k.write(w); err != nil {
			return err
		}
	}
	if b.fn != nil {
		return b.fn(w)
	}
	return nil
}

func box(typ string, data []byte, kids ...*node) *node {
	return &node{typ: typ, data: data, kids: kids}
}

func full(typ string, version byte, flags uint32, data []byte, kids ...*node) *node {
	vf := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append(vf, data...), kids...)
}

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// chunkSamples is how many samples a chunk of the M4A holds: about a second of AAC, as iTunes
// writes them, so the Q2's index never has to take a whole episode as one.
const chunkSamples = 44

// Written is what Write wrote: the M4A's bytes, and its length in seconds.
type Written struct {
	Size   int64
	Length float64
}

// Write writes the stream to w as a plain M4A, its samples read from r, those in cuts (in order)
// left out: ftyp, mdat, then moov. w may be r's own file: every byte is written before any place
// still to be read, as the samples move only toward its start (Write fails if a file laid out
// otherwise would have them overtake). The caller cuts a file written over itself to Size.
func (st *Stream) Write(w io.WriterAt, r io.ReaderAt, cuts []Range, tags *Tags, chapters []Chapter) (Written, error) {
	keep := func(i int, c *int) bool { // cuts walked once, in order
		for *c < len(cuts) && cuts[*c].To <= i {
			*c++
		}
		return *c >= len(cuts) || i < cuts[*c].From
	}
	var kept, bytes, dur uint64
	c := 0
	for i, s := range st.sizes {
		if keep(i, &c) {
			kept++
			bytes += uint64(s)
			dur += uint64(st.durOf(i))
		}
	}
	if kept == 0 {
		return Written{}, errors.New("mp4: nothing left to write")
	}
	if bytes+8 > math.MaxUint32 {
		return Written{}, errors.New("mp4: too long for one mdat")
	}
	ftyp := box("ftyp", []byte("M4A \x00\x00\x00\x00M4A mp42isom\x00\x00\x00\x00"))
	out := &atWriter{w: w, buf: make([]byte, 0, 64<<10)}
	if err := ftyp.write(out); err != nil {
		return Written{}, err
	}
	mdat := uint32(out.pos())
	var mh [8]byte
	binary.BigEndian.PutUint32(mh[:], uint32(bytes+8))
	copy(mh[4:], "mdat")
	out.Write(mh[:])
	in := &sequential{r: r}
	buf := make([]byte, math.MaxUint16)
	c = 0
	for ri, ru := range st.runs {
		end := len(st.sizes)
		if ri+1 < len(st.runs) {
			end = int(st.runs[ri+1].first)
		}
		off := ru.off
		for i := int(ru.first); i < end; i++ {
			n := int(st.sizes[i])
			if !keep(i, &c) {
				off += int64(n)
				continue
			}
			if out.pos() > off {
				return Written{}, errors.New("mp4: written over what is still to be read")
			}
			if err := in.read(buf[:n], off); err != nil {
				return Written{}, err
			}
			if _, err := out.Write(buf[:n]); err != nil {
				return Written{}, err
			}
			off += int64(n)
		}
	}
	moov, err := st.moov(cuts, keep, kept, dur, mdat+8, tags, chapters)
	if err != nil {
		return Written{}, err
	}
	if err := moov.write(out); err != nil {
		return Written{}, err
	}
	if err := out.flush(); err != nil {
		return Written{}, err
	}
	return Written{Size: out.pos(), Length: float64(dur) / float64(st.tr.timescale)}, nil
}

// atWriter writes at a running offset, through a buffer.
type atWriter struct {
	w   io.WriterAt
	off int64 // where buf goes
	buf []byte
}

func (a *atWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		k := min(len(p), cap(a.buf)-len(a.buf))
		a.buf = append(a.buf, p[:k]...)
		p = p[k:]
		if len(a.buf) == cap(a.buf) {
			if err := a.flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

// pos is where the next byte written goes.
func (a *atWriter) pos() int64 { return a.off + int64(len(a.buf)) }

func (a *atWriter) flush() error {
	if len(a.buf) == 0 {
		return nil
	}
	_, err := a.w.WriteAt(a.buf, a.off)
	a.off += int64(len(a.buf))
	a.buf = a.buf[:0]
	return err
}

// sequential reads samples in file order through one buffer: a short gap (a moof between two
// fragments' samples) read past, a long one sought over.
type sequential struct {
	r   io.ReaderAt
	br  *bufio.Reader
	pos int64
}

func (q *sequential) read(b []byte, off int64) error {
	switch {
	case q.br == nil:
		q.br = bufio.NewReaderSize(io.NewSectionReader(q.r, off, 1<<62), 64<<10)
		q.pos = off
	case off > q.pos && off-q.pos <= 64<<10:
		n, err := q.br.Discard(int(off - q.pos))
		q.pos += int64(n)
		if err != nil {
			return err
		}
	case off != q.pos:
		q.br.Reset(io.NewSectionReader(q.r, off, 1<<62))
		q.pos = off
	}
	n, err := io.ReadFull(q.br, b)
	q.pos += int64(n)
	return err
}

// moov is the M4A's moov for the kept samples (kept of them, dur long), its mdat's samples from
// first: the input's mvhd, tkhd, mdhd, hdlr and smhd with the new duration, its sample
// description, the sample tables (chunks of chunkSamples), the tags and the chapters.
func (st *Stream) moov(cuts []Range, keep func(int, *int) bool, kept, dur uint64, first uint32, tags *Tags, chapters []Chapter) (*node, error) {
	tr := st.tr
	ts := uint64(tr.timescale)
	mvhd, tkhd, mdhd := append([]byte(nil), tr.mvhd...), append([]byte(nil), tr.tkhd...), append([]byte(nil), tr.mdhd...)
	movieTS := u32(mvhd[12:])
	if mvhd[0] == 1 {
		movieTS = u32(mvhd[20:])
	}
	if movieTS == 0 {
		movieTS = tr.timescale
	}
	movieDur := dur * uint64(movieTS) / ts
	setDuration(mvhd, 16, 24, movieDur)
	setDuration(tkhd, 20, 28, movieDur)
	setDuration(mdhd, 16, 24, dur)
	tkhd[3] |= 7 // enabled, in the movie and its preview
	smhd := tr.smhd
	if smhd == nil {
		smhd = make([]byte, 8)
	}
	dinf := box("dinf", tr.dinf)
	if tr.dinf == nil {
		dinf = box("dinf", nil, full("dref", 0, 0, be32(1), full("url ", 0, 1, nil)))
	}

	// stts: runs of one duration among the kept samples
	var stts []byte
	runs := uint32(0)
	var last, count uint32
	c := 0
	for i := range st.sizes {
		if !keep(i, &c) {
			continue
		}
		d := st.durOf(i)
		if count > 0 && d == last {
			count++
			continue
		}
		if count > 0 {
			stts = append(append(stts, be32(count)...), be32(last)...)
			runs++
		}
		last, count = d, 1
	}
	stts = append(append(stts, be32(count)...), be32(last)...)
	runs++
	// stsz streamed from the sizes (4 bytes a sample: the table not held twice)
	stsz := full("stsz", 0, 0, append(be32(0), be32(uint32(kept))...))
	stsz.n = 4 * int64(kept)
	stsz.fn = func(w io.Writer) error {
		b := make([]byte, 0, 16<<10)
		c := 0
		for i, s := range st.sizes {
			if !keep(i, &c) {
				continue
			}
			b = binary.BigEndian.AppendUint32(b, uint32(s))
			if len(b) == cap(b) {
				if _, err := w.Write(b); err != nil {
					return err
				}
				b = b[:0]
			}
		}
		_, err := w.Write(b)
		return err
	}
	// chunks of chunkSamples, the last one shorter: two stsc runs at most, an stco entry each
	chunks := (kept + chunkSamples - 1) / chunkSamples
	stsc := append(be32(1), be32(chunkSamples)...)
	stsc = append(stsc, be32(1)...)
	runsc := uint32(1)
	if lastN := kept - (chunks-1)*chunkSamples; lastN != chunkSamples {
		if chunks == 1 {
			stsc = append(append(be32(1), be32(uint32(lastN))...), be32(1)...)
		} else {
			stsc = append(stsc, be32(uint32(chunks))...)
			stsc = append(stsc, be32(uint32(lastN))...)
			stsc = append(stsc, be32(1)...)
			runsc = 2
		}
	}
	stco := make([]byte, 0, 4*chunks)
	off := first
	n := uint64(0)
	c = 0
	for i, s := range st.sizes {
		if !keep(i, &c) {
			continue
		}
		if n%chunkSamples == 0 {
			stco = append(stco, be32(off)...)
		}
		off += uint32(s)
		n++
	}
	stbl := box("stbl", nil,
		full("stsd", 0, 0, be32(1), &node{typ: string(tr.entry[4:8]), data: tr.entry[8:]}),
		full("stts", 0, 0, append(be32(runs), stts...)),
		full("stsc", 0, 0, append(be32(runsc), stsc...)),
		stsz,
		full("stco", 0, 0, append(be32(uint32(chunks)), stco...)))
	trak := box("trak", nil, box("tkhd", tkhd),
		box("mdia", nil, box("mdhd", mdhd), box("hdlr", tr.hdlr),
			box("minf", nil, box("smhd", smhd), dinf, stbl)))
	moov := box("moov", nil, box("mvhd", mvhd), trak)
	if u := udta(tags, chapters); u != nil {
		moov.kids = append(moov.kids, u)
	}
	if moov.size() > math.MaxUint32 {
		return nil, fmt.Errorf("mp4: index too big")
	}
	return moov, nil
}

// setDuration writes d at a version 0 box's offset v0 (32 bits) or a version 1's v1 (64 bits).
func setDuration(b []byte, v0, v1 int, d uint64) {
	if b[0] == 1 {
		if len(b) >= v1+8 {
			binary.BigEndian.PutUint64(b[v1:], d)
		}
		return
	}
	if d > math.MaxUint32 {
		d = math.MaxUint32
	}
	if len(b) >= v0+4 {
		binary.BigEndian.PutUint32(b[v0:], uint32(d))
	}
}

// data atom types.
const (
	dataUTF8 = 1
	dataJPEG = 13
	dataPNG  = 14
	dataInt  = 21
)

func item(name string, typ uint32, v []byte) *node {
	d := make([]byte, 8, 8+len(v))
	binary.BigEndian.PutUint32(d, typ)
	return box(name, nil, box("data", append(d, v...)))
}

// udta is the tags (iTunes' meta/ilst, as q2-applemusic writes them) and the chapters (Nero's
// chpl: version 1, a 100-nanosecond start and a title of up to 255 bytes each, 255 at most, as
// ffmpeg's mov muxer writes and its demuxer reads them).
func udta(t *Tags, chapters []Chapter) *node {
	var kids []*node
	if t != nil {
		var ilst []*node
		text := func(name, v string) {
			if v != "" {
				ilst = append(ilst, item(name, dataUTF8, []byte(v)))
			}
		}
		text("\xa9nam", t.Title)
		text("\xa9ART", t.Artist)
		text("\xa9alb", t.Album)
		text("\xa9gen", "Podcast")
		text("\xa9day", t.Date)
		ilst = append(ilst, item("stik", dataInt, []byte{21})) // podcast
		if len(t.Cover) > 0 {
			typ := uint32(dataJPEG)
			if len(t.Cover) > 4 && string(t.Cover[:4]) == "\x89PNG" {
				typ = dataPNG
			}
			ilst = append(ilst, item("covr", typ, t.Cover))
		}
		hdlr := full("hdlr", 0, 0, append(append(be32(0), []byte("mdirappl")...), make([]byte, 9)...))
		kids = append(kids, full("meta", 0, 0, nil, hdlr, box("ilst", nil, ilst...)))
	}
	if len(chapters) > 0 {
		chapters = chapters[:min(len(chapters), 255)]
		b := []byte{0, 0, 0, 0, byte(len(chapters))} // reserved, then the count
		for _, ch := range chapters {
			title := ch.Title
			if len(title) > 255 { // cut at a character's start
				cut := 255
				for cut > 0 && !utf8.RuneStart(title[cut]) {
					cut--
				}
				title = title[:cut]
			}
			b = binary.BigEndian.AppendUint64(b, uint64(ch.Start)*10000)
			b = append(b, byte(len(title)))
			b = append(b, title...)
		}
		kids = append(kids, full("chpl", 1, 0, b))
	}
	if len(kids) == 0 {
		return nil
	}
	return box("udta", nil, kids...)
}

package mp3

import (
	"encoding/binary"
	"io"
	"os"
	"sort"
)

// Range is frames [From, To) of a stream.
type Range struct{ From, To int }

// Write writes the stream read from src to dst: head (the new ID3v2 tag) first, then its frames but
// those in cuts. A frame whose audio data starts in frames that were cut (a Layer III frame's bit
// reservoir) is written silent, its side information zeroed, so a join never plays another frame's
// bytes as noise; the frames after it read their data from its bytes, kept as they were. A Xing or
// Info frame is kept with the new length, byte count, seek table and LAME CRCs; a VBRI one is
// dropped. dst is written from its start and synced; how many audio frames it holds is returned.
func (s *Stream) Write(src io.ReaderAt, dst *os.File, head []byte, cuts []Range) (int, error) {
	cut := normal(cuts, s.Info, len(s.Frames))
	w := &counter{f: dst}
	if _, err := w.Write(head); err != nil {
		return 0, err
	}
	var info []byte // the Xing/Info frame, patched once the rest is written
	infoAt := w.n
	if s.Info == 1 {
		f := s.Frames[0]
		b := make([]byte, f.Len)
		if _, err := src.ReadAt(b, f.Off); err != nil {
			return 0, err
		}
		if x, _ := parse(be32(b)); string(b[x.mainData():x.mainData()+4]) == "Xing" || string(b[x.mainData():x.mainData()+4]) == "Info" {
			info = b
			if _, err := w.Write(b); err != nil {
				return 0, err
			}
		}
	}
	audioAt := w.n
	var offs []int64 // each audio frame's start, from audioAt
	crc := uint16(0)
	avail := 0 // main data bytes in the frames written since the last cut
	next := 0  // the next cut
	buf := make([]byte, 0, 512<<10)
	for i := s.Info; i < len(s.Frames); {
		if next < len(cut) && i >= cut[next].From {
			i = max(i, cut[next].To)
			next++
			avail = 0
			continue
		}
		// a run of frames up to the next cut, read in one go
		end := len(s.Frames)
		if next < len(cut) {
			end = cut[next].From
		}
		j, size := i, 0
		for j < end && size+int(s.Frames[j].Len) <= cap(buf) && (j == i || s.Frames[j].Off == s.Frames[j-1].Off+int64(s.Frames[j-1].Len)) {
			size += int(s.Frames[j].Len)
			j++
		}
		buf = buf[:size]
		if _, err := src.ReadAt(buf, s.Frames[i].Off); err != nil {
			return 0, err
		}
		for p, k := 0, i; k < j; k++ {
			fr := buf[p : p+int(s.Frames[k].Len)]
			x, _ := parse(be32(fr))
			if x.layer == 3 {
				if int(s.Frames[k].MDB) > avail {
					silence(x, fr)
				}
				avail = min(avail+len(fr)-x.mainData(), 1<<12)
			}
			offs = append(offs, w.n-audioAt+int64(p))
			crc = crcLAME(crc, fr)
			p += len(fr)
		}
		if _, err := w.Write(buf); err != nil {
			return 0, err
		}
		i = j
	}
	if info != nil {
		patchInfo(info, len(offs), w.n-infoAt, offs, w.n-audioAt, crc)
		if _, err := dst.WriteAt(info, infoAt); err != nil {
			return 0, err
		}
	}
	if err := dst.Truncate(w.n); err != nil {
		return 0, err
	}
	return len(offs), dst.Sync()
}

// normal is cuts sorted, merged and clamped to the audio frames [lo, hi).
func normal(cuts []Range, lo, hi int) []Range {
	c := make([]Range, 0, len(cuts))
	for _, r := range cuts {
		r.From, r.To = max(r.From, lo), min(r.To, hi)
		if r.From < r.To {
			c = append(c, r)
		}
	}
	sort.Slice(c, func(i, j int) bool { return c[i].From < c[j].From })
	out := c[:0]
	for _, r := range c {
		if n := len(out); n > 0 && r.From <= out[n-1].To {
			out[n-1].To = max(out[n-1].To, r.To)
			continue
		}
		out = append(out, r)
	}
	return out
}

type counter struct {
	f *os.File
	n int64
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.f.WriteAt(p, c.n)
	c.n += int64(n)
	return n, err
}

// silence zeroes a Layer III frame's side information: main_data_begin 0, and every granule
// without data, so it decodes as silence and reads no reservoir.
func silence(x header, fr []byte) {
	p := 4
	if x.crc {
		p += 2
	}
	for k := p; k < p+x.sideLen(); k++ {
		fr[k] = 0
	}
	if x.crc {
		c := crc16(0xffff, fr[2:4])
		c = crc16(c, fr[p:p+x.sideLen()])
		binary.BigEndian.PutUint16(fr[4:6], c)
	}
}

// crc16 is CRC-16 (polynomial 0x8005, MSB first), as MPEG's frame CRC and LAME's tag use.
func crc16(c uint16, b []byte) uint16 {
	for _, v := range b {
		c = crcTable[byte(c>>8)^v] ^ c<<8
	}
	return c
}

var crcTable = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i) << 8
		for k := 0; k < 8; k++ {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x8005
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return
}()

// LAME's music CRC and its tag CRC are CRC-16/ARC: the reflected polynomial (0xA001), started at 0.
func crcLAME(c uint16, b []byte) uint16 {
	for _, v := range b {
		c = arcTable[byte(c)^v] ^ c>>8
	}
	return c
}

var arcTable = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i)
		for k := 0; k < 8; k++ {
			if c&1 != 0 {
				c = c>>1 ^ 0xa001
			} else {
				c >>= 1
			}
		}
		t[i] = c
	}
	return
}()

// patchInfo rewrites a Xing/Info frame for a stream of frames audio frames: bytes (the Info frame
// included), the seek table from offs (each frame's start in the audio, audioBytes in all), and a
// LAME tag's music length, music CRC (music, of the audio frames) and its own CRC, when that CRC
// held before: a tag whose CRC doesn't is someone else's layout, left alone.
func patchInfo(b []byte, frames int, bytes int64, offs []int64, audioBytes int64, music uint16) {
	x, _ := parse(be32(b))
	p := x.mainData() + 4
	if len(b) < p+4 {
		return
	}
	flags := be32(b[p:])
	q := p + 4 // the LAME tag's start, past the fields flags says are there
	for _, f := range []struct {
		bit uint32
		n   int
	}{{1, 4}, {2, 4}, {4, 100}, {8, 4}} {
		if flags&f.bit != 0 {
			q += f.n
		}
	}
	// LAME's tag: 36 bytes; at 28 the music length, at 32 the music CRC, at 34 the tag's CRC: LAME's
	// over every byte of the frame before it, FFmpeg's over the frame's first 190 bytes with the
	// CRC's own bytes taken as 0 (the same bytes in a stereo MPEG-1 frame).
	tagCRC := func(fixed bool) uint16 {
		if !fixed {
			return crcLAME(0, b[:q+34])
		}
		was := [2]byte{b[q+34], b[q+35]}
		b[q+34], b[q+35] = 0, 0
		c := crcLAME(0, b[:190])
		b[q+34], b[q+35] = was[0], was[1]
		return c
	}
	lame, fixed := false, false
	if q+36 <= len(b) && len(b) >= 190 {
		stored := binary.BigEndian.Uint16(b[q+34:])
		fixed = tagCRC(true) == stored
		lame = fixed || tagCRC(false) == stored
	}
	w := p + 4
	if flags&1 != 0 {
		binary.BigEndian.PutUint32(b[w:], uint32(frames))
		w += 4
	}
	if flags&2 != 0 {
		binary.BigEndian.PutUint32(b[w:], uint32(bytes))
		w += 4
	}
	if flags&4 != 0 && w+100 <= len(b) {
		for i := 0; i < 100; i++ {
			v := byte(0)
			if n := len(offs); n > 0 && audioBytes > 0 {
				v = byte(min(offs[min(i*n/100, n-1)]*256/audioBytes, 255))
			}
			b[w+i] = v
		}
	}
	if !lame {
		return
	}
	binary.BigEndian.PutUint32(b[q+28:], uint32(bytes))
	binary.BigEndian.PutUint16(b[q+32:], music)
	binary.BigEndian.PutUint16(b[q+34:], tagCRC(fixed))
}

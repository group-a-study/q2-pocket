// Package mp3 reads an MP3's frames and writes it again with parts left out. It never decodes the
// audio: what tells a podcast's own sound from an advert put in as it is served is in the frames
// themselves. A server that puts adverts in (dynamic ad insertion) stitches separately encoded files
// together at frame boundaries, so the episode's frames are the same bytes in every copy served,
// each stitched part starts with a fresh encoder (its bit reservoir empty, its padding pattern
// started again), and an advert is the same bytes wherever it is put (package adfree reads that).
//
// Layer III (and Layer II, for the rare MP2 feed) of MPEG-1, 2 and 2.5 are read. Free-format
// streams, and anything that isn't MPEG audio (an M4A), are ErrNotMP3.
package mp3

import (
	"bufio"
	"errors"
	"hash/fnv"
	"io"
	"os"
)

var ErrNotMP3 = errors.New("not an MP3 file")

// Frame is one frame of a stream as read. A four-hour episode has over half a million, all held
// while it is checked, so it is kept small: 24 bytes.
type Frame struct {
	Off  int64  // where it starts in the file
	Hash uint32 // of its bytes (FNV-1a's 64 bits folded to 32: all adfree compares)
	Head uint32 // its header
	Len  uint16 // its bytes, header included
	MDB  uint16 // main_data_begin: how far back into earlier frames its audio data starts
	Tag  bool   // it carries a Xing/Info tag: a stitched part's first frame, or the stream's
}

// fold is a 64-bit hash as the 32 bits kept (adfree.Fold).
func fold(h uint64) uint32 { return uint32(h ^ h>>32) }

// Pad is whether the frame has its padding slot.
func (f Frame) Pad() bool { return f.Head>>9&1 == 1 }

// Bitrate is the header's bitrate index.
func (f Frame) Bitrate() int { return int(f.Head >> 12 & 15) }

// Sig is the header bits a stitched part may differ in from the episode around it: the private
// bit, the channel mode, copyright, original and emphasis (not the mode extension, which an
// encoder changes frame by frame, nor the bitrate and padding).
func (f Frame) Sig() uint32 { return f.Head & 0x1cf }

// Stream is an MP3's frames.
type Stream struct {
	Frames     []Frame
	TagLen     int64 // the leading ID3v2 tags' bytes
	Version    int   // 1, 2, or 25 (MPEG-2.5)
	Layer      int   // 3 or 2
	SampleRate int
	Samples    int   // per frame
	Info       int   // frames[0] is a Xing/Info (or VBRI) frame: 1, else 0; its audio is silence
	Size       int64 // the file's bytes
}

// Seconds is how long frames [from, to) play.
func (s *Stream) Seconds(from, to int) float64 {
	return float64(to-from) * float64(s.Samples) / float64(s.SampleRate)
}

// Duration is the whole stream's length in seconds, the Info frame aside.
func (s *Stream) Duration() float64 { return s.Seconds(s.Info, len(s.Frames)) }

// FrameAt is the frame playing at sec seconds in (the Info frame aside), clamped to the stream.
func (s *Stream) FrameAt(sec float64) int {
	i := s.Info + int(sec*float64(s.SampleRate)/float64(s.Samples)+0.5)
	return min(max(i, s.Info), len(s.Frames))
}

// At is the time frame i starts at, the Info frame aside.
func (s *Stream) At(i int) float64 { return s.Seconds(s.Info, max(i, s.Info)) }

// Hashes is every frame's hash, the Info frame's too.
func (s *Stream) Hashes() []uint64 {
	out := make([]uint64, len(s.Frames))
	for i, f := range s.Frames {
		out[i] = uint64(f.Hash)
	}
	return out
}

var bitrates = [2][3][16]int{
	{ // MPEG-1: layers I, II, III
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	},
	{ // MPEG-2 and 2.5
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	},
}

var rates = map[int][3]int{1: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 25: {11025, 12000, 8000}}

// header is a frame header's fields; ok false for anything that isn't a Layer II or III header this
// package reads.
type header struct {
	version, layer, rate, samples, length int
	crc, mono                             bool
}

func parse(h uint32) (header, bool) {
	if h>>21 != 0x7ff {
		return header{}, false
	}
	var x header
	switch h >> 19 & 3 {
	case 0:
		x.version = 25
	case 2:
		x.version = 2
	case 3:
		x.version = 1
	default:
		return header{}, false
	}
	switch h >> 17 & 3 {
	case 1:
		x.layer = 3
	case 2:
		x.layer = 2
	default: // Layer I, or reserved
		return header{}, false
	}
	bi, ri := int(h>>12&15), int(h>>10&3)
	if bi == 0 || bi == 15 || ri == 3 || h&3 == 2 { // free format, bad, reserved rate or emphasis
		return header{}, false
	}
	v := 0
	if x.version != 1 {
		v = 1
	}
	kbps := bitrates[v][x.layer-1][bi]
	x.rate = rates[x.version][ri]
	pad := int(h >> 9 & 1)
	x.crc = h>>16&1 == 0
	x.mono = h>>6&3 == 3
	switch {
	case x.layer == 2:
		x.samples, x.length = 1152, 144000*kbps/x.rate+pad
	case x.version == 1:
		x.samples, x.length = 1152, 144000*kbps/x.rate+pad
	default:
		x.samples, x.length = 576, 72000*kbps/x.rate+pad
	}
	return x, x.length > 4
}

// sideLen is a Layer III frame's side information's bytes.
func (x header) sideLen() int {
	switch {
	case x.version == 1 && x.mono:
		return 17
	case x.version == 1:
		return 32
	case x.mono:
		return 9
	}
	return 17
}

// mainData is where a Layer III frame's main data area starts, from its header.
func (x header) mainData() int {
	n := 4 + x.sideLen()
	if x.crc {
		n += 2
	}
	return n
}

func (x header) same(y header) bool {
	return x.version == y.version && x.layer == y.layer && x.rate == y.rate
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// mdb is a Layer III frame's main_data_begin.
func (x header) mdb(frame []byte) uint16 {
	if x.layer != 3 {
		return 0
	}
	p := 4
	if x.crc {
		p += 2
	}
	if len(frame) < p+2 {
		return 0
	}
	if x.version == 1 {
		return uint16(frame[p])<<1 | uint16(frame[p+1]>>7)
	}
	return uint16(frame[p])
}

// Scan reads an MP3's frames from r: the leading ID3v2 tags passed over, then frames of one MPEG
// version, layer and sample rate; anything between them (a stitched part's own tag, junk) is passed
// over too, and so are frames of another sample rate. Two headers in a row must agree before the
// first is taken, so a tag's bytes that look like one aren't.
func Scan(r io.Reader) (*Stream, error) { return scan(r, 0, nil) }

// ScanHashes is only the frames' hashes, folded (Stream.Hashes): a second copy of an episode, which
// needs no more, in a third of the memory.
func ScanHashes(r io.Reader) ([]uint64, error) {
	var h []uint64
	_, err := scan(r, 0, func(f Frame) { h = append(h, uint64(f.Hash)) })
	return h, err
}

// scan reads the frames, into the stream's Frames, or to each, which then has them instead. size,
// when known, is the file's bytes, from which Frames is sized at once, so it isn't grown (copied)
// a few hundred thousand frames in.
func scan(r io.Reader, size int64, each func(Frame)) (*Stream, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	s := &Stream{}
	count := 0 // frames read
	var off int64
	for { // ID3v2 tags, one after another
		b, _ := br.Peek(10)
		if len(b) < 10 || string(b[:3]) != "ID3" {
			break
		}
		n := int64(b[6]&0x7f)<<21 | int64(b[7]&0x7f)<<14 | int64(b[8]&0x7f)<<7 | int64(b[9]&0x7f) + 10
		if b[3] == 4 && b[5]&0x10 != 0 { // a footer
			n += 10
		}
		k, err := br.Discard(int(n))
		off += int64(k)
		if err != nil {
			return nil, ErrNotMP3
		}
	}
	s.TagLen = off
	var lock header
	locked := false
	buf := make([]byte, 0, 4096)
	h := fnv.New64a()
	for {
		b, err := br.Peek(4)
		if len(b) < 4 {
			if err != nil && err != io.EOF && !errors.Is(err, bufio.ErrBufferFull) {
				return nil, err
			}
			break
		}
		x, ok := parse(be32(b))
		if ok && locked && !x.same(lock) {
			ok = false
		}
		if ok && !locked { // the next header must agree, unless the stream ends here
			if nb, _ := br.Peek(x.length + 4); len(nb) == x.length+4 {
				y, ok2 := parse(be32(nb[x.length:]))
				ok = ok2 && y.same(x)
			} else if len(nb) < x.length {
				ok = false
			}
		}
		if !ok {
			br.Discard(1)
			off++
			continue
		}
		buf = buf[:x.length]
		n, err := io.ReadFull(br, buf)
		if n < x.length { // cut short: the partial frame is left out
			off += int64(n)
			if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
				return nil, err
			}
			break
		}
		if !locked {
			locked, lock = true, x
			s.Version, s.Layer, s.SampleRate, s.Samples = x.version, x.layer, x.rate, x.samples
		}
		h.Reset()
		h.Write(buf)
		tag := infoFrame(x, buf)
		f := Frame{Off: off, Len: uint16(x.length), Hash: fold(h.Sum64()), MDB: x.mdb(buf), Head: be32(buf), Tag: tag}
		if each != nil {
			each(f)
		} else {
			if count == 0 && size > off {
				s.Frames = make([]Frame, 0, frameCount(x, buf, size-off))
			}
			s.Frames = append(s.Frames, f)
		}
		count++
		if count == 1 && tag {
			s.Info = 1
		}
		off += int64(x.length)
	}
	s.Size = off
	if count < 2 {
		return nil, ErrNotMP3
	}
	return s, nil
}

// frameCount is about how many frames left bytes from the first frame (b, its header x) hold: as many
// as the first one's length, an eighth more for a variable rate, or what a Xing/Info frame says if
// that is more; at most a million (seven hours at 44.1 kHz), past which Frames grows as it must.
func frameCount(x header, b []byte, left int64) int {
	n := left / int64(x.length)
	n += n/8 + 16
	if x.layer == 3 {
		if p := x.mainData(); len(b) >= p+12 && (string(b[p:p+4]) == "Xing" || string(b[p:p+4]) == "Info") && b[p+7]&1 != 0 {
			n = max(n, int64(be32(b[p+8:]))+16)
		}
	}
	return int(min(n, 1<<20))
}

// infoFrame is whether a stream's first frame is a Xing, Info or VBRI frame: silence carrying the
// stream's length, not audio.
func infoFrame(x header, b []byte) bool {
	if x.layer != 3 {
		return false
	}
	p := x.mainData()
	if len(b) >= p+4 && (string(b[p:p+4]) == "Xing" || string(b[p:p+4]) == "Info") {
		return true
	}
	return len(b) >= 40 && string(b[36:40]) == "VBRI"
}

// ScanFile is Scan of the file at path.
func ScanFile(path string) (*Stream, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	return scan(f, size, nil)
}

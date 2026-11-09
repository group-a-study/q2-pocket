package mp3

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The fixtures are separately encoded files (ffmpeg's LAME, 64 kb/s mono, 44.1 kHz), as a server
// that puts adverts in has them: content.mp3 an episode (12 s), ad1.mp3 and ad2.mp3 adverts.

func load(t *testing.T, name string) (*Stream, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Scan(bytes.NewReader(b))
	if err != nil {
		t.Fatal(name, err)
	}
	return s, b
}

// frames is frames [from, to) of a file's bytes, whole.
func frames(s *Stream, b []byte, from, to int) []byte {
	return b[s.Frames[from].Off : s.Frames[to-1].Off+int64(s.Frames[to-1].Len)]
}

// Stitched is how an advert server builds a copy: the episode's tag and Info frame, then its audio
// cut at frame boundaries with adverts' audio between.
func stitch(t *testing.T, at int) ([]byte, int, int) {
	c, cb := load(t, "content.mp3")
	a, ab := load(t, "ad1.mp3")
	var out []byte
	out = append(out, cb[:c.Frames[at].Off]...) // tag, Info frame and the audio before
	from := len(c.Frames[1:at]) + 1
	out = append(out, frames(a, ab, a.Info, len(a.Frames))...)
	to := from + len(a.Frames) - a.Info
	out = append(out, cb[c.Frames[at].Off:]...)
	return out, from, to
}

func TestScan(t *testing.T) {
	s, _ := load(t, "content.mp3")
	if s.Version != 1 || s.Layer != 3 || s.SampleRate != 44100 || s.Samples != 1152 || s.Info != 1 || s.TagLen == 0 {
		t.Fatalf("%+v", s)
	}
	if d := s.Duration(); math.Abs(d-12) > 0.1 {
		t.Errorf("duration %.2f", d)
	}
	if s.FrameAt(s.At(100)) != 100 {
		t.Errorf("FrameAt(At(100)) = %d", s.FrameAt(s.At(100)))
	}
	if _, err := Scan(bytes.NewReader(bytes.Repeat([]byte("not audio "), 1000))); err != ErrNotMP3 {
		t.Error(err)
	}
}

// Junk between frames (a stitched part's own ID3 tag) is passed over, and its frames are read.
func TestScanJunk(t *testing.T) {
	c, cb := load(t, "content.mp3")
	var in []byte
	in = append(in, cb[:c.Frames[50].Off]...)
	in = append(in, []byte("ID3\x04\x00\x00\x00\x00\x00\x05\xff\xfb\x00\x00\x00")...)
	in = append(in, cb[c.Frames[50].Off:]...)
	s, err := Scan(bytes.NewReader(in))
	if err != nil || len(s.Frames) != len(c.Frames) {
		t.Fatalf("%v: %d frames, want %d", err, len(s.Frames), len(c.Frames))
	}
	for i := range c.Frames {
		if s.Frames[i].Hash != c.Frames[i].Hash {
			t.Fatalf("frame %d differs", i)
		}
	}
}

func write(t *testing.T, in []byte, cuts []Range) (*Stream, []byte) {
	t.Helper()
	s, err := Scan(bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out.mp3")
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	head := in[:s.TagLen]
	n, err := s.Write(bytes.NewReader(in), f, head, cuts)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(dst)
	o, err := Scan(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(o.Frames) - o.Info; got != n {
		t.Errorf("Write says %d frames, the file has %d", n, got)
	}
	decodes(t, dst)
	return o, out
}

// decodes checks with ffmpeg, where there is one, that the file plays without an error.
func decodes(t *testing.T, path string) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return
	}
	out, err := exec.Command(ff, "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil || len(out) > 0 {
		t.Errorf("ffmpeg: %v %s", err, out)
	}
}

// infoFrames is a Xing/Info frame's frame count.
func infoFrames(t *testing.T, s *Stream, b []byte) uint32 {
	f := b[s.Frames[0].Off:]
	x, _ := parse(be32(f))
	p := x.mainData()
	if be32(f[p+4:])&1 == 0 {
		t.Fatal("no frame count")
	}
	return be32(f[p+8:])
}

// The advert cut out leaves the episode's own frames, in order; the Info frame counts them.
func TestWriteCut(t *testing.T) {
	c, _ := load(t, "content.mp3")
	in, from, to := stitch(t, 200)
	o, out := write(t, in, []Range{{from, to}})
	if len(o.Frames) != len(c.Frames) {
		t.Fatalf("%d frames, want %d", len(o.Frames), len(c.Frames))
	}
	silenced := 0
	for i := 1; i < len(c.Frames); i++ {
		if o.Frames[i].Hash != c.Frames[i].Hash {
			// the join's: frames whose audio data began before it, 187 bytes of it a frame here
			if i < 200 || i > 202 || o.Frames[i].MDB != 0 {
				t.Errorf("frame %d differs", i)
			}
			silenced++
		}
	}
	if c.Frames[200].MDB > 0 && silenced == 0 {
		t.Error("the frame after the cut kept a reservoir it no longer has")
	}
	if n := infoFrames(t, o, out); int(n) != len(o.Frames)-1 {
		t.Errorf("Info frame: %d frames, want %d", n, len(o.Frames)-1)
	}
}

// Cuts at the start, the end, overlapping and out of order; nothing cut is the file as it was.
func TestWriteRanges(t *testing.T) {
	c, cb := load(t, "content.mp3")
	o, _ := write(t, cb, nil)
	for i := range c.Frames[1:] {
		if o.Frames[i+1].Hash != c.Frames[i+1].Hash {
			t.Fatalf("an uncut copy's frame %d differs", i+1)
		}
	}
	o, _ = write(t, cb, []Range{{300, 400}, {0, 10}, {350, 420}, {len(c.Frames) - 5, len(c.Frames) + 9}})
	want := len(c.Frames) - 9 - 120 - 5 // frame 0 is the Info frame, never cut
	if len(o.Frames) != want {
		t.Errorf("%d frames, want %d", len(o.Frames), want)
	}
	if o.Frames[1].MDB != 0 {
		t.Error("the first frame reads a reservoir that isn't there")
	}
}

// A LAME tag's own CRC holds after its fields change.
func TestLAMETag(t *testing.T) {
	in, from, to := stitch(t, 120)
	o, out := write(t, in, []Range{{from, to}})
	f := out[o.Frames[0].Off:]
	x, _ := parse(be32(f))
	q := x.mainData() + 4 + 4 + 4 + 4 + 100 + 4
	if string(f[q:q+4]) != "Lavc" && string(f[q:q+4]) != "LAME" {
		t.Skipf("no LAME tag: %q", f[q:q+4])
	}
	tag := append([]byte(nil), f[:190]...)
	tag[q+34], tag[q+35] = 0, 0 // FFmpeg's tag CRC: the first 190 bytes, its own as 0
	if got := binary.BigEndian.Uint16(f[q+34:]); got != crcLAME(0, tag) {
		t.Errorf("tag CRC %04x, want %04x", got, crcLAME(0, tag))
	}
	var music uint16
	for _, fr := range o.Frames[1:] {
		music = crcLAME(music, out[fr.Off:fr.Off+int64(fr.Len)])
	}
	if got := binary.BigEndian.Uint16(f[q+32:]); got != music {
		t.Errorf("music CRC %04x, want %04x", got, music)
	}
	if got := binary.BigEndian.Uint32(f[q+28:]); int64(got) != int64(len(out))-o.TagLen {
		t.Errorf("music length %d, want %d", got, int64(len(out))-o.TagLen)
	}
}

// A frame with a CRC keeps a right one when it is made silent.
func TestSilenceCRC(t *testing.T) {
	fr := make([]byte, 208)
	binary.BigEndian.PutUint32(fr, 0xfff2_5000|0x00c4) // MPEG-1 L3 with CRC, 64 kb/s 44.1k, mono
	x, ok := parse(be32(fr))
	if !ok || !x.crc {
		t.Fatal(x, ok)
	}
	for i := 6; i < len(fr); i++ {
		fr[i] = byte(i)
	}
	silence(x, fr)
	want := crc16(crc16(0xffff, fr[2:4]), fr[6:6+x.sideLen()])
	if binary.BigEndian.Uint16(fr[4:]) != want || x.mdb(fr) != 0 {
		t.Errorf("crc %04x want %04x, mdb %d", binary.BigEndian.Uint16(fr[4:]), want, x.mdb(fr))
	}
}

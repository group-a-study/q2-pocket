package adfree

import (
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// TestCheckMemory measures what a check for ads holds at its peak beyond the episode's frames and a
// second copy: a three-hour episode, a full library of adverts, a dozen three-hour episodes to compare
// with. clean's memory budget (clean.need) is built on it; a change here that takes more than the
// budget's 10 bytes a frame, one and a half times the library's file and 6 bytes a frame of the
// longest peer fails it. Slow and heavy on the disk, so asked for: run it as the Q2 would, 32-bit:
//
//	Q2MEM=1 GOARCH=386 go test -run TestCheckMemory -v ./internal/adfree
func TestCheckMemory(t *testing.T) {
	if os.Getenv("Q2MEM") == "" {
		t.Skip("Q2MEM=1 to measure")
	}
	dir := t.TempDir()
	// a full library: maxFrames of adverts, ~30 s each
	rnd := rand.New(rand.NewSource(7))
	lib := &Library{Path: filepath.Join(dir, "ads.bin")}
	for n := 0; n < maxFrames-2000; {
		h := make([]uint32, 1150)
		for i := range h {
			h[i] = Fold(rnd.Uint32())
		}
		lib.ads = append(lib.ads, ad{int32(len(lib.all)), int32(len(h)), 1, int64(n)})
		lib.all = append(append(lib.all, h...), sentinel)
		n += len(h)
	}
	lib.Save()
	lib = nil
	s, _ := synth([]part{{60 * 60 * 3, sigShow, 0}}, 3)
	frames := len(s.Frames)
	cp := make([]uint32, frames)
	for i, f := range s.Frames {
		cp[i] = f.Hash
	}
	var peers []func() []uint32
	for k := 0; k < 12; k++ {
		p := filepath.Join(dir, "p"+string(rune('a'+k)))
		h := make([]uint32, frames)
		for i := range h {
			h[i] = rnd.Uint32()
		}
		SaveHashes(p, h)
		peers = append(peers, func() []uint32 { h, _ := LoadHashes(p); return h })
	}
	debug.SetGCPercent(5)
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base := m.HeapAlloc
	var peak atomic.Uint64
	done := make(chan bool)
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	start := time.Now()
	l := LoadLibrary(filepath.Join(dir, "ads.bin"))
	res := Detect(s, Evidence{Copy: cp, Peers: peers, Lib: l, Now: time.Now()})
	l.Learn(res.Learn, time.Now())
	close(done)
	took := peak.Load() - base
	fi, _ := os.Stat(filepath.Join(dir, "ads.bin"))
	budget := uint64(frames)*10 + uint64(fi.Size())*3/2 + uint64(frames)*6 + 2<<20
	t.Logf("%d frames (%d bytes each): peak beyond them and the copy %d KB (%.1f bytes a frame), budget %d KB, %v",
		frames, unsafe.Sizeof(s.Frames[0]), took>>10, float64(took)/float64(frames), budget>>10, time.Since(start))
	if took > budget {
		t.Errorf("the check took %d KB, over clean's budget of %d KB", took>>10, budget>>10)
	}
	runtime.KeepAlive(s)
	runtime.KeepAlive(cp)
}

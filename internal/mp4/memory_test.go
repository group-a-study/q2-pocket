//go:build live

// The memory writing a real episode takes, on a 32-bit build like the Q2's (MP4_FILE: a YouTube
// download, fmp4):
//
//	MP4_FILE=... GOARCH=386 go test -tags live -run TestMemory -v ./internal/mp4
package mp4

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMemory(t *testing.T) {
	p := os.Getenv("MP4_FILE")
	if p == "" {
		t.Skip("no MP4_FILE")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	st, err := Read(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	o, _ := os.Create(filepath.Join(t.TempDir(), "out.m4a"))
	defer o.Close()
	w, err := st.Write(o, f, st.Ranges([][2]float64{{44, 54}, {10404, 10414}}), &Tags{Title: "t", Cover: make([]byte, 200<<10)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	t.Logf("%d samples, %.0f s, written %d MB; allocated %d MB in all, %d MB held", st.Samples(), w.Length, w.Size>>20,
		(after.TotalAlloc-before.TotalAlloc)>>20, (after.HeapAlloc)>>20)
}

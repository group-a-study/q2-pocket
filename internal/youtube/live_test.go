//go:build live

// YouTube itself: a video's details, captions and whole audio, written as the Q2's M4A with a cut,
// and decoded by ffmpeg (YT_ID picks the video, YT_OUT keeps the files there):
//
//	go test -tags live -run TestLive -v ./internal/youtube
//
// Not part of the normal suite: it needs the network, and fetches about 170 MB.
package youtube

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"q2-pocket/internal/mp4"
)

func TestLive(t *testing.T) {
	id := os.Getenv("YT_ID")
	if id == "" {
		id = "2hJkprFnzhY" // Lex Fridman Podcast #504
	}
	dir := os.Getenv("YT_OUT")
	if dir == "" {
		dir = t.TempDir()
	}
	ctx := context.Background()
	c := &Client{HTTP: &http.Client{Timeout: 5 * time.Minute}, Agent: "Q2Pocket/test (Shanling Q2; Q2 Pod card app)"}
	v, err := c.Video(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := v.Pick()
	t.Logf("%q by %s, %.0f s; %d formats; picked itag %d (%s, %d MB, index %d MB); %d caption tracks",
		v.Title, v.Channel, v.Length, len(v.Audio), f.Itag, f.Codec, f.Size>>20, Index(v.Length, f)>>20, len(v.Captions))
	if !ok {
		t.Fatal("no AAC format")
	}
	if tr, ok := v.Caption(); ok {
		b, err := c.Captions(ctx, tr)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, id+".json3"), b, 0o644)
		t.Logf("captions %s (auto %v): %d KB", tr.Lang, tr.Auto, len(b)>>10)
	}
	part := filepath.Join(dir, id+".part")
	out, err := os.Create(part)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	for off := int64(0); off < f.Size; {
		resp, err := Get(ctx, c.HTTP, f, off, min(off+ChunkSize, f.Size)-1)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("HTTP %d at %d", resp.StatusCode, off)
		}
		n, err := io.Copy(out, resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		off += n
	}
	out.Close()
	t.Logf("downloaded %d MB in %v", f.Size>>20, time.Since(began).Round(time.Second))
	in, _ := os.Open(part)
	defer in.Close()
	st, err := mp4.Read(in, f.Size)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d samples, %.1f s", st.Samples(), st.Duration())
	dst := filepath.Join(dir, id+".m4a")
	o, _ := os.Create(dst)
	w, err := st.Write(o, in, st.Ranges([][2]float64{{43.96, 53.94}}), &mp4.Tags{Title: v.Title, Artist: v.Channel, Album: v.Channel},
		[]mp4.Chapter{{0, "Introduction"}, {60000, "Part two"}})
	o.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("written %d MB, %.1f s", w.Size>>20, w.Length)
	res, _ := exec.Command("ffmpeg", "-v", "error", "-i", dst, "-map", "0:a", "-f", "null", "-").CombinedOutput()
	if s := strings.TrimSpace(string(res)); s != "" {
		t.Fatalf("ffmpeg: %s", s[:min(len(s), 2000)])
	}
}

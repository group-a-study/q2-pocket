//go:build live

package sponsor

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"q2-pocket/internal/pocketcasts"
)

func TestProbeReads(t *testing.T) {
	c := pocketcasts.New(&http.Client{Timeout: 30 * time.Second}, "Q2Pocket/0.1 (test)", pocketcasts.Token{})
	ctx := context.Background()
	for _, p := range []struct{ pod, match string }{
		{"52375d40-eec9-0135-c25e-7d73a919276a", "intelligence overrated"},
		{"4eb5b260-c933-0134-10da-25324e2a541d", "keisha"},
		{"a9a362c0-9404-013f-5cfc-02ffffe690b5", "purple tsunami"},
		{os.Getenv("POD"), os.Getenv("MATCH")},
	} {
		if p.pod == "" {
			continue
		}
		s, _, _, err := c.Full(ctx, p.pod, "")
		if err != nil {
			t.Fatal(err)
		}
		notes, _ := c.ShowNotes(ctx, p.pod)
		for _, e := range s.Episodes {
			if p.match != "" && !strings.Contains(strings.ToLower(e.Title), p.match) {
				continue
			}
			n := notes[e.UUID]
			tr := append(append([]pocketcasts.Transcript{}, n.Transcripts...), n.Generated...)
			if len(tr) == 0 {
				continue
			}
			b, _ := c.Get(ctx, tr[0].URL, 8<<20)
			cues := Transcript(b, tr[0].Type)
			t.Logf("== %s: %s (%d s, transcript %s %d cues, ends %.0f)", s.Title, e.Title, e.Duration, tr[0].Type, len(cues), End(cues))
			for _, r := range Reads(cues) {
				var words []string
				for _, cu := range cues {
					if cu.From >= r.From && cu.To <= r.To {
						words = append(words, cu.Text)
					}
				}
				all := strings.Join(words, " ")
				t.Logf("   %.0f-%.0f (%.0f s): %.110s ... %.70s", r.From, r.To, r.To-r.From, all, all[max(0, len(all)-70):])
			}
			_ = regexp.MustCompile
			break
		}
	}
}

func TestProbeFile(t *testing.T) {
	for _, f := range strings.Fields(os.Getenv("FILES")) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		cues := Transcript(b, "")
		t.Logf("== %s: %d cues, ends %.0f", f, len(cues), End(cues))
		for _, r := range Reads(cues) {
			var words []string
			for _, cu := range cues {
				if cu.From >= r.From && cu.To <= r.To {
					words = append(words, cu.Text)
				}
			}
			all := strings.Join(words, " ")
			t.Logf("   %.0f-%.0f (%.0f s): %.120s ... %.80s", r.From, r.To, r.To-r.From, all, all[max(0, len(all)-80):])
		}
	}
}

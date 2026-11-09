//go:build live

package pocketcasts

// The public parts of Pocket Casts, live: go test -tags live -run TestLive -v ./internal/pocketcasts

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestLive(t *testing.T) {
	c := New(&http.Client{Timeout: 30 * time.Second}, "Q2Pocket/0.1 (test)", Token{})
	ctx := context.Background()
	pods, err := c.List(ctx, "popular")
	if err != nil || len(pods) == 0 {
		t.Fatal(err)
	}
	t.Logf("popular: %d, first %+v", len(pods), pods[0])
	s, tag, _, err := c.Full(ctx, "4eb5b260-c933-0134-10da-25324e2a541d", "") // The Daily
	if err != nil || len(s.Episodes) == 0 {
		t.Fatal(err)
	}
	t.Logf("The Daily: %d episodes, tag %s, first %+v", len(s.Episodes), tag, s.Episodes[0])
	if _, _, same, err := c.Full(ctx, s.UUID, tag); err != nil || !same {
		t.Errorf("asked again: same %v %v", same, err)
	}
	notes, err := c.ShowNotes(ctx, s.UUID)
	if err != nil {
		t.Fatal(err)
	}
	n := notes[s.Episodes[0].UUID]
	t.Logf("notes: %d; first: %d chars, transcripts %v, generated %v, chapters %q", len(notes), len(n.Text), n.Transcripts, n.Generated, n.ChaptersURL)
	if len(n.Generated) > 0 {
		b, err := c.Get(ctx, n.Generated[0].URL, 8<<20)
		t.Logf("generated transcript: %d bytes %v; starts %q", len(b), err, b[:min(len(b), 60)])
	}
}

//go:build account

package pocketcasts

// Signs in with PC_EMAIL / PC_PASSWORD once and keeps only the session at PC_TOKEN (mode 0600).

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAccountLogin(t *testing.T) {
	c := New(&http.Client{Timeout: 30 * time.Second}, "Q2Pocket/0.1 (Shanling Q2; Q2 Pod card app)", Token{})
	tok, err := c.Login(context.Background(), os.Getenv("PC_EMAIL"), os.Getenv("PC_PASSWORD"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(tok)
	if err := os.WriteFile(os.Getenv("PC_TOKEN"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("signed in: refresh token %v, expires %v, user id set %v", tok.Refresh != "", tok.Expires.Format(time.RFC3339), tok.User != "")
}

// Read-only calls with the session at PC_TOKEN: what each returns, as counts and the fields set.
func TestAccountRead(t *testing.T) {
	b, err := os.ReadFile(os.Getenv("PC_TOKEN"))
	if err != nil {
		t.Skip(err)
	}
	var tok Token
	json.Unmarshal(b, &tok)
	c := New(&http.Client{Timeout: 30 * time.Second}, "Q2Pocket/0.1 (Shanling Q2; Q2 Pod card app)", tok)
	saved := false
	c.Saved = func(nt Token) {
		saved = true
		b, _ := json.Marshal(nt)
		os.WriteFile(os.Getenv("PC_TOKEN"), b, 0o600)
	}
	ctx := context.Background()
	fields := func(e Episode) string {
		var f []string
		for k, v := range map[string]bool{"uuid": e.UUID != "", "podcast": e.Show() != "", "title": e.Title != "", "url": e.URL != "",
			"published": e.Published != "", "duration": e.Duration > 0, "playedUpTo": e.PlayedUpTo > 0, "status": e.Status > 0,
			"podcastTitle": e.PodcastTitle != ""} {
			if v {
				f = append(f, k)
			}
		}
		return strings.Join(f, ",")
	}
	pods, err := c.Podcasts(ctx)
	t.Logf("podcasts: %d %v; first has url %v feed %v", len(pods), err, len(pods) > 0 && pods[0].URL != "", len(pods) > 0 && pods[0].Feed != "")
	for name, f := range map[string]func(context.Context) ([]Episode, error){"up next": c.UpNext, "in progress": c.InProgress,
		"new releases": c.NewReleases, "starred": c.Starred, "history": c.History} {
		eps, err := f(ctx)
		first := ""
		if len(eps) > 0 {
			first = fields(eps[0])
		}
		t.Logf("%s: %d %v; first has %s", name, len(eps), err, first)
	}
	if len(pods) > 0 {
		st, err := c.States(ctx, pods[0].UUID)
		t.Logf("states of the first podcast: %d %v", len(st), err)
	}
	files, err := c.Files(ctx)
	t.Logf("files: %d %v", len(files), err)
	// a stale session is refreshed and kept
	c.mu.Lock()
	c.tok.Access = "stale"
	c.mu.Unlock()
	_, err = c.Podcasts(ctx)
	t.Logf("after a refused session: %v, refreshed and saved %v", err, saved)
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/q2"
)

// settings is .pocket/config.json. Missing choices take their defaults: Up Next's first upNextFirst
// episodes downloaded, played episodes kept, the sync one-way. Every ad removal (adverts, sponsor
// reads, self-promotion, copies compared) is always on (the user's, 2026-11-15: on by default;
// 2026-11-16: they had come unticked, so always on, ticked, and no switch turns them off).
type settings struct {
	Key         string `json:"web_key"`      // the QR code's: signs a browser in
	PIN         string `json:"pin"`          // the Q2's screen's, typed with the address
	IdleMinutes int    `json:"idle_minutes"` // stop by itself after this long unused (0: never)

	Ads          bool `json:"remove_ads"`
	Sponsors     bool `json:"remove_sponsor_reads"`
	SelfPromo    bool `json:"remove_self_promotion"`
	Copy         bool `json:"compare_copies"`
	UpNext       int  `json:"download_up_next"`    // Up Next kept downloaded: -1 all, else its first this many
	NextCount    int  `json:"download_next_count"` // Download Next's: the next this many of Up Next not on the Q2
	DeletePlayed bool `json:"delete_played"`
	TwoWay       bool `json:"sync_two_way"`     // the sync sends the Q2's places, played episodes and what plays too
	Version      int  `json:"settings_version"` // settingsVersion once loadSettings has brought it up to date
	// Rechecked is the last recheckRound done: the episodes on the Q2 with their ads asked for again
	Rechecked int `json:"rechecked,omitempty"`
	// YouTubeFallback: an episode's YouTube video that nothing can check for sponsor reads (no
	// captions, nothing in SponsorBlock) gives way to the feed's MP3. Off by default (the user's,
	// 2026-11-17): the video comes as it is, unmarked, for a Remove Ads Again once SponsorBlock has
	// its segments (they submit them themselves).
	YouTubeFallback bool `json:"youtube_fallback,omitempty"`

	YouTubeKey    string `json:"youtube_api_key,omitempty"`
	SpotifyID     string `json:"spotify_client_id,omitempty"`
	SpotifySecret string `json:"spotify_client_secret,omitempty"`
}

// recheckRound is the round of Remove Ads Again a start asks for once, for every episode on the Q2
// marked Ads Not Removed: 1 (v1.12, the user's, 2026-11-16) for those that went on uncut after
// crashing v1.10, which lacked the memory to check them; v1.12 has it.
const recheckRound = 1

// upNextFirst is how many of Up Next's episodes are kept downloaded unless chosen otherwise; Download
// All on the Up Next page fetches the rest once.
const upNextFirst = 10

// nextFirst is Download Next's count unless chosen otherwise (nextChoices).
const nextFirst = 5

// nextChoices are the counts Download Next's Play/Pause hold and setting go through.
var nextChoices = []int{1, 3, 5, 10, 20}

// nextCount is s's Download Next count, one of nextChoices.
func (s settings) nextCount() int {
	for _, n := range nextChoices {
		if n == s.NextCount {
			return n
		}
	}
	return nextFirst
}

// settingsVersion 2: Up Next's default became its first upNextFirst, not all of it, so a config
// saved before (which holds the old default, -1) takes the new one once. 3: every ad removal on
// (the user's, 2026-11-15), self-promotion's too, once.
const settingsVersion = 3

func defaults() settings {
	return settings{IdleMinutes: 15, Ads: true, Sponsors: true, SelfPromo: true, Copy: true, UpNext: upNextFirst,
		NextCount: nextFirst}
}

// loadSettings reads config.json, making the key and PIN the first time; they stay, so a phone
// signed in once stays signed in.
func loadSettings(dir string) settings {
	s := defaults()
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		json.Unmarshal(b, &s)
	}
	dirty := false
	if len(s.Key) < 12 {
		s.Key, dirty = q2.Key(12), true
	}
	if len(s.PIN) != 6 {
		s.PIN, dirty = q2.PIN(6), true
	}
	if s.IdleMinutes < 0 {
		s.IdleMinutes = 0
	}
	if s.Version < settingsVersion {
		if s.Version < 2 && s.UpNext < 0 {
			s.UpNext = upNextFirst
		}
		s.Version, dirty = settingsVersion, true
	}
	if !s.Ads || !s.Sponsors || !s.SelfPromo || !s.Copy {
		s.Ads, s.Sponsors, s.SelfPromo, s.Copy, dirty = true, true, true, true, true
	}
	if dirty {
		saveSettings(dir, s)
	}
	return s
}

func saveSettings(dir string, s settings) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return q2.WriteFile(filepath.Join(dir, "config.json"), b)
}

func (a *app) settings() settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.set
}

// change applies fn to the settings and saves them.
func (a *app) change(fn func(*settings)) error {
	a.mu.Lock()
	fn(&a.set)
	s := a.set
	a.mu.Unlock()
	return saveSettings(a.opts.dir, s)
}

// cleanSettings is the settings as the cleaner takes them: every removal on, always.
func (a *app) cleanSettings() clean.Settings {
	s := a.settings()
	return clean.Settings{Ads: true, Sponsors: true, SelfPromo: true, Copy: true, YouTubeFallback: s.YouTubeFallback,
		YouTubeKey: s.YouTubeKey, SpotifyID: s.SpotifyID, SpotifySecret: s.SpotifySecret}
}

// The Pocket Casts session is .pocket/account.json: tokens, never the password.

func accountPath(dir string) string { return filepath.Join(dir, "account.json") }

func loadAccount(dir string) pocketcasts.Token {
	var t pocketcasts.Token
	if b, err := os.ReadFile(accountPath(dir)); err == nil {
		json.Unmarshal(b, &t)
	}
	return t
}

func saveAccount(dir string, t pocketcasts.Token) {
	if t.Access == "" {
		os.Remove(accountPath(dir))
		return
	}
	b, _ := json.Marshal(t)
	q2.WriteFile(accountPath(dir), b)
}

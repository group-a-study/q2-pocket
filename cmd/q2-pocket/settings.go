package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"q2-pocket/internal/clean"
	"q2-pocket/internal/pocketcasts"
	"q2-pocket/internal/q2"
)

// settings is .pocket/config.json. Missing choices take their defaults: adverts and sponsor reads
// cut, copies compared, Up Next's first upNextFirst episodes downloaded, played episodes kept.
type settings struct {
	Key         string `json:"web_key"`      // the QR code's: signs a browser in
	PIN         string `json:"pin"`          // the Q2's screen's, typed with the address
	IdleMinutes int    `json:"idle_minutes"` // stop by itself after this long unused (0: never)

	Ads          bool `json:"remove_ads"`
	Sponsors     bool `json:"remove_sponsor_reads"`
	SelfPromo    bool `json:"remove_self_promotion"`
	Copy         bool `json:"compare_copies"`
	UpNext       int  `json:"download_up_next"` // Up Next kept downloaded: -1 all, else its first this many
	DeletePlayed bool `json:"delete_played"`
	Version      int  `json:"settings_version"` // settingsVersion once loadSettings has brought it up to date

	YouTubeKey    string `json:"youtube_api_key,omitempty"`
	SpotifyID     string `json:"spotify_client_id,omitempty"`
	SpotifySecret string `json:"spotify_client_secret,omitempty"`
}

// upNextFirst is how many of Up Next's episodes are kept downloaded unless chosen otherwise; Download
// All on the Up Next page fetches the rest once.
const upNextFirst = 10

// settingsVersion 2: Up Next's default became its first upNextFirst, not all of it, so a config
// saved before (which holds the old default, -1) takes the new one once.
const settingsVersion = 2

func defaults() settings {
	return settings{IdleMinutes: 15, Ads: true, Sponsors: true, Copy: true, UpNext: upNextFirst}
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
		if s.UpNext < 0 {
			s.UpNext = upNextFirst
		}
		s.Version, dirty = settingsVersion, true
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

// cleanSettings is the settings as the cleaner takes them.
func (a *app) cleanSettings() clean.Settings {
	s := a.settings()
	return clean.Settings{Ads: s.Ads, Sponsors: s.Sponsors, SelfPromo: s.SelfPromo, Copy: s.Copy,
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

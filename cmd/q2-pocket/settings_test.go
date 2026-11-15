package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Up Next keeps its first upNextFirst episodes downloaded unless chosen otherwise; a config saved
// before that, which holds the old default (all of it), takes the new one once, and a later choice
// of all of it stays.
func TestUpNextDefault(t *testing.T) {
	dir := t.TempDir()
	if s := loadSettings(dir); s.UpNext != upNextFirst || s.Version != settingsVersion {
		t.Fatalf("new config: %+v", s)
	}
	old := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(old, []byte(`{"web_key":"ABCDEFGHJKMN","pin":"123456","download_up_next":-1,"remove_ads":true}`), 0o644)
	s := loadSettings(filepath.Dir(old))
	if s.UpNext != upNextFirst || s.Version != settingsVersion || !s.Ads {
		t.Fatalf("old config: %+v", s)
	}
	var saved settings
	b, _ := os.ReadFile(old)
	json.Unmarshal(b, &saved)
	if saved.UpNext != upNextFirst || saved.Version != settingsVersion {
		t.Fatalf("not saved: %s", b)
	}
	s.UpNext = -1
	saveSettings(filepath.Dir(old), s)
	if s := loadSettings(filepath.Dir(old)); s.UpNext != -1 {
		t.Fatalf("all of it, chosen: %+v", s)
	}
}

// Every ad removal is on by default (the user's, 2026-11-15), self-promotion's too, and a config saved
// before version 3 turns them all on once; one turned off since stays off. The sync is one-way unless
// chosen.
func TestAdRemovalOnByDefault(t *testing.T) {
	if s := loadSettings(t.TempDir()); !s.Ads || !s.Sponsors || !s.SelfPromo || !s.Copy || s.TwoWay {
		t.Fatalf("new config: %+v", s)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"web_key":"ABCDEFGHJKMN","pin":"123456","settings_version":2,`+
		`"remove_ads":false,"remove_sponsor_reads":false,"remove_self_promotion":false,"compare_copies":false,"download_up_next":-1}`), 0o644)
	s := loadSettings(dir)
	if !s.Ads || !s.Sponsors || !s.SelfPromo || !s.Copy || s.UpNext != -1 || s.Version != settingsVersion {
		t.Fatalf("version 2 config: %+v", s)
	}
	s.SelfPromo = false
	saveSettings(dir, s)
	if s := loadSettings(dir); s.SelfPromo {
		t.Fatal("turned off since, but on again")
	}
}

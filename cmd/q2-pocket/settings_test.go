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

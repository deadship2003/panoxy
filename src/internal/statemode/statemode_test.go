package statemode

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadMigratesLegacyStateFile: a pre-rename Panoxy.yaml (capital P) left in
// the state dir is adopted as the canonical panoxy.yaml on first read, keeping
// its stored value.
func TestReadMigratesLegacyStateFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, legacyStateName)
	canonical := filepath.Join(dir, "panoxy.yaml")
	if err := os.WriteFile(legacy, []byte("proxy-mode: tproxy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Read(canonical); got != "tproxy" {
		t.Fatalf("proxy-mode = %q, want tproxy (adopted from the legacy file)", got)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("canonical state file not created: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy state file still present after migration")
	}
}

// TestReadKeepsCanonicalOverLegacy: when both files exist the canonical one
// wins and the legacy file is left untouched (no destructive overwrite).
func TestReadKeepsCanonicalOverLegacy(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, legacyStateName)
	canonical := filepath.Join(dir, "panoxy.yaml")
	if err := os.WriteFile(legacy, []byte("proxy-mode: tproxy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("proxy-mode: tun\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Read(canonical); got != "tun" {
		t.Fatalf("proxy-mode = %q, want tun (canonical wins)", got)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy file must stay untouched when canonical exists: %v", err)
	}
}

// TestReadDefaultOnMissing: no state file at all -> default tun, no error path.
func TestReadDefaultOnMissing(t *testing.T) {
	if got := Read(filepath.Join(t.TempDir(), "panoxy.yaml")); got != "tun" {
		t.Fatalf("proxy-mode = %q, want the tun default", got)
	}
}

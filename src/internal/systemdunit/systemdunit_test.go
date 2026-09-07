package systemdunit

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadship2003/panoxy/internal/paths"
)

func TestInstalled(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{UnitDir: dir}
	if Installed(p) {
		t.Fatal("no unit written yet, Installed should be false")
	}
	if err := os.WriteFile(filepath.Join(dir, unitMain), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Installed(p) {
		t.Fatal("main unit written, Installed should be true")
	}
}

func TestParseShow(t *testing.T) {
	out := "LoadState=loaded\nActiveState=active\nMainPID=1234\nActiveEnterTimestamp=Sun 2026-09-07 17:44:00 CST\nExecMainStatus=0\nResult=success\nNRestarts=2\n\n"
	m := ParseShow(out)
	if m["ActiveState"] != "active" || m["MainPID"] != "1234" || m["NRestarts"] != "2" {
		t.Fatalf("unexpected parse result: %v", m)
	}
	// values containing '=' stay intact; empty input yields an empty map
	if len(ParseShow("")) != 0 {
		t.Fatal("empty input should parse to an empty map")
	}
}

func TestParseUptime(t *testing.T) {
	// A timestamp one hour ago parses to roughly 3600s (weekday prefix and zone suffix ignored).
	ts := time.Now().Add(-time.Hour).Format("Mon 2006-01-02 15:04:05 MST")
	got := ParseUptime(ts)
	if got < 3590 || got > 3610 {
		t.Fatalf("expected ~3600s, got %d", got)
	}
	for _, bad := range []string{"", "not a timestamp", "Sun 2026-13-99 99:99:99 CST"} {
		if ParseUptime(bad) != 0 {
			t.Fatalf("unparseable input %q should yield 0", bad)
		}
	}
}

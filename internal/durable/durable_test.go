package durable

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteKeepsThePreviousGoodCopy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "state.json")
	if err := WriteJSON(p, map[string]int{"v": 1}, " "); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(BackupPath(p)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("first write must not create a backup")
	}
	if err := WriteJSON(p, map[string]int{"v": 2}, " "); err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(p)
	bak, _ := os.ReadFile(BackupPath(p))
	if !strings.Contains(string(cur), "2") || !strings.Contains(string(bak), "1") {
		t.Fatalf("cur %s bak %s", cur, bak)
	}
	if m, _ := filepath.Glob(p + ".tmp*"); len(m) != 0 {
		t.Fatalf("tmp file left behind: %v", m)
	}
}

func TestReadFallsBackToTheBackupWhenTheMainFileIsEmptyOrBroken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	_ = WriteJSON(p, map[string]int{"v": 1}, " ")
	_ = WriteJSON(p, map[string]int{"v": 2}, " ")
	for _, bad := range []string{"", "{\"v\": 2", "\x00\x00\x00"} {
		if err := os.WriteFile(p, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		b, fromBak, err := ReadJSON(p)
		if err != nil || !fromBak || !strings.Contains(string(b), "1") {
			t.Fatalf("bad %q: %s %v %v", bad, b, fromBak, err)
		}
	}
	_ = os.Remove(p) // a reset between the two renames leaves only the backup
	if b, fromBak, err := ReadJSON(p); err != nil || !fromBak || !strings.Contains(string(b), "1") {
		t.Fatalf("missing main: %s %v %v", b, fromBak, err)
	}
}

func TestBrokenFileIsMovedAsideNotUsedAsBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	_ = WriteJSON(p, map[string]int{"v": 1}, " ")
	_ = WriteJSON(p, map[string]int{"v": 2}, " ")
	_ = os.WriteFile(p, nil, 0o644) // a zero-byte file after a reset
	if err := WriteJSON(p, map[string]int{"v": 3}, " "); err != nil {
		t.Fatal(err)
	}
	bak, _ := os.ReadFile(BackupPath(p))
	if !strings.Contains(string(bak), "1") {
		t.Fatalf("the good backup was replaced: %s", bak)
	}
	m, _ := filepath.Glob(p + ".corrupt-*")
	if len(m) != 1 {
		t.Fatalf("want one .corrupt file, got %v", m)
	}
}

func TestReadReportsMissingAndUnusable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if _, _, err := ReadJSON(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
	_ = os.WriteFile(p, []byte("not json"), 0o644)
	if _, _, err := ReadJSON(p); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want an unusable-file error, got %v", err)
	}
}

func TestWriteFileNonJSONPreservesBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "raw.bin")
	first := []byte("not-json-first-payload\x00\x01")
	second := []byte("not-json-second-payload\x02\x03")
	if err := WriteFile(p, first, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, second, 0o644); err != nil {
		t.Fatal(err)
	}
	cur, err := os.ReadFile(p)
	if err != nil || string(cur) != string(second) {
		t.Fatalf("cur=%q err=%v", cur, err)
	}
	bak, err := os.ReadFile(BackupPath(p))
	if err != nil || string(bak) != string(first) {
		t.Fatalf("expected non-JSON backup preserved, bak=%q err=%v", bak, err)
	}
	if m, _ := filepath.Glob(p + ".corrupt-*"); len(m) != 0 {
		t.Fatalf("non-JSON WriteFile must not treat valid raw file as corrupt: %v", m)
	}
	if m, _ := filepath.Glob(p + ".tmp*"); len(m) != 0 {
		t.Fatalf("tmp file left behind: %v", m)
	}
}

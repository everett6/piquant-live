package collect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupFetchDueAndPrune(t *testing.T) {
	dest := t.TempDir()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "paper"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "paper", "dryrun_state.json"), []byte(`{"equity":200}`), 0o644)
	var logs []string
	b := &backupRunner{
		cfg: BackupConfig{Dest: dest, Keep: 2, Hour: 3, command: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "tar", "czf", "-", "-C", src, "paper")
		}},
		logf: func(f string, a ...any) { logs = append(logs, f) },
	}
	for _, d := range []string{"2026-11-01", "2026-11-02", "2026-11-03"} {
		_ = os.WriteFile(filepath.Join(dest, d+".tar.gz"), []byte{0x1f, 0x8b}, 0o644)
	}
	early := time.Date(2026, 11, 11, 2, 0, 0, 0, time.Local)
	if b.due(early) {
		t.Fatal("due before the backup hour")
	}
	now := time.Date(2026, 11, 11, 4, 0, 0, 0, time.Local)
	b.tick(context.Background(), now)
	got, _ := filepath.Glob(filepath.Join(dest, "*.tar.gz"))
	if len(got) != 2 || !strings.HasSuffix(got[1], "2026-11-11.tar.gz") {
		t.Fatalf("archives = %v, logs %v", got, logs)
	}
	out, err := exec.Command("tar", "tzf", got[1]).Output()
	if err != nil || !strings.Contains(string(out), "paper/dryrun_state.json") {
		t.Fatalf("archive contents %q, %v", out, err)
	}
	if b.due(now.Add(time.Hour)) {
		t.Fatal("due again after today's archive exists")
	}
}

func TestBackupFailureLeavesNoArchiveAndWaits(t *testing.T) {
	dest := t.TempDir()
	var logs []string
	b := &backupRunner{
		cfg: BackupConfig{Dest: dest, Keep: 30, command: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "echo boom >&2; exit 3")
		}},
		logf: func(f string, a ...any) { logs = append(logs, f) },
	}
	now := time.Date(2026, 11, 11, 4, 0, 0, 0, time.Local)
	b.tick(context.Background(), now)
	if m, _ := filepath.Glob(filepath.Join(dest, "*")); len(m) != 0 {
		t.Fatalf("left files %v", m)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "failed") {
		t.Fatalf("logs %v", logs)
	}
	if b.due(now.Add(10*time.Minute)) || !b.due(now.Add(31*time.Minute)) {
		t.Fatal("retry spacing wrong")
	}
	// Non-gzip output is rejected too.
	b.cfg.command = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "echo", "hi") }
	if _, err := b.fetch(context.Background(), now); err == nil {
		t.Fatal("accepted a non-gzip archive")
	}
}

func TestPushLedgersRefusesPublicRepo(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:x/piquant-live.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	b := &backupRunner{cfg: BackupConfig{GitDir: dir}}
	err := b.pushLedgers(context.Background(), "none.tar.gz", time.Now())
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("err = %v", err)
	}
}

func TestMirrorOncePerDayRetriesAndAlerts(t *testing.T) {
	dest := t.TempDir()
	runs, alerts := 0, 0
	fail := true
	b := &backupRunner{
		cfg: BackupConfig{Hour: 3, MirrorRemote: "piquant-collect", MirrorDest: filepath.Join(dest, "m"),
			Alert: func(key, _, _ string) {
				if key == "mirror" {
					alerts++
				}
			},
			mirrorCommand: func(ctx context.Context) *exec.Cmd {
				runs++
				if fail {
					return exec.CommandContext(ctx, "sh", "-c", "echo down >&2; exit 12")
				}
				return exec.CommandContext(ctx, "true")
			}},
		logf: func(string, ...any) {},
	}
	now := time.Date(2026, 11, 13, 2, 0, 0, 0, time.Local)
	b.mirrorTick(context.Background(), now) // before the hour
	if runs != 0 {
		t.Fatal("ran before the backup hour")
	}
	now = now.Add(2 * time.Hour)
	b.mirrorTick(context.Background(), now)
	b.mirrorTick(context.Background(), now.Add(10*time.Minute)) // too soon to retry
	if runs != 1 || alerts != 1 {
		t.Fatalf("runs %d alerts %d after a failure", runs, alerts)
	}
	fail = false
	b.mirrorTick(context.Background(), now.Add(31*time.Minute))
	b.mirrorTick(context.Background(), now.Add(3*time.Hour)) // done for today
	if runs != 2 {
		t.Fatalf("runs %d, want 2", runs)
	}
	b.mirrorTick(context.Background(), now.Add(24*time.Hour))
	if runs != 3 {
		t.Fatalf("runs %d, want 3 on the next day", runs)
	}
	if _, err := os.Stat(filepath.Join(dest, "m")); err != nil {
		t.Fatal("mirror dest not created")
	}
}

func TestMirrorCommandArgs(t *testing.T) {
	b := &backupRunner{cfg: BackupConfig{Host: "pi", MirrorRemote: "piquant-collect",
		MirrorPaths: []string{"ticks", "book"}, MirrorDest: "/x"}}
	got := strings.Join(b.mirrorCmd(context.Background()).Args, " ")
	if !strings.Contains(got, "pi:piquant-collect/ticks pi:piquant-collect/book /x/") ||
		!strings.Contains(got, "BatchMode=yes") || strings.Contains(got, "--delete") {
		t.Fatalf("args %q", got)
	}
}

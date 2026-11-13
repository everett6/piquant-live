package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupConfig drives the nightly copy of the Pi's state to this machine. It runs inside the collector process
// (no extra timer): once per local day, at or after Hour, it pulls a tar.gz over ssh.
type BackupConfig struct {
	Host      string   // ssh target, e.g. everett@raspberrypi.local; empty disables backups
	RemoteDir string   // data dir on the Pi, relative to its home (e.g. piquant-data)
	Paths     []string // subdirectories of RemoteDir to copy
	Dest      string   // local folder for YYYY-MM-DD.tar.gz files
	Keep      int      // archives to keep (oldest pruned)
	Hour      int      // local hour after which the day's backup is due
	GitDir    string   // optional: a clone of a PRIVATE repo; ledgers are committed and pushed there
	Check     time.Duration
	Alert     func(key, subject, body string) // optional: called on failures (the caller rate-limits)
	// ExtraFiles are local ledger files (e.g. the desktop carry ledger) copied into GitDir/desktop on each push.
	ExtraFiles []string
	// MirrorRemote (relative to the Pi's home) and MirrorPaths are rsynced into MirrorDest once a day after the
	// backup hour: an incremental copy of the tick/book recording, too big to re-tar every night. Files deleted
	// on the Pi (for example after a month is packed) are kept here. Empty MirrorRemote disables it.
	MirrorRemote string
	MirrorPaths  []string
	MirrorDest   string

	// command builds the fetch command; tests replace it.
	command func(ctx context.Context) *exec.Cmd
	// mirrorCommand builds the rsync command; tests replace it.
	mirrorCommand func(ctx context.Context) *exec.Cmd
}

// backupRunner tracks the last attempt so a failed fetch retries after 30 minutes, not every check.
type backupRunner struct {
	cfg         BackupConfig
	logf        func(format string, a ...any)
	lastAttempt time.Time
	mirrorTry   time.Time
	mirrorDay   string // local date of the last successful mirror
}

// RunBackups blocks until ctx ends.
func RunBackups(ctx context.Context, cfg BackupConfig, logf func(string, ...any)) {
	if cfg.Host == "" {
		return
	}
	if cfg.Keep <= 0 {
		cfg.Keep = 30
	}
	if cfg.Check <= 0 {
		cfg.Check = 10 * time.Minute
	}
	b := &backupRunner{cfg: cfg, logf: logf}
	t := time.NewTicker(cfg.Check)
	defer t.Stop()
	for {
		b.tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *backupRunner) archivePath(day time.Time) string {
	return filepath.Join(b.cfg.Dest, day.Format("2006-01-02")+".tar.gz")
}

// due reports whether today's archive should be fetched now (failed attempts retry after 30 minutes).
func (b *backupRunner) due(now time.Time) bool {
	if now.Hour() < b.cfg.Hour || now.Sub(b.lastAttempt) < 30*time.Minute {
		return false
	}
	_, err := os.Stat(b.archivePath(now))
	return errors.Is(err, os.ErrNotExist)
}

func (b *backupRunner) tick(ctx context.Context, now time.Time) {
	b.mirrorTick(ctx, now)
	if !b.due(now) {
		return
	}
	b.lastAttempt = now
	n, err := b.fetch(ctx, now)
	if err != nil {
		b.logf("[BACKUP] Pi backup failed: %v", err)
		if b.cfg.Alert != nil {
			b.cfg.Alert("backup", "Pi backup failed", fmt.Sprintf("%v\n\nIt retries every 30 minutes.", err))
		}
		return
	}
	b.logf("[BACKUP] saved %s (%d KB)", b.archivePath(now), n>>10)
	if err := pruneArchives(b.cfg.Dest, b.cfg.Keep); err != nil {
		b.logf("[BACKUP] prune: %v", err)
	}
	if b.cfg.GitDir != "" {
		if err := b.pushLedgers(ctx, b.archivePath(now), now); err != nil {
			b.logf("[BACKUP] ledger push failed: %v", err)
			if b.cfg.Alert != nil {
				b.cfg.Alert("ledger-push", "Ledger push to the private repo failed", err.Error())
			}
		}
	}
}

func (b *backupRunner) cmd(ctx context.Context) *exec.Cmd {
	if b.cfg.command != nil {
		return b.cfg.command(ctx)
	}
	remote := fmt.Sprintf("cd %s && tar czf - --ignore-failed-read %s", shellQuote(b.cfg.RemoteDir),
		strings.Join(quoteAll(b.cfg.Paths), " "))
	return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", b.cfg.Host, remote)
}

func (b *backupRunner) mirrorDue(now time.Time) bool {
	return b.cfg.MirrorRemote != "" && now.Hour() >= b.cfg.Hour && now.Sub(b.mirrorTry) >= 30*time.Minute &&
		b.mirrorDay != now.Format("2006-01-02")
}

func (b *backupRunner) mirrorCmd(ctx context.Context) *exec.Cmd {
	if b.cfg.mirrorCommand != nil {
		return b.cfg.mirrorCommand(ctx)
	}
	args := []string{"-a", "--partial", "--ignore-missing-args", "-e", "ssh -o BatchMode=yes -o ConnectTimeout=15"}
	for _, p := range b.cfg.MirrorPaths {
		args = append(args, b.cfg.Host+":"+filepath.Join(b.cfg.MirrorRemote, p))
	}
	return exec.CommandContext(ctx, "rsync", append(args, b.cfg.MirrorDest+"/")...)
}

// mirrorTick rsyncs the Pi's recording once a day; a failure alerts and retries after 30 minutes.
func (b *backupRunner) mirrorTick(ctx context.Context, now time.Time) {
	if !b.mirrorDue(now) {
		return
	}
	b.mirrorTry = now
	if err := os.MkdirAll(b.cfg.MirrorDest, 0o755); err != nil {
		b.logf("[BACKUP] mirror: %v", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	c := b.mirrorCmd(cctx)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		msg := fmt.Sprintf("%v: %s", err, strings.TrimSpace(stderr.String()))
		b.logf("[BACKUP] tick-data mirror failed: %s", msg)
		if b.cfg.Alert != nil {
			b.cfg.Alert("mirror", "Pi tick-data mirror failed", msg+"\n\nIt retries every 30 minutes.")
		}
		return
	}
	b.mirrorDay = now.Format("2006-01-02")
	b.logf("[BACKUP] tick data mirrored to %s", b.cfg.MirrorDest)
}

// fetch streams the archive to a temp file, checks it is a non-empty gzip, then renames it into place.
func (b *backupRunner) fetch(ctx context.Context, now time.Time) (int64, error) {
	if err := os.MkdirAll(b.cfg.Dest, 0o755); err != nil {
		return 0, err
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	final := b.archivePath(now)
	tmp := final + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp)
	c := b.cmd(cctx)
	var stderr bytes.Buffer
	c.Stdout, c.Stderr = f, &stderr
	runErr := c.Run()
	cerr := f.Close()
	if runErr != nil {
		return 0, fmt.Errorf("%v: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	if cerr != nil {
		return 0, cerr
	}
	st, err := os.Stat(tmp)
	if err != nil {
		return 0, err
	}
	if err := checkGzip(tmp); err != nil {
		return 0, err
	}
	return st.Size(), os.Rename(tmp, final)
}

func checkGzip(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var hdr [2]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return fmt.Errorf("archive empty or short: %w", err)
	}
	if hdr != [2]byte{0x1f, 0x8b} {
		return errors.New("archive is not gzip")
	}
	return nil
}

// pruneArchives keeps the newest keep YYYY-MM-DD.tar.gz files in dir.
func pruneArchives(dir string, keep int) error {
	m, err := filepath.Glob(filepath.Join(dir, "????-??-??.tar.gz"))
	if err != nil || len(m) <= keep {
		return err
	}
	sort.Strings(m)
	for _, p := range m[:len(m)-keep] {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}

// pushLedgers unpacks the archive's ledgers into GitDir/pi and commits and pushes them. It refuses a repo whose
// remote is the public piquant-live code repo.
func (b *backupRunner) pushLedgers(ctx context.Context, archive string, now time.Time) error {
	git := func(args ...string) (string, error) {
		c := exec.CommandContext(ctx, "git", append([]string{"-C", b.cfg.GitDir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	remote, err := git("remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if strings.Contains(remote, "piquant-live") {
		return fmt.Errorf("refusing to push ledgers to %s (public code repo)", strings.TrimSpace(remote))
	}
	out := filepath.Join(b.cfg.GitDir, "pi")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	tar := exec.CommandContext(ctx, "tar", "xzf", archive, "-C", out, "--wildcards",
		"paper/dryrun_journal.jsonl", "paper/dryrun_state.json", "trend/*")
	if o, err := tar.CombinedOutput(); err != nil {
		return fmt.Errorf("tar: %v: %s", err, strings.TrimSpace(string(o)))
	}
	for _, f := range b.cfg.ExtraFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue // not created yet
		}
		dst := filepath.Join(b.cfg.GitDir, "desktop", filepath.Base(f))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	specs := []string{"pi"}
	if _, err := os.Stat(filepath.Join(b.cfg.GitDir, "desktop")); err == nil {
		specs = append(specs, "desktop")
	}
	if _, err := git(append([]string{"add", "-A"}, specs...)...); err != nil {
		return err
	}
	if st, _ := git(append([]string{"status", "--porcelain"}, specs...)...); strings.TrimSpace(st) == "" {
		return nil
	}
	if _, err := git("commit", "-q", "-m", "Pi ledgers "+now.Format("2006-01-02")); err != nil {
		return err
	}
	_, err = git("push", "-q")
	return err
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = shellQuote(s)
	}
	return out
}

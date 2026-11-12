package backtest

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, path, csv string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("X-aggTrades.csv")
	_, _ = w.Write([]byte(csv))
	_ = zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConvertZipToPQBinMatchesSavePQBin(t *testing.T) {
	dir := t.TempDir()
	csv := "1,42000.10,0.5,10,11,1704067200000,true,true\n" +
		"2,42000.20,0.25,12,12,1704067200000,false,true\n" +
		"3,41999.90,1.0,13,15,1704067200250,true,true\n"
	zp := filepath.Join(dir, "a.zip")
	writeZip(t, zp, csv)
	out := filepath.Join(dir, "real.pqbin")
	n, err := ConvertZipToPQBin(zp, out, "btcusdt", 2024, 1)
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var ticks []AggTradeTick
	if err := StreamZipArchive(zp, func(x *AggTradeTick) error { ticks = append(ticks, *x); return nil }); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(dir, "ref.pqbin")
	if err := SavePQBin(ref, "BTCUSDT", 2024, 1, ticks); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(out)
	b, _ := os.ReadFile(ref)
	if len(a) != len(b) {
		t.Fatalf("sizes %d vs %d", len(a), len(b))
	}
	a[25] = 0 // the only intended difference: the real-source marker
	if !bytes.Equal(a, b) {
		t.Fatal("streamed partition differs from SavePQBin output")
	}
	if real, err := PQBinIsReal(out); err != nil || !real {
		t.Fatalf("real marker missing: %v %v", real, err)
	}
	if real, _ := PQBinIsReal(ref); real {
		t.Fatal("SavePQBin output marked real")
	}
	var got []AggTradeTick
	if _, err := LoadPQBinFile(out, func(x *AggTradeTick) error { got = append(got, *x); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Price != 42000.10 || !got[0].IsBuyerMaker || got[1].IsBuyerMaker ||
		got[1].TimestampNs <= got[0].TimestampNs {
		t.Fatalf("round trip %+v", got)
	}
}

func TestMicrosecondTimestamps(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "us.zip")
	writeZip(t, zp, "1,95000.0,0.1,1,1,1735689600123456,false,true\n") // 2025-01-01 00:00:00.123456 in µs
	var ns int64
	if err := StreamZipArchive(zp, func(x *AggTradeTick) error { ns = x.TimestampNs; return nil }); err != nil {
		t.Fatal(err)
	}
	if ns != 1735689600123456000 {
		t.Fatalf("ns = %d", ns)
	}
	if _, err := ConvertZipToPQBin(filepath.Join(dir, "missing.zip"), filepath.Join(dir, "x.pqbin"), "X", 2025, 1); err == nil {
		t.Fatal("missing zip accepted")
	}
}

func TestOutOfOrderTimestamps(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "o.zip")
	writeZip(t, zp, "1,100.0,1,1,1,1704067200500,false,true\n2,100.0,1,2,2,1704067200490,false,true\n")
	n, clamped, err := ConvertZipToPQBinStats(zp, filepath.Join(dir, "o.pqbin"), "X", 2024, 1)
	if err != nil || n != 2 || clamped != 1 {
		t.Fatalf("n=%d clamped=%d err=%v", n, clamped, err)
	}
	var ts []int64
	_, _ = LoadPQBinFile(filepath.Join(dir, "o.pqbin"), func(x *AggTradeTick) error { ts = append(ts, x.TimestampNs); return nil })
	if ts[1] < ts[0] {
		t.Fatal("time runs backwards")
	}
	writeZip(t, zp, "1,100.0,1,1,1,1704067205000,false,true\n2,100.0,1,2,2,1704067200000,false,true\n")
	if _, _, err := ConvertZipToPQBinStats(zp, filepath.Join(dir, "bad.pqbin"), "X", 2024, 1); err == nil {
		t.Fatal("5 s backwards step accepted")
	}
}

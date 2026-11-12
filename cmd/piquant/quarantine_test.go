package main

import (
	"flag"
	"strings"
	"testing"
)

func TestQuarantineDefaultsOffAndNeedsOverride(t *testing.T) {
	parse := func(args ...string) *dryRunFlags {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		f := addDryRunFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := parse(); f.mftEntries || f.autoRecalibrate || checkQuarantine(f, "us-futures") != nil {
		t.Fatal("defaults must leave quarantined paths off and pass")
	}
	for _, c := range []struct {
		args   []string
		region string
		want   string
	}{
		{[]string{"--mft-entries"}, "us-futures", "mft-entries"},
		{[]string{"--auto-recalibrate"}, "us-futures", "auto-recalibrate"},
		{nil, "global", "global"},
	} {
		f := parse(c.args...)
		if err := checkQuarantine(f, c.region); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v %s: err %v", c.args, c.region, err)
		}
		f.allowQuarantined = true
		if err := checkQuarantine(f, c.region); err != nil {
			t.Fatalf("override refused: %v", err)
		}
	}
}

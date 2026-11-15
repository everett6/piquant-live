package main

import (
	"context"
	"testing"
)

func TestRunPingHelp(t *testing.T) {
	if err := runPing(context.Background(), []string{"-h"}); err == nil {
		t.Fatalf("expected flag.ErrHelp error on -h")
	}
}

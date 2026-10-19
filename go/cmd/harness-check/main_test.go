package main

import (
	"context"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
)

// The command passes the Eino adapter on a leg and a scenario, and exits 1 for an adapter that
// does nothing: a failing check is a failing run.
func TestTheCommandExitsByTheChecks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	server, err := certification.StartServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	with := func(inner certification.Adapter) certification.Adapter {
		return delegate.With(inner, delegate.Options{})
	}
	if code := certify(server, eino.New(), with, "openai-compatible/whole"); code != 0 {
		t.Fatalf("the Eino adapter's legs on openai-compatible/whole exited %d", code)
	}
	idle := certification.AdapterFunc(func(context.Context, *certification.Run) error { return nil })
	if code := certify(server, idle, with, "engine openai-compatible/whole"); code != 1 {
		t.Fatalf("an idle adapter exited %d", code)
	}
}

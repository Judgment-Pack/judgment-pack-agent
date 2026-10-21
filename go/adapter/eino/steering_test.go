package eino

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestSteerKeepsRequestRankWhenStateUsesSystemTail(t *testing.T) {
	history := []*schema.Message{{Role: schema.Tool, Content: "tool result"}}
	system, messages := placeState("system-tail", history, "stable system", "changing state")
	messages = placeSteer(messages, "framed steer")
	if strings.Contains(system.Head+system.Tail, "framed steer") {
		t.Fatal("steer entered system text")
	}
	if len(messages) != 2 || messages[1].Role != schema.User || !strings.Contains(messages[1].Content, "framed steer") {
		t.Fatalf("steer is not request-ranked: %#v", messages)
	}
}

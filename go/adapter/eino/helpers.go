package eino

import (
	"io"
	"net/http"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

func planReads() []string        { return gate.PlanRuntimeReads }
func contextToolNames() []string { return gate.ContextTools }

func planningTool(tool runcontext.HostTool) bool { return gate.PlanningTool(tool.Declaration()) }

// readLimited reads a refusal's body, at most 64 KiB of it, for the catalogue's codes.
func readLimited(response *http.Response) (string, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return string(body), err
}

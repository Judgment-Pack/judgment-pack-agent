package main

import (
	"encoding/json"
	"flag"
	"fmt"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
)

type matrixCase struct {
	name       string
	agent      bool
	author     string
	role       string
	value      string
	project    bool
	delegation string
}

func main() {
	legacy := flag.Bool("legacy", false, "probe v0.8.1 session normalization")
	flag.Parse()
	cases := []matrixCase{
		{name: "model author omitted"},
		{name: "model author set", author: "low"},
		{name: "model critic off", role: "critic", value: "off"},
		{name: "model compactor low", role: "compactor", value: "low"},
		{name: "model child off, run only", role: "child.review", value: "off"},
		{name: "model child off, project authorized", role: "child.review", value: "off", project: true},
		{name: "model delegated profile off", role: "child.profile", value: "off", delegation: "profile"},
		{name: "agent author omitted", agent: true},
		{name: "agent author set", agent: true, author: "low"},
		{name: "agent critic none", agent: true, role: "critic", value: "none"},
		{name: "agent compactor low", agent: true, role: "compactor", value: "low"},
		{name: "agent subagent none, run only", agent: true, role: "subagent.audit", value: "none"},
		{name: "agent subagent none, project authorized", agent: true, role: "subagent.audit", value: "none", project: true},
		{name: "agent declared subagent none", agent: true, role: "subagent.worker", value: "none", delegation: "subagent"},
	}
	for _, row := range cases {
		models := map[string]any{
			"author":          map[string]any{"model": "connected"},
			"critic":          map[string]any{"model": "connected"},
			"compactor":       map[string]any{"model": "connected", "reasoning": "low"},
			"child.review":    map[string]any{"model": "connected"},
			"child.profile":   map[string]any{"model": "connected"},
			"subagent.audit":  map[string]any{"model": "connected"},
			"subagent.worker": map[string]any{"model": "connected"},
		}
		if *legacy && row.author == "" {
			models["author"].(map[string]any)["reasoning"] = map[bool]string{false: "off", true: "none"}[row.agent]
		}
		run := runcontext.PolicyLayer{}
		project := runcontext.PolicyLayer{}
		if row.author != "" {
			run["models"] = map[string]any{"author": map[string]any{"reasoning": row.author}}
		}
		if row.role != "" {
			roles, _ := run["models"].(map[string]any)
			if roles == nil {
				roles = map[string]any{}
				run["models"] = roles
			}
			roles[row.role] = map[string]any{"reasoning": row.value}
			if row.project {
				project["models"] = map[string]any{row.role: map[string]any{"reasoning": row.value}}
			}
		}
		resolved, err := runcontext.ResolvePolicy(run, project, runcontext.PolicyLayer{"models": models}, runcontext.PolicyHost{Connections: []string{"connected"}, Agent: row.agent, Requests: 16})
		admitted := err == nil
		request := map[string]string{}
		if admitted {
			if value, ok := resolved.Members["models.author.reasoning"].Value.(string); ok && value != "" {
				request["reasoning"] = value
			}
		}
		encoded, _ := json.Marshal(request)
		fmt.Printf("%s\t%t\t%s\n", row.name, admitted, encoded)
	}
}

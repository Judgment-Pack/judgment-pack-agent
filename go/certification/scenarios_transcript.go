package certification

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

type transcriptFile struct {
	Turns []struct {
		Request string `json:"request"`
	} `json:"turns"`
	HostTools []ScenarioHostTool `json:"hostTools"`
}

// openingTexts are the first user message's text parts of a request on the OpenAI-compatible wire.
func openingTexts(request EndpointRequest) []string {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(request.Body, &body)
	for _, message := range body.Messages {
		if message.Role != "user" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			return []string{text}
		}
		var parts []struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(message.Content, &parts)
		var out []string
		for _, part := range parts {
			out = append(out, part.Text)
		}
		return out
	}
	return nil
}

var indexMarker = regexp.MustCompile(`(?m)^BEGIN RETRIEVED DATA ([0-9a-f]{32}) from ` + regexp.QuoteMeta(runcontext.IndexSource) + `;`)

type indexData struct {
	Steps []struct {
		Tool string `json:"tool"`
	} `json:"steps"`
	Omitted int `json:"omitted"`
}

// readIndex is the JSON inside an index's data block.
func readIndex(text string) (indexData, bool) {
	var data indexData
	found := indexMarker.FindStringSubmatch(text)
	if found == nil {
		return data, false
	}
	inner, ok := runcontext.UnframeData(strings.TrimPrefix(text, runcontext.IndexNotice), found[1])
	if !ok {
		return data, false
	}
	return data, json.Unmarshal([]byte(inner), &data) == nil
}

// Transcript is scenario 6: the chat's index and ledger across three runs, and a fourth on the chat
// after it was disposed of.
var Transcript = ScenarioCheck{
	Scenario: "multi-turn",
	Names:    []string{"6 reads", "6 index", "6 ledger"},
	Run: func(ctx context.Context, server *Server, answerAs string, adapter Adapter) ([]Finding, error) {
		var file transcriptFile
		if err := ScenarioFile("multi-turn", &file); err != nil {
			return nil, err
		}
		ledger := &HostLedger{}
		hostTools := HostToolsOf(file.HostTools, ledger)
		host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "multi-turn", AnswerAs: answerAs}, hostTools, true)
		if err != nil {
			return nil, err
		}
		chat := runcontext.NewChatContext("")
		runtime := &runcontext.RuntimeIdentity{Identity: "jpack", Version: "0.19.0"}
		var runs []*ScenarioRun
		var opening [][]string
		var reads [][3]int
		var held []int
		sent := 0
		turn := func(request string) (*ScenarioRun, []string, error) {
			input := requestInput(request)
			input.Chat = chat
			input.Runtime = runtime
			ran, err := host.runOn(ctx, adapter, &Run{Prompt: request, AllowConversation: true, HostTools: hostTools, Context: input})
			if err != nil {
				return nil, nil, err
			}
			var first []string
			if len(ran.Requests) > sent {
				first = openingTexts(ran.Requests[sent])
			}
			ran.Requests = ran.Requests[sent:]
			sent += len(ran.Requests)
			return ran, first, nil
		}
		for at, item := range file.Turns {
			if at > 0 {
				if err := host.session.Advance(ctx); err != nil {
					return nil, err
				}
			}
			ran, first, err := turn(item.Request)
			if err != nil {
				return nil, err
			}
			runs = append(runs, ran)
			opening = append(opening, first)
			schemaReads := 0
			for _, call := range ran.Seen {
				if call.Name == "get_schema" {
					schemaReads++
				}
			}
			executed := ledger.executed()
			reads = append(reads, [3]int{schemaReads, executed["get_authoring_instructions"], executed["read_pack_tests"]})
			held = append(held, len(chat.Entries()))
		}
		chat.Dispose()
		after, _, err := turn("Once more.")
		if err != nil {
			return nil, err
		}
		var out []Finding
		f := &findings{check: "6 reads"}
		for at, read := range reads {
			if read != [3]int{1, 1, 1} {
				f.fail("after turn %d the sources answered %v", at+1, read)
			}
		}
		for at, ran := range runs {
			if !slices.Contains(callNames(ran.Events), "get_schema") {
				f.fail("turn %d did not call get_schema", at+1)
			}
			if errs := errorsOf(ran.Events); len(errs) > 0 {
				f.fail("turn %d: %s", at+1, errs[0].Message)
			}
		}
		out = append(out, f.list...)
		f = &findings{check: "6 index"}
		indexOf := func(texts []string) (string, bool) {
			for _, text := range texts {
				if strings.HasPrefix(text, runcontext.IndexNotice) {
					return text, true
				}
			}
			return "", false
		}
		if _, ok := indexOf(opening[0]); ok {
			f.fail("the first turn carried an index")
		}
		policy := runcontext.Defaults
		for at := 1; at < len(opening); at++ {
			text, ok := indexOf(opening[at])
			if !ok {
				f.fail("turn %d carried no index", at+1)
				continue
			}
			data, ok := readIndex(text)
			var tools []string
			for _, step := range data.Steps {
				tools = append(tools, step.Tool)
			}
			if !ok || !slices.Equal(tools, []string{"get_schema", "get_authoring_instructions", "read_pack_tests"}) || data.Omitted != 0 ||
				runcontext.UTF8Bytes(text) > policy.TurnIndexBytes || len(data.Steps) > policy.TurnIndexEntries {
				f.fail("turn %d's index listed %v, omitted %d, %d bytes", at+1, tools, data.Omitted, runcontext.UTF8Bytes(text))
			}
		}
		out = append(out, f.list...)
		f = &findings{check: "6 ledger"}
		for at, size := range held {
			if size <= 0 {
				f.fail("the ledger held nothing after turn %d", at+1)
			}
		}
		if len(held) > 0 && held[len(held)-1] < held[0] {
			f.fail("the ledger shrank: %v", held)
		}
		if chat.TurnCount() != 3 || len(chat.Entries()) != 0 || !chat.IsDisposed() {
			f.fail("turns %d, entries %d, disposed %t", chat.TurnCount(), len(chat.Entries()), chat.IsDisposed())
		}
		errs := errorsOf(after.Events)
		last := contract.RunEvent{}
		if len(after.Events) > 0 {
			last = after.Events[len(after.Events)-1]
		}
		if len(after.Requests) != 0 || len(errs) == 0 || !(strings.Contains(errs[0].Message, "disposed") || strings.Contains(errs[0].Message, "could not complete")) || last.End == nil {
			f.fail("the run after dispose sent %d requests and ended %v", len(after.Requests), eventTypes(after.Events))
		}
		return append(out, f.list...), nil
	},
}

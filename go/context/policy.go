package runcontext

import (
	"errors"
	"regexp"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// Policy is the budget one run works under (ContextPolicy in context/policy.ts): the Desk's table
// (contract/desk/policy.json), and the bounds the harness adds to it. Go cannot embed a file
// outside its module, so the table is written here and policy_test.go holds it equal to that file.
type Policy struct {
	Version             int `json:"version"`
	MaxInputBytes       int `json:"maxInputBytes"`
	ContextWindowTokens int `json:"contextWindowTokens"`
	OutputTokens        int `json:"outputTokens"`
	ToolResultBytes     int `json:"toolResultBytes"`
	ReadBytes           int `json:"readBytes"`
	RetainedBytes       int `json:"retainedBytes"`
	RecentMessages      int `json:"recentMessages"`
	SummaryBytes        int `json:"summaryBytes"`
	HistoryBytes        int `json:"historyBytes"`
	BriefBytes          int `json:"briefBytes"`
	MemoryBytes         int `json:"memoryBytes"`
	// The critic's switch the harness adds (ADR-0010): the mode where neither the run nor the project
	// states one, and the critic's tier where the host names none; and the switch before it, read as
	// an alias of CriticDefault for one release (nil states none; the harness's table carries none).
	CriticDefault        string `json:"criticDefault"`
	CriticReasoningFloor string `json:"criticReasoningFloor"`
	AdversarialReview    *bool  `json:"adversarialReview,omitempty"`
	// The time bounds the harness adds (TIME_DEFAULTS).
	TurnMilliseconds     int `json:"turnMilliseconds"`
	ToolCallMilliseconds int `json:"toolCallMilliseconds"`
	// The image bounds (IMAGE_DEFAULTS).
	ImagesPerRequest     int `json:"imagesPerRequest"`
	ImageBytesPerRequest int `json:"imageBytesPerRequest"`
	// The test-tool bounds (TEST_DEFAULTS).
	TestCallsPerTurn int `json:"testCallsPerTurn"`
	TestPackBytes    int `json:"testPackBytes"`
	TestMatrixBytes  int `json:"testMatrixBytes"`
	// The bounds on a chat's index of earlier results (INDEX_DEFAULTS).
	TurnIndexEntries int `json:"turnIndexEntries"`
	TurnIndexBytes   int `json:"turnIndexBytes"`
	// The bound on the project's own instructions (PROMPT_DEFAULTS, ADR-0005).
	ProjectInstructionsBytes int `json:"projectInstructionsBytes"`
	// The bounds on tools with a source (SOURCE_DEFAULTS, ADR-0006).
	ToolDescriptionBytes   int `json:"toolDescriptionBytes"`
	OfferedSchemas         int `json:"offeredSchemas"`
	OfferedSchemaBytes     int `json:"offeredSchemaBytes"`
	CataloguedTools        int `json:"cataloguedTools"`
	CatalogueBytes         int `json:"catalogueBytes"`
	SourceResultBytes      int `json:"sourceResultBytes"`
	SourceCallsPerTurn     int `json:"sourceCallsPerTurn"`
	SourceCallMilliseconds int `json:"sourceCallMilliseconds"`
	// The skills budget (SKILL_DEFAULTS, ADR-0009).
	SkillBytes             int `json:"skillBytes"`
	SkillsPerRun           int `json:"skillsPerRun"`
	SkillsCatalogueBytes   int `json:"skillsCatalogueBytes"`
	CataloguedSkills       int `json:"cataloguedSkills"`
	CataloguedSubagents    int `json:"cataloguedSubagents"`
	SubagentCatalogueBytes int `json:"subagentCatalogueBytes"`
	MemoryProposalBytes    int `json:"memoryProposalBytes"`
	MemoryProposalsPerRun  int `json:"memoryProposalsPerRun"`
	EditsPerTurn           int `json:"editsPerTurn"`
	EditBytes              int `json:"editBytes"`
	ProposalBytes          int `json:"proposalBytes"`
}

// TimeDefaults are the time bounds the harness adds to the Desk's table (TIME_DEFAULTS): one
// turn's wall clock, after which the run pauses as budget_paused does, and one tool call's, after
// which the model reads a failed result. The Desk's table carries neither at the pinned commit; a
// table that does wins over these.
var TimeDefaults = struct{ TurnMilliseconds, ToolCallMilliseconds int }{15 * 60_000, 120_000}

// ImageDefaults are how many images, and how many of their bytes decoded, one model request may
// carry (agent#5, IMAGE_DEFAULTS). Not in the Desk's table; a table that carries them wins.
// Sixteen MiB is one image at its own bound.
var ImageDefaults = struct{ ImagesPerRequest, ImageBytesPerRequest int }{8, 16 * 1024 * 1024}

// TestDefaults are the bounds the harness puts on the Runtime's test tool (TEST_DEFAULTS): how
// many test calls one turn may make, and the most bytes the pack text and the matrix text may
// each be, which the tool gate holds on the wire. A table that carries one wins over these.
var TestDefaults = struct{ TestCallsPerTurn, TestPackBytes, TestMatrixBytes int }{3, 65_536, 65_536}

// IndexDefaults are the bounds on the index a run's first message carries of the results earlier
// runs in its chat kept (agent#8, INDEX_DEFAULTS): how many results it lists, and its most bytes,
// data block included. Not in the Desk's table; a table that carries them wins.
var IndexDefaults = struct{ TurnIndexEntries, TurnIndexBytes int }{20, 8000}

// PromptDefaults is the most UTF-8 bytes the project's own instructions may be (ADR-0005,
// PROMPT_DEFAULTS): a longer answer of the host's ProjectInstructions is refused before the run's
// first request, as a brief over BriefBytes and memory over MemoryBytes are. Not in the Desk's
// table; a table that carries it wins.
var PromptDefaults = struct{ ProjectInstructionsBytes int }{4000}

// SourceDefaults are the bounds on tools with a source (ADR-0006, SOURCE_DEFAULTS): the most bytes
// of a description the model is shown; how many such tools, and how many bytes of their names,
// descriptions and schemas, one run offers before sources become catalogues that load_tool loads
// from; how many tools, and how many bytes, those catalogues hold before the rest are withheld; the
// largest result shown whole; and per source, the calls one turn may make and one call's deadline.
// Not in the Desk's table; a table that carries them wins.
var SourceDefaults = struct {
	ToolDescriptionBytes, OfferedSchemas, OfferedSchemaBytes, CataloguedTools, CatalogueBytes, SourceResultBytes, SourceCallsPerTurn, SourceCallMilliseconds int
}{1024, 16, 24_000, 64, 24_000, 8000, 8, 30_000}

// SkillDefaults are the skills budget (ADR-0009, SKILL_DEFAULTS): the most bytes one skill's
// instructions and resources may take, how many loads one response may make, and how many skills, and
// how many bytes of their names and lines (each line unframed: the frame's lines are the same for
// every entry and not counted), the catalogue in the system text lists before the rest are withheld.
// Not in the Desk's table; a table that carries them wins.
var SkillDefaults = struct{ SkillBytes, SkillsPerRun, SkillsCatalogueBytes, CataloguedSkills int }{8000, 4, 8000, 32}

// DeskPolicy is the Desk's table itself (contract/desk/policy.json, packages/harness/contract/policy.json).
var DeskPolicy = Policy{
	CataloguedSubagents: 16, SubagentCatalogueBytes: 4000,
	Version:             1,
	MaxInputBytes:       180000,
	ContextWindowTokens: 65536,
	OutputTokens:        8192,
	ToolResultBytes:     12000,
	ReadBytes:           6000,
	RetainedBytes:       8388608,
	RecentMessages:      8,
	SummaryBytes:        4000,
	HistoryBytes:        32000,
	BriefBytes:          8000,
	MemoryBytes:         4000,
	// ADR-0010: the critic's switch, which replaced agent#10's adversarialReview false.
	CriticDefault:        "auto",
	CriticReasoningFloor: "author",
}

// Defaults is the policy a session works under (CONTEXT_DEFAULTS): the Desk's table with the
// harness's own bounds. The table wins over the harness's defaults, as the spread order does.
var Defaults = func() Policy {
	p := DeskPolicy
	p.EditsPerTurn = 24
	p.EditBytes = 8000
	p.ProposalBytes = 65536
	p.MemoryProposalBytes = 1000
	p.MemoryProposalsPerRun = 4
	p.TurnMilliseconds = TimeDefaults.TurnMilliseconds
	p.ToolCallMilliseconds = TimeDefaults.ToolCallMilliseconds
	p.ImagesPerRequest = ImageDefaults.ImagesPerRequest
	p.ImageBytesPerRequest = ImageDefaults.ImageBytesPerRequest
	p.TestCallsPerTurn = TestDefaults.TestCallsPerTurn
	p.TestPackBytes = TestDefaults.TestPackBytes
	p.TestMatrixBytes = TestDefaults.TestMatrixBytes
	p.TurnIndexEntries = IndexDefaults.TurnIndexEntries
	p.TurnIndexBytes = IndexDefaults.TurnIndexBytes
	p.ProjectInstructionsBytes = PromptDefaults.ProjectInstructionsBytes
	p.ToolDescriptionBytes = SourceDefaults.ToolDescriptionBytes
	p.OfferedSchemas = SourceDefaults.OfferedSchemas
	p.OfferedSchemaBytes = SourceDefaults.OfferedSchemaBytes
	p.CataloguedTools = SourceDefaults.CataloguedTools
	p.CatalogueBytes = SourceDefaults.CatalogueBytes
	p.SourceResultBytes = SourceDefaults.SourceResultBytes
	p.SourceCallsPerTurn = SourceDefaults.SourceCallsPerTurn
	p.SourceCallMilliseconds = SourceDefaults.SourceCallMilliseconds
	p.SkillBytes = SkillDefaults.SkillBytes
	p.SkillsPerRun = SkillDefaults.SkillsPerRun
	p.SkillsCatalogueBytes = SkillDefaults.SkillsCatalogueBytes
	p.CataloguedSkills = SkillDefaults.CataloguedSkills
	return p
}()

// ModelBudget is one model's context window and output cap (ModelContextBudget in policy.ts).
type ModelBudget struct {
	ContextWindowTokens int `json:"contextWindowTokens"`
	OutputTokens        int `json:"outputTokens"`
}

// Configuration is the assistant context configuration a host may give a run (ContextConfiguration).
type Configuration struct {
	Version      int                    `json:"version"`
	ProjectBrief string                 `json:"projectBrief,omitempty"`
	Models       map[string]ModelBudget `json:"models,omitempty"`
}

var (
	errConfiguration = errors.New("Invalid assistant context configuration")
	errBrief         = errors.New("Project brief must be a relative project file")
	errBudgets       = errors.New("Invalid model context budgets")
	errBudget        = errors.New("Invalid model context budget")
)

var briefRefused = regexp.MustCompile("[\\\\\x00\r\n:]")
var modelNameRefused = regexp.MustCompile("[\r\n\x00]")

// ReadConfiguration is contextConfiguration: the configuration where the value is one, read
// closed (a member it does not name is refused), or the sentence saying why it is not.
func ReadConfiguration(value *Value) (*Configuration, error) {
	if !value.IsObject() {
		return nil, errConfiguration
	}
	version := value.Get("version")
	if version == nil || version.Kind() != KindNumber || version.Num() != 1 {
		return nil, errConfiguration
	}
	for _, name := range value.Keys() {
		if name != "version" && name != "projectBrief" && name != "models" {
			return nil, errConfiguration
		}
	}
	out := &Configuration{Version: 1}
	if brief := value.Get("projectBrief"); brief != nil {
		if !brief.IsString() || brief.Str() == "" {
			return nil, errBrief
		}
		out.ProjectBrief = brief.Str()
	}
	if models := value.Get("models"); models != nil {
		if !models.IsObject() || models.Len() > 128 {
			return nil, errBudgets
		}
		out.Models = map[string]ModelBudget{}
		for _, name := range models.Keys() {
			held := models.Get(name)
			if !held.IsObject() {
				return nil, errBudget
			}
			for _, member := range held.Keys() {
				if member != "contextWindowTokens" && member != "outputTokens" {
					return nil, errBudget
				}
			}
			window, output := held.Get("contextWindowTokens"), held.Get("outputTokens")
			if !isInteger(window) || !isInteger(output) {
				return nil, errBudget
			}
			out.Models[name] = ModelBudget{int(window.Num()), int(output.Num())}
		}
	}
	return out, out.Check()
}

func isInteger(v *Value) bool {
	return v != nil && v.Kind() == KindNumber && v.Num() == float64(int64(v.Num()))
}

// Check holds a configuration to the rules contextConfiguration states: the brief a relative
// project file, and each model budget a window from 4,096 to 2,000,000 tokens with an output cap
// from 1,024 to half the window.
func (c *Configuration) Check() error {
	if c == nil {
		return nil
	}
	if c.Version != 1 {
		return errConfiguration
	}
	if c.ProjectBrief != "" {
		if U16Length(c.ProjectBrief) > 512 || briefRefused.MatchString(c.ProjectBrief) {
			return errBrief
		}
		for _, part := range strings.Split(c.ProjectBrief, "/") {
			if part == "" || part == "." || part == ".." {
				return errBrief
			}
		}
	}
	if len(c.Models) > 128 {
		return errBudgets
	}
	for name, budget := range c.Models {
		if name == "" || strings.TrimSpace(name) != name || U16Length(name) > 128 || modelNameRefused.MatchString(name) ||
			budget.ContextWindowTokens < 4096 || budget.ContextWindowTokens > 2000000 ||
			budget.OutputTokens < 1024 || float64(budget.OutputTokens) > float64(budget.ContextWindowTokens)/2 {
			return errBudget
		}
	}
	return nil
}

// ContextPolicy is contextPolicy: the budget one model runs under. The table first, then the
// window and output cap of the model's capability record where it carries them (agent#4), then
// the assistant profile's own override for the model, which a person wrote and which therefore
// stays the last word. BriefBytes and MemoryBytes are the bounds on the host's parts (ADR-0005);
// the policy takes no reserve for either, so the host's word on memory (Input.Memory) does not
// change it.
//
// capabilities is the capability record's two budget members where the host resolved them
// (capabilitiesFor in capabilities.ts belongs to the wire package, so the record reaches this
// package as these two numbers).
func ContextPolicy(config *Configuration, model string, capabilities *ModelBudget) (Policy, error) {
	policy := Defaults
	if capabilities != nil && capabilities.ContextWindowTokens != 0 && capabilities.OutputTokens != 0 {
		policy.ContextWindowTokens = capabilities.ContextWindowTokens
		policy.OutputTokens = capabilities.OutputTokens
	}
	if config != nil {
		if err := config.Check(); err != nil {
			return policy, err
		}
		if budget, ok := config.Models[model]; ok {
			policy.ContextWindowTokens = budget.ContextWindowTokens
			policy.OutputTokens = budget.OutputTokens
		}
	}
	return policy, nil
}

// InputBytes is inputBytes: the bytes one request's input may hold.
func InputBytes(policy Policy) int {
	by := (policy.ContextWindowTokens - policy.OutputTokens) * 3
	if by < 1024 {
		by = 1024
	}
	if policy.MaxInputBytes < by {
		return policy.MaxInputBytes
	}
	return by
}

// Prefix is prefix: the text where it fits in bytes, otherwise its longest prefix of at most that
// many UTF-8 bytes, never cutting a character in half.
func Prefix(text string, bytes int) string {
	if UTF8Bytes(text) <= bytes {
		return text
	}
	data := []byte(text)
	end := bytes
	if end < 0 {
		end = 0
	}
	if end > len(data) {
		end = len(data)
	}
	for end > 0 && end < len(data) && data[end]&0xc0 == 0x80 {
		end--
	}
	return string(data[:end])
}

// ReadEnvelopeBytes is what a reading tool's result carries beside its window: the reader's own
// metadata (its item, offsets, cursor, a short header) and the result's JSON wrapper. A reader
// whose metadata is longer cuts its window by the excess (READ_ENVELOPE_BYTES).
const ReadEnvelopeBytes = 1024

// ReadWindowBytes is the largest window a reading tool may return: the result limit less the
// envelope, counted in bytes of the window's JSON-encoded text (escapes included). A read held to
// it is one result, never a ledger reference and a preview that the model has to page back
// through read_context. Every reading tool a host offers uses this, through EncodedPrefix.
func ReadWindowBytes(toolResultBytes int) int {
	if window := toolResultBytes - ReadEnvelopeBytes; window > 256 {
		return window
	}
	return 256
}

// EncodedPrefix is encodedPrefix: the longest prefix of text whose JSON encoding is at most bytes
// bytes, quotes and escapes included, never splitting a surrogate pair.
func EncodedPrefix(text string, bytes int) string {
	units := u16(text)
	size, end := 2, 0
	for end < len(units) {
		code, width := codePointAt(units, end)
		cost := 0
		switch {
		case code == 0x22 || code == 0x5c || code == 0x08 || code == 0x09 || code == 0x0a || code == 0x0c || code == 0x0d:
			cost = 2
		case code < 0x20 || (code >= 0xd800 && code <= 0xdfff):
			cost = 6
		case code < 0x80:
			cost = 1
		case code < 0x800:
			cost = 2
		case code < 0x10000:
			cost = 3
		default:
			cost = 4
		}
		if size+cost > bytes {
			break
		}
		size += cost
		end += width
	}
	return U16Slice(text, 0, end)
}

// ContextBudgetError is ContextBudgetError: the context past its budget, as an error of class
// context-limit. Its message is the catalogue's sentence for that class, which is the
// TypeScript's own.
type ContextBudgetError struct{}

// Error is the sentence the person reads.
func (e *ContextBudgetError) Error() string { return failure.Catalog["context-limit"].Message }

// FailureCode is the class the error names (failure.Coded).
func (e *ContextBudgetError) FailureCode() string { return "context-limit" }

// ErrContextBudget is the one ContextBudgetError value; errors.Is finds it.
var ErrContextBudget = &ContextBudgetError{}

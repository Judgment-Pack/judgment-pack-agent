package runcontext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// chatSnapshotVersion is the version a snapshot is written at. Version 2 has version 1's shape and
// says its cache rows were keyed only from arguments their parsed form says exactly (#271). A version 1
// row may hold the answer to other arguments than its key names, and being a key and an entry it cannot
// be told apart, so version 1 still loads and restores without its cache rows (RestoreChatContext).
const chatSnapshotVersion = 2

type ContextSnapshotInvalid struct{}

func (*ContextSnapshotInvalid) Error() string {
	return "ContextSnapshotInvalid: the chat context snapshot is invalid."
}

type UnsupportedContextSnapshotVersion struct{}

func (*UnsupportedContextSnapshotVersion) Error() string {
	return "UnsupportedContextSnapshotVersion: the chat context snapshot version is unsupported."
}

type ContextTurnBindingInvalid struct{}

func (*ContextTurnBindingInvalid) Error() string {
	return "ContextTurnBindingInvalid: the active chat turn cannot be attached."
}

type SnapshotEntry struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Label     string  `json:"label"`
	ValueJSON string  `json:"valueJSON"`
	Bytes     int     `json:"bytes"`
	Digest    string  `json:"digest"`
	Revision  *string `json:"revision,omitempty"`
}
type SnapshotAge struct {
	ID   string `json:"id"`
	Turn int    `json:"turn"`
}
type SnapshotResult struct {
	Tool      string `json:"tool"`
	Arguments string `json:"arguments"`
	Entry     string `json:"entry"`
	Bytes     int    `json:"bytes"`
	FirstLine string `json:"firstLine"`
	Turn      int    `json:"turn"`
}
type SnapshotCache struct {
	Key   string `json:"key"`
	Entry string `json:"entry"`
}
type SnapshotEviction struct {
	Tool      string `json:"tool"`
	Arguments string `json:"arguments"`
	Bytes     int    `json:"bytes"`
}
type SnapshotThread struct {
	Profile       string `json:"profile"`
	Title         string `json:"title"`
	ExchangesJSON string `json:"exchangesJSON"`
	Visible       *int   `json:"visible,omitempty"`
}
type SnapshotTask struct {
	ID     string `json:"id"`
	Thread int    `json:"thread"`
}
type SnapshotReuseValue struct {
	Task          string `json:"task"`
	Model         string `json:"model"`
	Text          string `json:"text"`
	QuestionsJSON string `json:"questionsJSON"`
}
type SnapshotReuse struct {
	Key   string             `json:"key"`
	Value SnapshotReuseValue `json:"value"`
}
type SnapshotTurn struct {
	Number     int      `json:"number"`
	Referenced []string `json:"referenced"`
	Visible    *int     `json:"visible,omitempty"`
	Offered    []int    `json:"offered"`
}
type ChatContextSnapshot struct {
	Version    int                `json:"version"`
	Scope      string             `json:"scope"`
	Turns      int                `json:"turns"`
	Disposed   bool               `json:"disposed"`
	Size       int                `json:"size"`
	Entries    []SnapshotEntry    `json:"entries"`
	Ages       []SnapshotAge      `json:"ages"`
	Results    []SnapshotResult   `json:"results"`
	Cache      []SnapshotCache    `json:"cache"`
	Gone       []string           `json:"gone"`
	Visibility [][]string         `json:"visibility"`
	Evictions  []SnapshotEviction `json:"evictions"`
	Evicted    []int              `json:"evicted"`
	Threads    []SnapshotThread   `json:"threads"`
	Tasks      []SnapshotTask     `json:"tasks"`
	Reusable   []SnapshotReuse    `json:"reusable"`
	Open       []SnapshotTurn     `json:"open"`
}

func snapshotObject(v *Value, required []string, optional ...string) bool {
	if !v.IsObject() {
		return false
	}
	for _, key := range required {
		if !v.Has(key) {
			return false
		}
	}
	return hasOnly(v, append(required, optional...)...)
}
func snapshotCount(v *Value, min float64) bool { return isCount(v, 9007199254740991) && v.Num() >= min }
func snapshotStrings(v *Value, check func(string) bool, sorted bool) bool {
	if !v.IsArray() {
		return false
	}
	seen := map[string]bool{}
	last := ""
	for i, item := range v.Items() {
		if !item.IsString() || seen[item.s] || check != nil && !check(item.s) || sorted && i > 0 && !lessUTF16(last, item.s) {
			return false
		}
		seen[item.s] = true
		last = item.s
	}
	return true
}
func snapshotRefs(v *Value, bound int) bool {
	if !v.IsArray() {
		return false
	}
	seen := map[float64]bool{}
	for _, item := range v.Items() {
		if !snapshotCount(item, 0) || item.Num() >= float64(bound) || seen[item.Num()] {
			return false
		}
		seen[item.Num()] = true
	}
	return true
}
func snapshotSourceArray(source *Value, check func(*Value) bool) bool {
	if !source.IsString() {
		return false
	}
	value, err := readHistorySourceJSON([]byte(source.s))
	if err != nil || !value.IsArray() {
		return false
	}
	for _, item := range value.Items() {
		if check != nil && !check(item) {
			return false
		}
	}
	return value.Text() == source.s
}

// ReadChatContextSnapshot validates the complete portable state before constructing a context.
func ReadChatContextSnapshot(raw []byte) (*ChatContextSnapshot, error) {
	v, err := readCanonicalHistoryJSON(raw, HistoryTotalBytes)
	if err != nil {
		return nil, &ContextSnapshotInvalid{}
	}
	if snapshotCount(v.Get("version"), chatSnapshotVersion+1) {
		return nil, &UnsupportedContextSnapshotVersion{}
	}
	invalid := func() (*ChatContextSnapshot, error) { return nil, &ContextSnapshotInvalid{} }
	required := []string{"version", "scope", "turns", "disposed", "size", "entries", "ages", "results", "cache", "gone", "visibility", "evictions", "evicted", "threads", "tasks", "reusable", "open"}
	if !snapshotObject(v, required) || !snapshotCount(v.Get("version"), 1) || !v.Get("scope").IsString() || !snapshotCount(v.Get("turns"), 0) || v.Get("disposed").kind != KindBool || !snapshotCount(v.Get("size"), 0) {
		return invalid()
	}
	for _, key := range required[5:] {
		if !v.Get(key).IsArray() {
			return invalid()
		}
	}
	scoped := func(id string) bool { return strings.HasPrefix(id, v.Get("scope").s+":") }
	fieldText := func(row *Value, keys ...string) bool {
		for _, key := range keys {
			if !row.Get(key).IsString() {
				return false
			}
		}
		return true
	}
	unique := func(rows *Value, key func(*Value) string) bool {
		seen := map[string]bool{}
		for _, row := range rows.Items() {
			id := key(row)
			if seen[id] {
				return false
			}
			seen[id] = true
		}
		return true
	}
	total := 0
	for _, row := range v.Get("entries").Items() {
		if !snapshotObject(row, []string{"id", "kind", "label", "valueJSON", "bytes", "digest"}, "revision") || !fieldText(row, "id", "kind", "label", "valueJSON", "digest") || !scoped(row.Get("id").s) || !oneOf(row.Get("kind"), "history", "artifact", "request", "tool") || !snapshotCount(row.Get("bytes"), 0) || row.Has("revision") && !row.Get("revision").IsString() {
			return invalid()
		}
		source := row.Get("valueJSON").s
		value, err := readHistorySourceJSON([]byte(source))
		if err != nil || value.Text() != source || len(source) != int(row.Get("bytes").Num()) || ContextDigest(source) != row.Get("digest").s {
			return invalid()
		}
		total += len(source)
	}
	if total != int(v.Get("size").Num()) || !unique(v.Get("entries"), func(row *Value) string { return row.Get("id").s }) {
		return invalid()
	}
	last := ""
	for i, row := range v.Get("ages").Items() {
		if !snapshotObject(row, []string{"id", "turn"}) || !fieldText(row, "id") || !scoped(row.Get("id").s) || !snapshotCount(row.Get("turn"), 1) || row.Get("turn").n > v.Get("turns").n || i > 0 && !lessUTF16(last, row.Get("id").s) {
			return invalid()
		}
		last = row.Get("id").s
	}
	for _, row := range v.Get("results").Items() {
		if !snapshotObject(row, []string{"tool", "arguments", "entry", "bytes", "firstLine", "turn"}) || !fieldText(row, "tool", "arguments", "entry", "firstLine") || !scoped(row.Get("entry").s) || !snapshotCount(row.Get("bytes"), 0) || !snapshotCount(row.Get("turn"), 1) || row.Get("turn").n > v.Get("turns").n {
			return invalid()
		}
	}
	if !unique(v.Get("results"), func(row *Value) string { return row.Get("tool").s + "\n" + row.Get("arguments").s }) {
		return invalid()
	}
	last = ""
	for i, row := range v.Get("cache").Items() {
		if !snapshotObject(row, []string{"key", "entry"}) || !fieldText(row, "key", "entry") || !scoped(row.Get("entry").s) || i > 0 && !lessUTF16(last, row.Get("key").s) {
			return invalid()
		}
		last = row.Get("key").s
	}
	if !snapshotStrings(v.Get("gone"), scoped, false) || v.Get("gone").Len() > evictedIDs {
		return invalid()
	}
	for _, set := range v.Get("visibility").Items() {
		if !snapshotStrings(set, scoped, true) {
			return invalid()
		}
	}
	for _, row := range v.Get("evictions").Items() {
		if !snapshotObject(row, []string{"tool", "arguments", "bytes"}) || !fieldText(row, "tool", "arguments") || !snapshotCount(row.Get("bytes"), 0) {
			return invalid()
		}
	}
	if !snapshotRefs(v.Get("evicted"), v.Get("evictions").Len()) {
		return invalid()
	}
	usedVisibility := map[int]bool{}
	usedThreads := map[int]bool{}
	usedEvictions := map[int]bool{}
	for _, row := range v.Get("threads").Items() {
		if !snapshotObject(row, []string{"profile", "title", "exchangesJSON"}, "visible") || !fieldText(row, "profile", "title") || !snapshotSourceArray(row.Get("exchangesJSON"), nil) || row.Has("visible") && (!snapshotCount(row.Get("visible"), 0) || row.Get("visible").n >= float64(v.Get("visibility").Len())) {
			return invalid()
		}
		if row.Has("visible") {
			usedVisibility[int(row.Get("visible").n)] = true
		}
	}
	if v.Get("tasks").Len() > keptTasks {
		return invalid()
	}
	for _, row := range v.Get("tasks").Items() {
		if !snapshotObject(row, []string{"id", "thread"}) || !fieldText(row, "id") || !snapshotCount(row.Get("thread"), 0) || row.Get("thread").n >= float64(v.Get("threads").Len()) {
			return invalid()
		}
		usedThreads[int(row.Get("thread").n)] = true
	}
	if !unique(v.Get("tasks"), func(row *Value) string { return row.Get("id").s }) {
		return invalid()
	}
	if v.Get("reusable").Len() > keptTasks {
		return invalid()
	}
	for _, row := range v.Get("reusable").Items() {
		value := row.Get("value")
		if !snapshotObject(row, []string{"key", "value"}) || !fieldText(row, "key") || !snapshotObject(value, []string{"task", "model", "text", "questionsJSON"}) || !fieldText(value, "task", "model", "text") || !snapshotSourceArray(value.Get("questionsJSON"), nil) {
			return invalid()
		}
	}
	if !unique(v.Get("reusable"), func(row *Value) string { return row.Get("key").s }) {
		return invalid()
	}
	previous := 0.0
	for _, row := range v.Get("open").Items() {
		if !snapshotObject(row, []string{"number", "referenced", "offered"}, "visible") || !snapshotCount(row.Get("number"), 1) || row.Get("number").n > v.Get("turns").n || row.Get("number").n <= previous || !snapshotStrings(row.Get("referenced"), scoped, false) || !snapshotRefs(row.Get("offered"), v.Get("evictions").Len()) {
			return invalid()
		}
		previous = row.Get("number").n
		if visible := row.Get("visible"); visible != nil {
			if !snapshotCount(visible, 0) || visible.n >= float64(v.Get("visibility").Len()) {
				return invalid()
			}
			usedVisibility[int(visible.n)] = true
		}
		for _, ref := range row.Get("offered").Items() {
			usedEvictions[int(ref.n)] = true
		}
	}
	for _, ref := range v.Get("evicted").Items() {
		usedEvictions[int(ref.n)] = true
	}
	if len(usedThreads) != v.Get("threads").Len() || len(usedVisibility) != v.Get("visibility").Len() || len(usedEvictions) != v.Get("evictions").Len() {
		return invalid()
	}
	if v.Get("disposed").Truth() {
		for _, key := range required[5:] {
			if v.Get(key).Len() != 0 {
				return invalid()
			}
		}
	}
	var out ChatContextSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil {
		return invalid()
	}
	return &out, nil
}
func snapshotSortedKeys[V any](value map[string]V) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
	return keys
}
func snapshotListStrings(value []string) []string { return append([]string{}, value...) }

// Snapshot holds the chat lock for one coherent view, including all active turns and alias tables.
func (c *ChatContext) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

// snapshotClosing releases only the exact turn and captures one coherent view under the chat lock.
func (c *ChatContext) snapshotClosing(turn *ChatTurn) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if turn == nil || turn.context != c {
		return nil, &ContextTurnBindingInvalid{}
	}
	delete(c.open, turn)
	return c.snapshotLocked()
}
func (c *ChatContext) snapshotLocked() ([]byte, error) {
	out := ChatContextSnapshot{Version: chatSnapshotVersion, Scope: c.scope, Turns: c.turns, Disposed: c.disposed, Size: c.size, Entries: []SnapshotEntry{}, Ages: []SnapshotAge{}, Results: []SnapshotResult{}, Cache: []SnapshotCache{}, Gone: snapshotListStrings(c.gone), Visibility: [][]string{}, Evictions: []SnapshotEviction{}, Evicted: []int{}, Threads: []SnapshotThread{}, Tasks: []SnapshotTask{}, Reusable: []SnapshotReuse{}, Open: []SnapshotTurn{}}
	for _, e := range c.entries.values() {
		row := SnapshotEntry{ID: e.ID, Kind: e.Kind, Label: e.Label, ValueJSON: e.Value.Text(), Bytes: e.Bytes, Digest: e.Digest}
		if e.Revision != "" || e.revisionPresent {
			r := e.Revision
			row.Revision = &r
		}
		out.Entries = append(out.Entries, row)
	}
	for _, id := range snapshotSortedKeys(c.ages) {
		out.Ages = append(out.Ages, SnapshotAge{ID: id, Turn: c.ages[id]})
	}
	for _, r := range c.results {
		out.Results = append(out.Results, SnapshotResult{Tool: r.tool, Arguments: r.arguments, Entry: r.entry, Bytes: r.bytes, FirstLine: r.firstLine, Turn: r.turn})
	}
	for _, key := range snapshotSortedKeys(c.cache) {
		out.Cache = append(out.Cache, SnapshotCache{Key: key, Entry: c.cache[key]})
	}
	sets := map[string]int{}
	visibility := func(value map[string]bool) (int, error) {
		if value == nil {
			return 0, &ContextSnapshotInvalid{}
		}
		key := fmt.Sprintf("%p", value)
		if at, ok := sets[key]; ok {
			return at, nil
		}
		at := len(out.Visibility)
		sets[key] = at
		ids := []string{}
		for _, id := range snapshotSortedKeys(value) {
			if value[id] {
				ids = append(ids, id)
			}
		}
		out.Visibility = append(out.Visibility, ids)
		return at, nil
	}
	evictions := map[*evictedResult]int{}
	eviction := func(row *evictedResult) int {
		if at, ok := evictions[row]; ok {
			return at
		}
		at := len(out.Evictions)
		evictions[row] = at
		out.Evictions = append(out.Evictions, SnapshotEviction{Tool: row.tool, Arguments: row.arguments, Bytes: row.bytes})
		return at
	}
	for _, row := range c.evicted {
		out.Evicted = append(out.Evicted, eviction(row))
	}
	threads := map[*TaskThread]int{}
	for _, id := range c.tasks {
		value := c.taskThreads[id]
		if value == nil {
			return nil, &ContextSnapshotInvalid{}
		}
		at, ok := threads[value]
		if !ok {
			at = len(out.Threads)
			threads[value] = at
			var visible *int
			if value.Visible != nil {
				at, err := visibility(value.Visible)
				if err != nil {
					return nil, err
				}
				visible = &at
			}
			out.Threads = append(out.Threads, SnapshotThread{Profile: value.Profile, Title: value.Title, ExchangesJSON: Array(value.Exchanges...).Text(), Visible: visible})
		}
		out.Tasks = append(out.Tasks, SnapshotTask{ID: id, Thread: at})
	}
	for _, key := range c.reusableOrder {
		value := c.reusableResults[key]
		out.Reusable = append(out.Reusable, SnapshotReuse{Key: key, Value: SnapshotReuseValue{Task: value.Task, Model: value.Model, Text: value.Text, QuestionsJSON: Array(value.Questions...).Text()}})
	}
	turns := make([]*ChatTurn, 0, len(c.open))
	for turn := range c.open {
		turns = append(turns, turn)
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i].number < turns[j].number })
	for _, turn := range turns {
		row := SnapshotTurn{Number: turn.number, Referenced: turn.referenced.list(), Offered: []int{}}
		if turn.visible != nil {
			at, err := visibility(turn.visible)
			if err != nil {
				return nil, err
			}
			row.Visible = &at
		}
		for _, offered := range turn.offered {
			row.Offered = append(row.Offered, eviction(offered))
		}
		out.Open = append(out.Open, row)
	}
	raw, err := historyJSONBytes(out)
	if err != nil {
		return nil, err
	}
	if _, err = ReadChatContextSnapshot(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// RestoreChatContext publishes no partially restored state and never opens/increments a turn. A
// version 1 snapshot's entries and index are restored and its cache rows are not (chatSnapshotVersion):
// a read they would have answered runs again, which costs one call and never a wrong answer.
func RestoreChatContext(raw []byte) (*ChatContext, error) {
	saved, err := ReadChatContextSnapshot(raw)
	if err != nil {
		return nil, err
	}
	c := NewChatContext(saved.Scope)
	c.scope = saved.Scope
	c.turns = saved.Turns
	c.disposed = saved.Disposed
	c.size = saved.Size
	c.restoredTurns = map[int]bool{}
	for _, row := range saved.Entries {
		value, _ := ParseJSON([]byte(row.ValueJSON))
		entry := &Entry{ID: row.ID, Kind: row.Kind, Label: row.Label, Value: value, Bytes: row.Bytes, Digest: row.Digest}
		if row.Revision != nil {
			entry.Revision = *row.Revision
			entry.revisionPresent = true
		}
		c.entries.set(entry)
	}
	for _, row := range saved.Ages {
		c.ages[row.ID] = row.Turn
	}
	for _, row := range saved.Results {
		r := &keptResult{tool: row.Tool, arguments: row.Arguments, entry: row.Entry, bytes: row.Bytes, firstLine: row.FirstLine, turn: row.Turn}
		c.results = append(c.results, r)
		c.byCall[row.Tool+"\n"+row.Arguments] = r
	}
	if saved.Version >= chatSnapshotVersion {
		for _, row := range saved.Cache {
			c.cache[row.Key] = row.Entry
		}
	}
	c.gone = snapshotListStrings(saved.Gone)
	for _, id := range c.gone {
		c.goneSet[id] = true
	}
	visibility := make([]map[string]bool, len(saved.Visibility))
	for i, ids := range saved.Visibility {
		set := map[string]bool{}
		for _, id := range ids {
			set[id] = true
		}
		visibility[i] = set
	}
	evictions := make([]*evictedResult, len(saved.Evictions))
	for i, row := range saved.Evictions {
		evictions[i] = &evictedResult{tool: row.Tool, arguments: row.Arguments, bytes: row.Bytes}
	}
	for _, at := range saved.Evicted {
		c.evicted = append(c.evicted, evictions[at])
	}
	threads := make([]*TaskThread, len(saved.Threads))
	for i, row := range saved.Threads {
		exchanges, _ := ParseJSON([]byte(row.ExchangesJSON))
		threads[i] = &TaskThread{Profile: row.Profile, Title: row.Title, Exchanges: exchanges.Items()}
		if row.Visible != nil {
			threads[i].Visible = visibility[*row.Visible]
		}
	}
	for _, row := range saved.Tasks {
		c.tasks = append(c.tasks, row.ID)
		c.taskThreads[row.ID] = threads[row.Thread]
	}
	for _, row := range saved.Reusable {
		questions, _ := ParseJSON([]byte(row.Value.QuestionsJSON))
		c.reusableOrder = append(c.reusableOrder, row.Key)
		c.reusableResults[row.Key] = &ReusableResult{Task: row.Value.Task, Model: row.Value.Model, Text: row.Value.Text, Questions: questions.Items()}
	}
	for _, row := range saved.Open {
		turn := &ChatTurn{context: c, number: row.Number, referenced: newOrderedSet()}
		for _, id := range row.Referenced {
			turn.referenced.add(id)
		}
		if row.Visible != nil {
			turn.visible = visibility[*row.Visible]
		}
		for _, at := range row.Offered {
			turn.offered = append(turn.offered, evictions[at])
		}
		c.open[turn] = true
		c.restoredTurns[row.Number] = true
	}
	return c, nil
}

type restoredChatScope struct {
	context *ChatContext
	number  int
}

func (s *restoredChatScope) OpenTurn() (*ChatTurn, error) {
	s.context.mu.Lock()
	defer s.context.mu.Unlock()
	if s.context.disposed || !s.context.restoredTurns[s.number] {
		return nil, &ContextTurnBindingInvalid{}
	}
	for turn := range s.context.open {
		if turn.number == s.number {
			delete(s.context.restoredTurns, s.number)
			return turn, nil
		}
	}
	return nil, &ContextTurnBindingInvalid{}
}
func (c *ChatContext) ResumeTurn(number int) (ChatScope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disposed || !c.restoredTurns[number] {
		return nil, &ContextTurnBindingInvalid{}
	}
	return &restoredChatScope{context: c, number: number}, nil
}

// hasActiveTurn observes exact logical-turn identity without creating attachment authority.
func (c *ChatContext) hasActiveTurn(number int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for turn := range c.open {
		if turn.number == number {
			return true
		}
	}
	return false
}

package runcontext

import "encoding/json"

// CorpusCheckpointView is an explicit read-only display, never an execution checkpoint.
type CorpusCheckpointView struct {
	Version        int              `json:"version"`
	Kind           string           `json:"kind"`
	Source         HistoryObjectRef `json:"source"`
	SourceVersion  int              `json:"sourceVersion"`
	Derived        HistoryObjectRef `json:"derived"`
	Representation string           `json:"representation"`
	Document       *Value           `json:"document"`
}

func newCorpusCheckpointView(source HistoryObjectRef, document *Value) (CorpusCheckpointView, error) {
	fail := func() (CorpusCheckpointView, error) { return CorpusCheckpointView{}, &ReplayUnavailable{} }
	if !validHistoryRef(source) || archiveValidate("checkpoint", document, true) != nil {
		return fail()
	}
	version := int(document.Get("version").Num())
	if version < 1 || version > 3 {
		return fail()
	}
	raw := []byte(CanonicalJSON(document))
	derived := HistoryObjectRef{HistoryBytesDigest(raw), len(raw)}
	representation := "derived"
	if source == derived {
		cp, e := ReadExecution(document)
		if e != nil || cp == nil {
			return fail()
		}
		representation = "exact"
	}
	copy, e := ParseJSON(raw)
	if e != nil {
		return fail()
	}
	return CorpusCheckpointView{1, "corpus-checkpoint-view", source, version, derived, representation, copy}, nil
}

// ReadCorpusCheckpointView checks self-consistency only. A validated capture supplies provenance.
func ReadCorpusCheckpointView(raw []byte) (CorpusCheckpointView, error) {
	fail := func() (CorpusCheckpointView, error) { return CorpusCheckpointView{}, &ReplayUnavailable{} }
	v, e := readHistoryJSON(raw, HistoryObjectBytes, true, true)
	if e != nil || !snapshotObject(v, []string{"version", "kind", "source", "sourceVersion", "derived", "representation", "document"}) || v.Get("version").Num() != 1 || v.Get("kind").Str() != "corpus-checkpoint-view" {
		return fail()
	}
	checked, e := newCorpusCheckpointView(archiveRef(v.Get("source")), v.Get("document"))
	if e != nil || !archiveEqual(checked.Source, v.Get("source")) || !archiveEqual(checked.Derived, v.Get("derived")) || v.Get("sourceVersion").Num() != float64(checked.SourceVersion) || v.Get("representation").Str() != checked.Representation {
		return fail()
	}
	return checked, nil
}

func (v CorpusCheckpointView) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Version        int              `json:"version"`
		Kind           string           `json:"kind"`
		Source         HistoryObjectRef `json:"source"`
		SourceVersion  int              `json:"sourceVersion"`
		Derived        HistoryObjectRef `json:"derived"`
		Representation string           `json:"representation"`
		Document       json.RawMessage  `json:"document"`
	}{v.Version, v.Kind, v.Source, v.SourceVersion, v.Derived, v.Representation, json.RawMessage(CanonicalJSON(v.Document))})
}

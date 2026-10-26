package runcontext

import "encoding/json"

// Validates every typed dependency before the containing owner publishes any member.
func historyObservationClosure(roots []HistoryObjectRef, resolve func(HistoryObjectRef) (HistoryObject, error), recorded map[string]int) ([]HistoryObject, error) {
	objects := map[string]HistoryObject{}
	ordered := []HistoryObject{}
	observations := map[string]*Value{}
	active := map[string]bool{}
	total := 0
	object := func(ref HistoryObjectRef) (HistoryObject, error) {
		if !validHistoryRef(ref) {
			return HistoryObject{}, &HistoryInputInvalid{}
		}
		if old, ok := objects[ref.Digest]; ok {
			if old.Bytes != ref.Bytes {
				return HistoryObject{}, &HistoryInputInvalid{}
			}
			return old, nil
		}
		value, err := resolve(ref)
		if err != nil {
			return HistoryObject{}, err
		}
		if value.Bytes != ref.Bytes || len(value.Value) != ref.Bytes || HistoryBytesDigest(value.Value) != ref.Digest || len(objects) >= 50000 || total+ref.Bytes > HistoryTotalBytes {
			return HistoryObject{}, &HistoryInputInvalid{}
		}
		total += ref.Bytes
		objects[ref.Digest] = value
		ordered = append(ordered, value)
		return value, nil
	}
	refValue := func(v *Value) HistoryObjectRef { var ref HistoryObjectRef; json.Unmarshal(v.JSON(), &ref); return ref }
	payload := func(ref HistoryObjectRef, canonical bool) error {
		value, err := object(ref)
		if err != nil {
			return err
		}
		manifest, err := ReadHistoryChunkManifest(value.Value)
		if err != nil {
			return err
		}
		full := []byte{}
		for _, part := range manifest.Parts {
			value, err = object(part)
			if err != nil {
				return err
			}
			full = append(full, value.Value...)
		}
		if HistoryBytesDigest(full) != manifest.Digest {
			return &HistoryInputInvalid{}
		}
		if canonical {
			_, err = readHistoryRequestJSON(full)
		} else {
			_, err = readHistorySourceJSON(full)
		}
		return err
	}
	var visit func(HistoryObjectRef, int, string, *Value, int) error
	visit = func(ref HistoryObjectRef, depth int, expected string, scope *Value, before int) error {
		sequence, ok := recorded[ref.Digest]
		if depth > 64 || !ok || before > 0 && sequence >= before || active[ref.Digest] {
			return &HistoryInputInvalid{}
		}
		value, err := object(ref)
		if err != nil {
			return err
		}
		row := observations[ref.Digest]
		if row == nil {
			row, err = ReadHistoryObservation(value.Value)
			if err != nil {
				return err
			}
		}
		if expected != "" && row.Get("kind").Str() != expected || scope != nil && CanonicalJSON(row.Get("scope")) != CanonicalJSON(scope) {
			return &HistoryInputInvalid{}
		}
		if observations[ref.Digest] != nil {
			return nil
		}
		active[ref.Digest] = true
		switch row.Get("kind").Str() {
		case "original":
			err = payload(refValue(row.Get("value")), false)
		case "compaction":
			if row.Get("originals").Len() < 1 {
				return &HistoryInputInvalid{}
			}
			for _, ref := range row.Get("originals").Items() {
				if err = visit(refValue(ref), depth+1, "original", row.Get("scope"), sequence); err != nil {
					return err
				}
			}
			if err = payload(refValue(row.Get("replacement")), false); err != nil {
				return err
			}
			for _, ref := range row.Get("prior").Items() {
				if err = visit(refValue(ref), depth+1, "compaction", row.Get("scope"), sequence); err != nil {
					return err
				}
			}
		case "request":
			if err = payload(refValue(row.Get("request")), true); err != nil {
				return err
			}
			for _, pair := range []struct{ key, kind string }{{"compactions", "compaction"}, {"originals", "original"}} {
				for _, ref := range row.Get(pair.key).Items() {
					if err = visit(refValue(ref), depth+1, pair.kind, row.Get("scope"), sequence+1); err != nil {
						return err
					}
				}
			}
		case "relation":
			var evidence HistoryObject
			evidence, err = object(refValue(row.Get("evidence")))
			if err == nil {
				_, err = readHistoryJSON(evidence.Value, HistoryObjectBytes, true, true)
			}
		}
		if err != nil {
			return err
		}
		delete(active, ref.Digest)
		observations[ref.Digest] = row
		return nil
	}
	for _, root := range roots {
		if err := visit(root, 0, "", nil, 0); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

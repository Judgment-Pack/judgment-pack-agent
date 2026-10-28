package runcontext

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"strconv"
)

const HistoryObjectBytes = 4 * 1024 * 1024
const HistoryTotalBytes = 64 * 1024 * 1024
const HistoryMaxRows = 20000
const HistoryMaxEvents = 10000

type HistoryInputInvalid struct{}

func (*HistoryInputInvalid) Error() string {
	return "HistoryInputInvalid: the retained history input is invalid."
}

type HistoryChanged struct{}

func (*HistoryChanged) Error() string { return "HistoryChanged: the retained history view changed." }

type HistoryObjectRef struct {
	Digest string `json:"digest"`
	Bytes  int    `json:"bytes"`
}
type HistoryObject struct {
	HistoryObjectRef
	Value []byte `json:"-"`
}
type HistoryChunkManifest struct {
	Version int                `json:"version"`
	Bytes   int                `json:"bytes"`
	Digest  string             `json:"digest"`
	Parts   []HistoryObjectRef `json:"parts"`
}

func HistoryBytesDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func NewHistoryObject(value []byte) (HistoryObject, error) {
	if len(value) < 1 || len(value) > HistoryObjectBytes {
		return HistoryObject{}, &HistoryInputInvalid{}
	}
	return HistoryObject{HistoryObjectRef: HistoryObjectRef{Digest: HistoryBytesDigest(value), Bytes: len(value)}, Value: bytes.Clone(value)}, nil
}

var historySHA256 = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func validHistoryRef(ref HistoryObjectRef) bool {
	return ref.Bytes > 0 && ref.Bytes <= HistoryObjectBytes && historySHA256.MatchString(ref.Digest)
}
func historyJSONBytes(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, &HistoryInputInvalid{}
	}
	v, err := ParseJSON(raw)
	if err != nil {
		return nil, &HistoryInputInvalid{}
	}
	return []byte(CanonicalJSON(v)), nil
}
func ChunkHistoryValue(value []byte) (HistoryObject, []HistoryObject, error) {
	if len(value) < 1 || len(value) > HistoryTotalBytes {
		return HistoryObject{}, nil, &HistoryInputInvalid{}
	}
	objects := []HistoryObject{}
	manifest := HistoryChunkManifest{Version: 1, Bytes: len(value), Digest: HistoryBytesDigest(value), Parts: []HistoryObjectRef{}}
	for at := 0; at < len(value); at += HistoryObjectBytes {
		end := at + HistoryObjectBytes
		if end > len(value) {
			end = len(value)
		}
		object, _ := NewHistoryObject(value[at:end])
		objects = append(objects, object)
		manifest.Parts = append(manifest.Parts, object.HistoryObjectRef)
	}
	raw, err := historyJSONBytes(manifest)
	if err != nil {
		return HistoryObject{}, nil, err
	}
	object, err := NewHistoryObject(raw)
	return object, objects, err
}
func readHistoryChunkManifest(raw []byte) (HistoryChunkManifest, error) {
	var out HistoryChunkManifest
	v, err := readCanonicalHistoryJSON(raw, HistoryObjectBytes)
	if err != nil {
		return out, err
	}
	decoder := json.NewDecoder(bytes.NewReader(v.JSON()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || out.Version != 1 || out.Bytes < 1 || out.Bytes > HistoryTotalBytes || !historySHA256.MatchString(out.Digest) || len(out.Parts) < 1 || len(out.Parts) > 16 {
		return HistoryChunkManifest{}, &HistoryInputInvalid{}
	}
	total := 0
	for i, ref := range out.Parts {
		if !validHistoryRef(ref) || i < len(out.Parts)-1 && ref.Bytes != HistoryObjectBytes {
			return HistoryChunkManifest{}, &HistoryInputInvalid{}
		}
		total += ref.Bytes
	}
	if total != out.Bytes {
		return HistoryChunkManifest{}, &HistoryInputInvalid{}
	}
	return out, nil
}

// Canonical new envelopes preserve opaque source text in explicit JSON string fields.
// Bound work before recursion and reject duplicate/lossy spellings by canonical byte comparison.
func readCanonicalHistoryJSON(raw []byte, max int) (*Value, error) {
	return readHistoryJSON(raw, max, true)
}
func readHistorySourceJSON(raw []byte) (*Value, error) {
	return readHistoryJSON(raw, HistoryTotalBytes, false)
}

// Retained request bytes are lexical JSON, separate from canonical outer envelopes.
func readHistoryRequestJSON(raw []byte) (*Value, error) {
	return readHistoryJSON(raw, HistoryTotalBytes, false, true, true)
}
func readHistoryJSON(raw []byte, max int, canonical bool, finite ...bool) (*Value, error) {
	if len(raw) < 1 || len(raw) > max || !validReportUnicode(raw) {
		return nil, &HistoryInputInvalid{}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	var read func(int) (*Value, error)
	read = func(depth int) (*Value, error) {
		nodes++
		if depth > 64 || nodes > len(raw) {
			return nil, &HistoryInputInvalid{}
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, &HistoryInputInvalid{}
		}
		switch t := token.(type) {
		case nil:
			return Null(), nil
		case bool:
			return Bool(t), nil
		case string:
			return String(t), nil
		case json.Number:
			n, err := strconv.ParseFloat(string(t), 64)
			if err != nil || math.IsInf(n, 0) || math.IsNaN(n) || canonical && (len(finite) == 0 || !finite[0]) && (math.Trunc(n) != n || math.Abs(n) > 9007199254740991) {
				return nil, &HistoryInputInvalid{}
			}
			return Number(n), nil
		case json.Delim:
			if t != '{' && t != '[' {
				return nil, &HistoryInputInvalid{}
			}
			value := Object()
			end := json.Delim('}')
			if t == '[' {
				value = Array()
				end = ']'
			}
			for decoder.More() {
				key := ""
				if t == '{' {
					token, err = decoder.Token()
					if err != nil {
						return nil, &HistoryInputInvalid{}
					}
					var ok bool
					key, ok = token.(string)
					if !ok || value.Has(key) {
						return nil, &HistoryInputInvalid{}
					}
				}
				child, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				if t == '[' {
					value.Append(child)
				} else {
					value.Set(key, child)
				}
			}
			token, err = decoder.Token()
			if err != nil || token != end {
				return nil, &HistoryInputInvalid{}
			}
			return value, nil
		}
		return nil, &HistoryInputInvalid{}
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF || canonical && CanonicalJSON(value) != string(raw) || !canonical && !(len(finite) > 1 && finite[1]) && value.Text() != string(raw) {
		return nil, &HistoryInputInvalid{}
	}
	return value, nil
}

// ReadHistoryChunkManifest validates bounded canonical portable chunk metadata.
func ReadHistoryChunkManifest(raw []byte) (HistoryChunkManifest, error) {
	return readHistoryChunkManifest(raw)
}

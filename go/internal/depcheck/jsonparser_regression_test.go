package depcheck

import (
	"testing"

	"github.com/buger/jsonparser"
)

// GO-2026-4514: deleting a key from malformed input must not use invalid offsets.
func TestJSONParserDeleteMalformedInput(t *testing.T) {
	for _, row := range []struct {
		name, input, want string
	}{
		{"missing value", `{"x":}`, `{"x":}`},
		{"truncated object", `{"x":`, `{"x":`},
		{"malformed array", `{"x":[}`, `{"x":[}`},
		{"valid object", `{"x":1,"y":2}`, `{"y":2}`},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := string(jsonparser.Delete([]byte(row.input), "x")); got != row.want {
				t.Fatalf("Delete(%q, x) = %q, want %q", row.input, got, row.want)
			}
		})
	}
}

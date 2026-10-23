package runcontext

import (
	"crypto/sha256"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// typedMemory authors explicit test inputs using the standard library digest.
func typedMemory(text string) []contract.Memory {
	return []contract.Memory{{ID: "legacy-memory", Kind: "fact", Text: text, Source: contract.MemorySource{Run: "host", ProposedBy: "host"}, Scope: "project", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))), CreatedAt: "1970-01-01T00:00:00Z", State: "accepted"}}
}

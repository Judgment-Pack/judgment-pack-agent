// Command contractgen writes go/contract/contract_gen.go from contract/schemas:
//
//	go run ./internal/cmd/contractgen -schemas ../contract/schemas -package contract -out contract/contract_gen.go
//
// run from the go/ directory, or `go generate ./contract`.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/contractgen"
)

func main() {
	schemas := flag.String("schemas", "../contract/schemas", "the directory of the contract's JSON schemas")
	pkg := flag.String("package", "contract", "the Go package to write")
	out := flag.String("out", "contract/contract_gen.go", "the file to write")
	flag.Parse()
	source, err := contractgen.Generate(*schemas, *pkg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "contractgen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, source, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "contractgen:", err)
		os.Exit(1)
	}
}

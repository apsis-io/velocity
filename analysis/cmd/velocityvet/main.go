// Command velocityvet runs velocity's analyzers as a go vet tool:
//
//	go build -o ./bin/velocityvet github.com/apsis-io/velocity/analysis/cmd/velocityvet
//	go vet -vettool=./bin/velocityvet ./...
//
// It must be driven by `go vet -vettool=`, not run directly. Run directly the
// analyzer gets no configuration — no package patterns, no build tags, no
// module root — and reports nothing, or misreports. That constraint comes from
// the go/analysis unitchecker protocol, and unitchecker's own message for a
// direct invocation is templated on the tool's name, so a user who ran
// `go tool velocityvet` is told to run `go velocityvet`, which is not a thing;
// `go velocityvet` is not a Go subcommand either, so both suggested paths fail
// and a user cannot tell whether they are holding it wrong or the tool is
// broken. Detecting the case here and printing the form that works is the whole
// reason this main is not one line.
package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/apsis-io/velocity/analysis/lostrelease"
)

func main() {
	// `go vet -vettool=` drives this by handing it a single *.cfg file; that is
	// the only invocation that works, and unitchecker checks for exactly that
	// argument shape. Anything else is someone who ran the binary, or `go tool`,
	// expecting a program — and unitchecker's own message for that case is
	// templated on the tool's name, so it advises a `go velocityvet` that does
	// not exist. Print the usable form instead, and only then: under `go vet` the
	// argument is a .cfg and this stays silent, because a hint printed once per
	// analysed package is worse than no hint.
	if !drivenByGoVet(os.Args[1:]) {
		fmt.Fprint(os.Stderr, velocityvetHint)
	}

	unitchecker.Main(lostrelease.Analyzer)
}

// drivenByGoVet reports whether the arguments look like the vet protocol rather
// than like a user running the tool. A .cfg path is what `go vet -vettool=`
// passes; matching on the suffix alone cannot be fooled by a package pattern,
// and it is deliberately looser than unitchecker's own check so that a
// legitimate vet invocation is never silenced.
func drivenByGoVet(args []string) bool {
	if len(args) == 0 {
		return false
	}

	for _, arg := range args {
		if strings.HasSuffix(arg, ".cfg") {
			return true
		}
	}

	return false
}

const velocityvetHint = `velocityvet is a go vet tool, not a program to run directly.

  go build -o ./bin/velocityvet github.com/apsis-io/velocity/analysis/cmd/velocityvet
  go vet -vettool=./bin/velocityvet ./...

`

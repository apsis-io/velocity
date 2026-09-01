// Command velocityvet runs velocity's analyzers as a go vet tool:
//
//	go vet -vettool=$(which velocityvet) ./...
package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/apsis-io/velocity/analysis/lostrelease"
)

func main() { unitchecker.Main(lostrelease.Analyzer) }

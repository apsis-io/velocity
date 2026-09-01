package lostrelease_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/apsis-io/velocity/analysis/lostrelease"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), lostrelease.Analyzer, "a")
}

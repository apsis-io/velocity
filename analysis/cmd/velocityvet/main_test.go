package main

import "testing"

// The hint is only worth printing on the invocation it helps. Printed under
// `go vet -vettool=` it would appear once per analysed package and bury the
// actual findings, so the discriminator is tested in both directions: the
// shape `go vet` passes, and the shapes a user reaches for instead.
func TestDrivenByGoVet(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want bool
	}{
		{
			// What `go vet -vettool=` actually passes: a config file path.
			name: "a cfg file",
			args: []string{"/tmp/go-build123/b001/analysis.cfg"},
			want: true,
		},
		{
			name: "cfg among flags",
			args: []string{"-flags", "/tmp/go-build123/b001/analysis.cfg"},
			want: true,
		},
		{
			name: "package patterns",
			args: []string{"./..."},
			want: false,
		},
		{
			name: "one named package",
			args: []string{"./ownership"},
			want: false,
		},
		{
			name: "help",
			args: []string{"help"},
			want: false,
		},
		{
			// A user with no arguments at all gets unitchecker's own usage,
			// which is the right message for that case.
			name: "nothing",
			args: nil,
			want: false,
		},
		{
			// Nothing a package pattern plausibly looks like ends in .cfg, so
			// matching on the suffix cannot silence a real vet run.
			name: "a package named cfg",
			args: []string{"./cfg"},
			want: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := drivenByGoVet(tt.args); got != tt.want {
				t.Fatalf("drivenByGoVet(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

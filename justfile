set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

fmt:
    test -z "$(gofmt -l .)"

# staticcheck runs under the pinned go1.27.1 toolchain: its export-data
# reader stops at version 4, and a local toolchain newer than that (1.27.2
# writes version 5) feeds it what it cannot read. Same pin CI uses — the
# two move together, and both move when staticcheck ships a reader for the
# new format.
vet:
    go vet ./...
    go -C failsafeown vet ./...
    go -C teleos vet ./...
    GOTOOLCHAIN=go1.27.1 staticcheck ./...

# Whitespace style, through golangci-lint's bundled wsl_v5. See .golangci.yml:
# it runs that one linter and nothing else, because the rest of velocity's
# analysis is run directly, each pinned, and adopting a second copy of it is
# duplication with two places to configure the same rule.
#
# golangci-lint must be on PATH; it is not a go tool, so `go -C` cannot reach
# it and each module is run from a subshell. `go -C ./...` in the root would
# not work either — it is a separate module.
#
#   go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
wsl:
    GOTOOLCHAIN=go1.27.1 golangci-lint run ./...
    (cd failsafeown && GOTOOLCHAIN=go1.27.1 golangci-lint run ./...)
    (cd analysis && GOTOOLCHAIN=go1.27.1 golangci-lint run ./...)
    (cd benchmarks && GOTOOLCHAIN=go1.27.1 golangci-lint run ./...)
    (cd teleos && GOTOOLCHAIN=go1.27.1 golangci-lint run ./...)

# Apply the rules rather than report them. Mechanical, and reviewed as its own
# commit.
wsl-fix:
    golangci-lint run --fix ./...
    (cd failsafeown && golangci-lint run --fix ./...)
    (cd analysis && golangci-lint run --fix ./...)
    (cd benchmarks && golangci-lint run --fix ./...)
    (cd teleos && golangci-lint run --fix ./...)

# Run velocity's own analyzers (lostrelease) as a vet tool over every module.
lint:
    GOTOOLCHAIN=go1.27.1 go -C analysis build -o "${TMPDIR:-/tmp}/velocityvet" ./cmd/velocityvet
    GOTOOLCHAIN=go1.27.1 go vet -vettool="${TMPDIR:-/tmp}/velocityvet" ./...
    GOTOOLCHAIN=go1.27.1 go -C benchmarks vet -vettool="${TMPDIR:-/tmp}/velocityvet" ./...
    GOTOOLCHAIN=go1.27.1 go -C analysis test ./...
    GOTOOLCHAIN=go1.27.1 go -C failsafeown vet -vettool="${TMPDIR:-/tmp}/velocityvet" ./...

test:
    go test ./...
    go -C failsafeown test ./...
    go -C teleos test ./...

race:
    go test -race ./...
    go -C failsafeown test -race ./...
    go -C teleos test -race ./...

# Same combination CI runs — -race AND the tag together. They were separate
# recipes here and combined only in CI, which is how a race that needs the
# debug build passed every local check and went red on a release tag.
debug:
    go test -tags=velocitydebug -race ./...

# The same sequence CI runs; see .github/workflows/ci.yml.
check: fmt vet wsl lint test race debug examples

examples:
    go test ./... -run Example

fuzz duration="30s":
    go test ./ownership -run '^$' -fuzz '^FuzzOwnershipModel$' -fuzztime {{duration}}

bench:
    go test ./... -run '^$' -bench . -benchmem

bench-compare count="5":
    go -C benchmarks test ./... -run '^$' -bench . -benchmem -count={{count}}

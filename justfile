set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

fmt:
    test -z "$(gofmt -l .)"

vet:
    go vet ./...
    staticcheck ./...

# Run velocity's own analyzers (lostrelease) as a vet tool over every module.
lint:
    go -C analysis build -o "${TMPDIR:-/tmp}/velocityvet" ./cmd/velocityvet
    go vet -vettool="${TMPDIR:-/tmp}/velocityvet" ./...
    go -C benchmarks vet -vettool="${TMPDIR:-/tmp}/velocityvet" ./...
    go -C analysis test ./...

test:
    go test ./...

race:
    go test -race ./...

debug:
    go test -tags=velocitydebug ./...

# The same sequence CI runs; see .github/workflows/ci.yml.
check: fmt vet lint test race debug examples

examples:
    go test ./... -run Example

fuzz duration="30s":
    go test ./ownership -run '^$' -fuzz '^FuzzOwnershipModel$' -fuzztime {{duration}}

bench:
    go test ./... -run '^$' -bench . -benchmem

bench-compare count="5":
    go -C benchmarks test ./... -run '^$' -bench . -benchmem -count={{count}}

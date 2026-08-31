set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

fmt:
    test -z "$(gofmt -l .)"

vet:
    go vet ./...

test:
    go test ./...

race:
    go test -race ./...

debug:
    go test -tags=velocitydebug ./...

check: fmt vet test race debug

fuzz duration="30s":
    go test ./ownership -run '^$' -fuzz '^FuzzOwnershipModel$' -fuzztime {{duration}}

bench:
    go test ./... -run '^$' -bench . -benchmem

bench-compare count="5":
    go -C benchmarks test ./... -run '^$' -bench . -benchmem -count={{count}}

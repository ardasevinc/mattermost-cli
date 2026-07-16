set shell := ["zsh", "-eu", "-o", "pipefail", "-c"]

default:
    @just --list

go-format-check:
    @unformatted="$(gofmt -l cmd internal)"; if [[ -n "$unformatted" ]]; then print -r -- "$unformatted"; exit 1; fi

go-test:
    go test ./...

go-race:
    go test -race ./...

go-vet:
    go vet ./...

go-modules:
    go mod verify

go-build:
    go build -o "${TMPDIR:-/tmp}/mattermost-cli-mm" ./cmd/mm

oracle-smoke:
    git diff --quiet v1.6.0 -- src package.json bun.lock tsconfig.json
    go run ./cmd/conformance --scenario conformance/scenarios/v1/whoami.json --cwd . -- bun src/index.ts

go-gate: go-format-check go-test go-race go-vet go-modules go-build
    git diff --check

legacy-gate:
    bun run verify

gate: go-gate legacy-gate oracle-smoke

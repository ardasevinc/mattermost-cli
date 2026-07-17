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

go-cross-build:
    @tmp="${TMPDIR:-/tmp}/mattermost-cli-cross"; mkdir -p "$tmp"; for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do os="${target%/*}"; arch="${target#*/}"; echo "building $target"; CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -o "$tmp/mm-$os-$arch" ./cmd/mm; done

docker-e2e:
    bun run test:e2e

oracle-smoke:
    git diff --quiet v1.6.0 -- src package.json bun.lock tsconfig.json
    go run ./cmd/conformance --scenario conformance/scenarios/v1/whoami.json --cwd . -- bun src/index.ts

go-gate: go-format-check go-test go-race go-vet go-modules go-build go-cross-build
    git diff --check

legacy-gate:
    bun run verify

gate: go-gate legacy-gate oracle-smoke

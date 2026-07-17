set shell := ["zsh", "-eu", "-o", "pipefail", "-c"]

default:
    @just --list

go-format-check:
    @unformatted="$(gofmt -l cmd internal tests/e2e)"; if [[ -n "$unformatted" ]]; then print -r -- "$unformatted"; exit 1; fi

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

go-e2e-compile:
    go test -tags=e2e -run '^$' ./tests/e2e
    go vet -tags=e2e ./tests/e2e

go-cross-build:
    @tmp="$(mktemp -d "${TMPDIR:-/tmp}/mattermost-cli-cross.XXXXXX")"; trap 'find "$tmp" -type f -delete; rmdir "$tmp"' EXIT; for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do os="${target%/*}"; arch="${target#*/}"; echo "building $target"; CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -o "$tmp/mm-$os-$arch" ./cmd/mm; done

docker-e2e:
    bun run test:e2e

oracle-smoke:
    git diff --quiet v1.6.0 -- src package.json bun.lock tsconfig.json
    go run ./cmd/conformance --scenario conformance/scenarios/v1/whoami.json --cwd . -- bun src/index.ts

parity-smoke:
    @tmp="$(mktemp -d "${TMPDIR:-/tmp}/mattermost-cli-parity.XXXXXX")"; trap 'find "$tmp" -type f -delete; rmdir "$tmp"' EXIT; go build -o "$tmp/mm" ./cmd/mm; for scenario in conformance/scenarios/pairs/*.json; do go run ./cmd/conformance --pair "$scenario" --cwd . --oracle bun --oracle-prefix src/index.ts --candidate "$tmp/mm"; done

go-gate: go-format-check go-test go-race go-vet go-modules go-build go-e2e-compile go-cross-build
    git diff --check

legacy-gate:
    bun run verify

gate: go-gate legacy-gate oracle-smoke parity-smoke

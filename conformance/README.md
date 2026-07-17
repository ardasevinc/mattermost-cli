# Conformance harness

This directory owns language-neutral Mattermost scenarios used to compare the frozen TypeScript v1.6.0 oracle with Go v2.

Scenarios describe fake-server behavior, expected request order and bytes, subprocess stdout/stderr, and exit classes. `scenario.schema.json` covers one executable. `pair.schema.json` locks independent oracle and candidate expectations in one semantic case, so intentional v2 output changes are explicit instead of hidden by a false byte-equality claim. The Go runner executes both sides in isolated homes against independent fake servers. Fixtures remain after the TypeScript implementation is removed.

The scenario schema and runner land before feature ports. See `docs/V2_CONTRACT.md` and `docs/V1_PARITY_MATRIX.md` for the acceptance boundary.

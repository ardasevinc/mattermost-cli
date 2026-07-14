export const SHORT_MARKDOWN = `# Release ready ✅

**Status:** shipped with [runbook](https://example.test/runbook).

- [x] API healthy
- [x] migrations applied

> Verify the canary before broad rollout.

\`\`\`ts
const ready = true
\`\`\`
`

function buildLongMarkdown(): string {
  let message = `# Extended deployment report 🌍

This intentionally large fixture exercises structured Markdown without relying on generated prose.
`
  let section = 1
  while ([...message].length < 15_500) {
    message += `
## Service ${section}

| Check | Result | Detail |
| --- | --- | --- |
| health | ✅ | [probe](https://example.test/health/${section}) |
| queue | ✅ | **drained** |

- [x] deploy completed
- [x] metrics reviewed
- [ ] observe for 30 minutes

> Service ${section} remained inside its latency budget.

\`\`\`json
{"service":${section},"status":"healthy","regions":["eu","us"],"rollback":false}
\`\`\`
`
    section += 1
  }
  return `${message}
---

End of report. _Keep this final newline._
`
}

export const LONG_MARKDOWN = buildLongMarkdown()
export const LONG_MARKDOWN_BYTES = new TextEncoder().encode(LONG_MARKDOWN).byteLength
export const LONG_MARKDOWN_CHARACTERS = [...LONG_MARKDOWN].length

if (LONG_MARKDOWN_CHARACTERS < 15_500 || LONG_MARKDOWN_CHARACTERS >= 16_200) {
  throw new Error('Long Markdown fixture escaped its intended character range.')
}

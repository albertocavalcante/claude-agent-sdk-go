# Recorded CLI transcripts

Real sessions recorded against Claude Code 2.1.316 (model haiku). Format: one
JSON object per line, `{"t": seconds, "dir": "in"|"out"|"out-raw"|"meta", "msg": ...}`;
`in` is what the SDK wrote to stdin, `out` what the CLI wrote to stdout.

- `core/`: basic, multiturn, perm, interrupt, control.
- `m0/`: plain, resume, fork, compact, subagent, mcp-stdio (a local test MCP server), interrupt, approval hold, approval with stdin closed, approval with the controller gone.
- `hostile-repo/`: the repository-settings experiments (default, trusted, user-only, empty).

The `m0/` set and `core/multiturn` are recorded by this repository's own SDK in
scratch directories with an isolated `CLAUDE_CONFIG_DIR` and `HOME` and
hosted connectors disabled:

    export CLAUDE_CODE_OAUTH_TOKEN=...            # environment only, never on disk
    go run ./scripts/record-fixtures <raw-dir>
    python3 -I scripts/scrub-fixtures/scrub.py <raw-dir>

The scrubber is generic; person- or machine-specific substitutions come from a
JSON file outside the repository named by `$SCRUB_MAP` (format in the script's
docstring). `TestTestdataHasNoPrivateData` must pass afterwards and must never
be loosened.

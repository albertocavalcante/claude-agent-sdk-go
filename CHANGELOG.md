# Changelog

All notable changes to this project will be documented in this file.

## [0.1.0] - 2026-03-26

### Bug Fixes

- Use go-version-file in CI instead of hardcoded matrix([e598fee](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/e598feee9d58efcdb745a1ac167484ae505a40f5))
- Use go 1.26.0 + toolchain directive, CI matrix 1.25/1.26, MCP cleanup([1240c8b](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/1240c8bd1938fff13f1a1e78b29bf5f8148e1cf1))

### Documentation

- Add Anthropic trademark acknowledgement to README([051f599](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/051f599b012303dd43a6f2c28287700e43a5212f))

### Features

- Initial Claude Agent SDK for Go (Phase 1)([454474a](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/454474a1319799ae238646209f1e9ededd4cb416))
- Add Phase 2 — ClaudeClient with multi-turn sessions and hooks([c300337](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/c3003373a87be74c1ee9562fef0547ea142ef43c))
- Phase 3 MCP tool registration + CI workflow([eede4ae](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/eede4ae458d82c99d871b458c33ce5dda708abec))
- Phase 4 — auto-cleanup MCP temp files, MCPConfigPath option([ba4b6ed](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/ba4b6ed6ff280126882868113d357c0f75c01623))
- Make SDK production-ready with fixes, constants, and tooling([ee0c44a](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/ee0c44a93c4d909b58a6c3c232f0296635e66620))
- Add CLIPrefixArgs and graceful SIGTERM shutdown([d90c750](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/d90c750687075ae5d5bc07c84c855f9a6c39710f))


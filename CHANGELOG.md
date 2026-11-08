# Changelog

## v0.2.0 — 2026-11-08

### Decision workspace

- Guide setup for primary agents and optional reviewers, with global defaults and project overrides.
- Organize work into Overview, Reviews, Decisions and Activity; keep raw output and audit history in collapsed Diagnostics.
- Show the next action after analysis and automatic review rounds.
- Preserve browser drafts across restarts and distinguish drafts from saved answers and input included in an analysis.
- Let users explicitly reuse saved answers as point rulings without retyping them.
- Compare structured analysis revisions and preview final proposals as locally rendered Markdown.
- Improve keyboard navigation, field labels, and focus preservation.
- Warn when a newer final proposal differs from an approved selection; compare both proposals and identify the approved solution in implementation confirmations, including bulk sends. The user still selects and approves any replacement.

### Agents and usage

- Capture Codex JSONL token and run metrics, including partial usage on failed or cancelled runs; preserve Claude and Cursor usage support. Unreported cost is never estimated.
- Discover Codex model and reasoning choices, and support explicit read-only Codex analysis in non-Git folders.
- Use workspace-write sandboxing for new Codex implementation profiles; existing command settings are unchanged.
- Add an explicit About/update check and documented terminal update commands.

### Build and documentation

- Record the source version in VERSION for make builds.
- Refresh user and agent documentation and add Go/UI regression coverage.
- Bundle the pinned Markdown renderer and its license notices locally.

Account quota display remains deferred.

## v0.1.0 — 2026-10-28

- Initial public source release and verified Codex templates.

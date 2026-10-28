# Decision Ledger

## Problem statement

Developers use Codex, Claude, Gemini and other AI tools to investigate bugs, review designs and propose implementations. Their answers end up scattered across chat windows, terminals and documents, so decisions are hard to revisit:

- Which alternatives were considered, and who suggested each?
- What evidence supported them?
- Where did the agents disagree?
- Who approved the final direction, and why?

Orchestration tools run models and debates. None of them centre a durable, human-owned record of engineering decisions across automated and manual AI interactions.

## Solution

**Decision Ledger** (`dlg`) is a local-first single binary for **one user working with several AI agents**. A primary agent analyses, secondary agents challenge, and **the user decides**. The ledger contains no model: it runs agent CLIs that are already installed and logged in.

| Participant | Role | Connection |
|---|---|---|
| Primary agent (e.g. Codex, Claude Code) | Analyses the problem, keeps an agreement board of the points that matter, folds in reviewer replies each round, writes the final proposal, implements after approval | `dlg` runs its CLI (prompt on stdin) |
| Secondary agents (e.g. Claude, Gemini, Cursor) | Independently review the analysis, point by point, over as many rounds as needed | Copy-paste, a pipe (`dlg handoff … \| claude -p \| dlg review …`), an opt-in runner (`dlg run`), MCP (`dlg mcp`) or HTTP |
| User | Adds context, answers questions, rules on disputed points, selects, approves, triggers implementation | Local web UI (`dlg serve`) |

What it stores, in SQLite under `.decision-ledger/`:

- Problems and nested subproblems
- Competing alternatives per problem
- Every agent output and reviewer reply, verbatim, before parsing
- Evidence quotes with `path:line` sources
- Disagreements, which stay open until the user closes them
- The user's selection (with rationale) and a separate approval
- An audit trail written in the same transaction as each change

## Core flow

1. Create a problem and add notes.
2. **Analyse:** the primary investigates the repo read-only and returns a summary, clarifying questions (with its assumptions), one recommended solution with evidence, other options, open points and an agreement board.
3. Answer its questions; answers become notes.
4. **Hand off** to each reviewer: full context the first time, then only the delta. Each handoff is recorded.
5. **Store replies** verbatim. `dlg parse` reads per-point `[Pn] AGREE | DISAGREE` positions.
6. **Next round:** the primary folds the new replies in and updates the board. *Auto rounds* can repeat steps 4–6 for reviewers with an approved runner, stopping early on questions, a settled board or no change.
7. **Rule** on disputed points. Only a ruling note makes a point `decided`; the agent cannot mark it for you.
8. **Finalise:** the primary writes the consensus proposal, added as an alternative and selected (not if an approval already exists).
9. **Approve**, then **implement**: approved problems go to the primary with write access. Rejected alternatives are listed as out of scope; open disagreements and deferred problems are left out. A clean git workdir can be reverted if you stop the run.

## Package

- **Go**, pure-Go SQLite (no CGO), standard `net/http`, one embedded HTML page with plain JavaScript. Cross-compiles to static binaries for macOS, Linux and Windows (`make dist`); GitHub Actions builds releases on `v*` tags.
- **CLI:** `init`, `serve`, `problems`, `status`, `handoff`, `review`, `note`, `parse`, `run`, `config`, `models`, `mcp`. Read commands take `--json`. Commands use a running `dlg serve` when present, else open SQLite directly.
- **Long runs** (analyse, auto rounds, reviewer runs, finalise, implement) are background jobs with live progress and cancel.
- **Model presets** let the UI pick a model per stage by key; the flags themselves are CLI-only config.

## Safety

- Server binds to `127.0.0.1`, checks the `Host` header (DNS rebinding) and requires `X-Ledger: 1` on writes (CSRF).
- Settings that run commands (`secondary_commands`, `runner_confirmed`, `models`, `model_list_command`) can only be changed from the command line; a runner command must be confirmed once in a terminal.
- Agent output and imports are untrusted: JSON is validated, all of it is escaped in the UI, and imports never carry settings.

## Scope

In: one developer, one primary agent, one or more reviewers, local UI.
Out: multiple users, authentication, hosted collaboration, browser automation, direct provider APIs, background auto-parsing, and any implementation the user did not approve.

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
3. Save each answer or explicitly accept the stated assumption, then re-analyse. Answers become notes linked to the analysis/question and survive failed runs.
4. **Hand off** to each reviewer: full context the first time, then only the delta. Each handoff is recorded.
5. **Store replies** verbatim. `dlg parse` reads per-point `[Pn] AGREE | DISAGREE` positions.
6. **Next round:** the primary folds the new replies in and updates the board. *Auto rounds* can repeat steps 4–6 for reviewers with an approved runner, stopping early on questions, a settled board or no change.
7. **Rule** on disputed points. Only a ruling note makes a point `decided`; the agent cannot mark it for you.
8. **Finalise:** the primary writes the consensus proposal, added as an alternative and selected (not if an approval already exists).
9. **Approve**, then **implement**: approved problems go to the primary with write access. Rejected alternatives are listed as out of scope; open disagreements and deferred problems are left out. A clean git workdir can be reverted if you stop the run.

## Package

- **Go**, pure-Go SQLite (no CGO), standard `net/http`, an embedded HTML page plus JavaScript and CSS. Cross-compiles to static binaries for macOS, Linux and Windows (`make dist`); GitHub Actions builds releases on `v*` tags. Node is needed only for the UI DOM tests (`make test-ui`).
- **CLI:** `init`, `serve`, `problems`, `status`, `cost`, `handoff`, `review`, `note`, `parse`, `run`, `config`, `models`, `analyse`, `mcp`. Read commands take `--json`. Core ledger commands use a running `dlg serve` when present, else open SQLite directly.
- **Long runs** (analyse, auto rounds, reviewer runs, finalise, implement) are background jobs with live progress and cancel.
- **Model presets** let the UI pick a model per stage by key; the flags themselves are CLI-only config.
- **Setup:** no agent is silently selected on init. The UI detects CLIs and provides terminal commands for explicit configuration. Global defaults are inherited unless overridden; reviewer execution approvals stay project-local. Reviewers are optional and several identities can use the same tool.
- **Workspace:** Overview, Reviews, Decisions and Activity. Activity contains a compact Usage summary, expandable usage details, More options for editing alternatives/sub-problems, and collapsed Diagnostics for raw output and audit history. Import/export lives under Data; About offers an explicit update check without installing anything.
- **Approval changes:** a new final proposal with different title/description warns beside the decision and offers a Markdown comparison. Implementation confirmations identify the approved selection, including bulk sends; changing the selection still requires explicit approval.
- **Final proposal preview:** compact Markdown card and a full reading dialog, with original Markdown copying and a ledger-derived settled-point badge. The pinned renderer and license notices are embedded locally; raw HTML and image loading are disabled.
- **Usability:** browser drafts persist across restarts with explicit unsaved/save feedback and recovery; agent completion does not clear unsent input. Overview, Reviews and Activity recommend the next action, including after auto-rounds, without making decisions. Saved answers can explicitly supply point rulings; unconfirmed model-decided points explain why they need attention. Activity compares structured analysis revisions. Workspace tabs support arrow/Home/End navigation; rendering preserves field focus and selection.
- **Usage:** Claude/Cursor result events and Codex JSONL supply metrics where available. Codex tracks tokens, elapsed time, completed turns and tool actions; captured usage survives failures. Missing cost is never estimated. Account quota percentages are separate and not yet shown. New Codex implementation profiles use `--sandbox workspace-write`; existing saved commands remain unchanged.

## Safety

- Server binds to `127.0.0.1`, checks the `Host` header (DNS rebinding) and requires `X-Ledger: 1` on writes (CSRF).
- Settings that run commands (`secondary_commands`, `runner_confirmed`, `models`, `model_list_command`) can only be changed from the command line; a runner command must be confirmed once in a terminal.
- Agent output and imports are untrusted: JSON is validated, all of it is escaped in the UI, and imports never carry settings.

## Scope

In: one developer, one primary agent, optional reviewers, local UI.
Out: multiple users, authentication, hosted collaboration, browser automation, direct provider APIs, background auto-parsing, and any implementation the user did not approve.

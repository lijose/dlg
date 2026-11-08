# Decision Ledger (`dlg`)

Developed by [lijose](https://github.com/lijose). Released under the [MIT License](LICENSE).

A local-first, single-binary ledger of engineering decisions made with multiple AI agents, for **one user**. A primary agent investigates and proposes, secondary agents challenge, and **the user decides**. The record is durable, human-owned and resumable.

- [Problem and idea](#problem-and-idea)
- [Install](#install)
- [Quick start](#quick-start)
- [Concepts](#concepts)
- [Workflow](#workflow)
- [Commands and configuration](#commands-and-configuration)
- [Decision rules](#decision-rules)
- [Data model](#data-model)
- [HTTP API](#http-api)
- [Agent integration](#agent-integration)
- [Security model](#security-model)
- [Import and export](#import-and-export)
- [Architecture and code map](#architecture-and-code-map)
- [Build, test, distribute](#build-test-distribute)
- [Scope and limits](#scope-and-limits)

## Problem and idea

Answers from Codex, Claude, Gemini and similar tools end up scattered across chat windows, terminals and documents. Later it is hard to answer: which alternatives were considered, who suggested each, what evidence backed it, where the agents disagreed, and who approved the direction and why.

Orchestration tools run models and debates. Decision Ledger instead keeps a **workspace you return to**: competing alternatives stay standing, a pasted review is kept raw, only the user can close a disagreement, and the primary agent is asked to implement only after a separate approval. An Architecture Decision Record is the conclusion written afterwards; the ledger keeps the alternatives, the quote, and the disagreement the user has not closed.

The ledger contains **no model**. It runs an agent CLI that is already installed and logged in on your machine.

| Participant | Role | How it connects |
|---|---|---|
| Primary agent (e.g. Codex, Claude Code) | Analyses the problem (summary, questions, sub-problems, solution, open points), folds in reviewer replies each round, writes the final proposal | `dlg` runs its CLI |
| Secondary agents (e.g. Claude, Gemini) | Independently review and challenge the analysis, over as many rounds as needed | You copy the analysis to them (full context first, then only the delta) and paste their replies back |
| User | Adds context and alternatives, resolves disagreements, selects, approves, triggers implementation | Local web UI |

## Install

Once a release is published, download the binary for your machine from the [latest release](https://github.com/lijose/dlg/releases/latest). Until then, build from source using the instructions below.

| OS | File |
|---|---|
| macOS Apple Silicon / Intel | `dlg-darwin-arm64` / `dlg-darwin-amd64` |
| Linux x86-64 / ARM64 | `dlg-linux-amd64` / `dlg-linux-arm64` |
| Windows x86-64 | `dlg-windows-amd64.exe` |

macOS / Linux (example for Apple Silicon):

```sh
shasum -a 256 -c SHA256SUMS --ignore-missing   # optional: verify against the release checksums
chmod +x dlg-darwin-arm64
xattr -d com.apple.quarantine dlg-darwin-arm64 2>/dev/null   # macOS only: the binary is not signed
sudo mv dlg-darwin-arm64 /usr/local/bin/dlg
dlg --version
```

Windows: rename to `dlg.exe` and put it in a folder on your `PATH`.

With Go 1.26+: `go install github.com/lijose/dlg@latest` (installs to `$(go env GOPATH)/bin`).

From source:

```sh
git clone https://github.com/lijose/dlg.git
cd dlg
make build                 # version from VERSION (currently v0.2.0)
./dlg --version
```

This produces `./dlg`; put it on your `PATH` to run it from other projects.
On Windows without Make, run `go build -o dlg.exe .`.

Bug reports and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md).

You also need the agent CLI you want as primary, installed and logged in (e.g. `codex` or `claude`).

## Quick start

```sh
cd your-project
dlg init              # creates .decision-ledger/ here; inherits global defaults
dlg serve             # opens http://127.0.0.1:8765/
```

`dlg serve` again later resumes exactly where you left off, including the problem you had open.

The browser shows a guided first-run screen when no primary has been configured. It detects installed tools, lets you choose a tool and model, and shows the exact terminal commands to configure it. Leave `dlg serve` running and use another terminal for approval. Return to the browser and click **Check setup**. Detection checks whether the CLI is on PATH; it does not verify login or model access. Problems can be saved before agent setup is complete.

The workspace has four destinations: **Overview** (latest proposal, changes, questions, next step and reviewer status), **Reviews** (saved replies and individual reviewer actions), **Decisions** (point rulings, selection, approval and implementation), and **Activity** (previous rounds, cost and the detailed ledger). Saved answers stay beside their questions; saved replies open directly from a reviewer's entry. Diagnostics are collapsed by default. Older tab URLs still work.

Save each answer explicitly, or choose **Use assumption**. A blank field does not accept an assumption. Saving an answer records context. Expand **Also record a point decision (optional)** to explicitly choose points that answer resolves; saving then records point rulings in the same transaction. For earlier answers, expand a point in **Decisions → Needs you → Use a saved answer**, choose the answer, and click **Use answer for Pn**. The full saved answer is shown before you record the ruling. These links are user choices; dlg never guesses them from prose. Use **Re-analyse** after answering to incorporate it into the proposal. Answers remain in the ledger if a later run fails. **Data** in the header contains ledger import and export. **Activity → Usage** shows a compact summary with expandable details. **More options** contains alternatives and sub-problems; **Diagnostics** keeps raw output and the audit trail collapsed by default. These records remain stored and included in exports. Text drafts—including answers, replies, decision rationale, approval notes and alternative edits—survive navigation, reloads and browser restarts on the same browser profile and server address. **Unsaved draft** means the text is kept in browser storage, not yet submitted to the ledger. Explicit saves clear the corresponding draft; failed saves and completed agent runs do not discard unsent input. **Browser drafts** lets you recover earlier input or clear a draft without changing ledger records. Browser drafts are not included in ledger exports; clearing browser data removes them. Private browsing, blocked storage, or a different host/port can limit persistence; a storage failure is shown beside the draft status. Avoid editing the same problem in multiple browser tabs: drafts are not synchronized between tabs.

**Changes since approval:** when a newer final proposal differs in title or description from the approved selection, Decisions shows a warning and **Review changes** opens both versions. Overview, Reviews and Activity recommend reviewing the change. Implementation confirmations—including bulk sends—identify the approved solution that will be used. To adopt the newer proposal, explicitly select it and approve it. Identical regenerated text does not trigger a warning. This checks final proposals created after approval, not changes to analysis prose or edits to the selected alternative itself.

**Final proposal** in Decisions shows a compact formatted preview. **Read full proposal** opens a wide, scrollable Markdown view with headings, lists, tables, quotes and code blocks. Its settled-point badge reflects the saved ledger state. **Copy Markdown** copies the original text unchanged; **Close** or **Esc** returns to the decision controls. Raw HTML is displayed as text, images are not loaded, and clickable links are limited to HTTP(S), email and fragment links. The renderer is bundled locally, so viewing a proposal requires no CDN or internet access.

**Next step** is visible in Overview, Reviews and Activity, including after automatic review rounds. It highlights one recommended action for the current state: answer questions, incorporate saved input, request/check reviews, resolve points, select a proposal, or review approval and implementation. Existing secondary actions and confirmations remain available. **Activity → Compare proposals** compares any two saved analysis rounds, showing added, removed and changed fields side by side (stacked on narrow screens). It compares structured proposals, not raw model output or later user rulings.

Use **Left/Right Arrow**, **Home** and **End** while a workspace tab is focused to switch destinations. **Tab** moves into the active panel and controls; **Esc** closes dialogs. Fields and dialogs have accessible names, focus is visible, and refreshing a view preserves the active field and text selection where it still exists.

For a plain folder, choose **Use this folder without Git** during Codex setup or run `dlg config codex-folder plain` in the project terminal and approve it. Standard `codex exec` analysis then uses a read-only sandbox in the approved working directory. The option is project-local and preserves model settings. Run `dlg config codex-folder git` to restore the Git requirement for standard analysis.

### Configure once, reuse across projects

In **Agents**, choose **Global defaults** or **This project**. Set up a global primary once, in a terminal:

```sh
dlg config --global use claude
```

Read the settings printed and approve them. Built-in model presets then appear in the browser without a separate `init-models` command. Choose a model in Agents and save it as a global default, or choose one for a single run. New projects inherit global settings; project overrides take precedence. Changing the primary with `use` resets its model overrides so one tool's model isn't silently attached to another tool.

Each reviewer has its own name, tool and model. Several roles may use the same tool and model. Commands run in separate processes without resume flags in the built-in presets. For a global automatic reviewer, for example:

```sh
dlg config --global set-runner reviewer-1 'claude -p'
dlg config approve-runners
```

The first command configures the reusable runner and reviewer identity. The second approves inherited runner commands for the current project's files. In another project, only `dlg config approve-runners` is needed; commands and model preferences don't have to be re-entered. CLI approvals are never granted by the browser.

For a manual reviewer, use **Add reviewer → Copy & paste**; no CLI is required. Grok chat and other chat-only tools can be used this way. Custom CLIs require an explicit stdin-compatible command configured in the terminal. Cursor uses the installed `cursor-agent` or `agent` CLI; model discovery comes from that CLI. Codex choices include the installed CLI's model catalog and reasoning options for primary and approved reviewer commands. Configured and built-in presets remain available if discovery fails. Claude choices come from built-in and terminal-configured presets. Custom presets still use `dlg config add-model`.

Global settings are stored in `~/.config/dlg/config.json` (`DLG_CONFIG` overrides the path). Working directories, ledgers and runner approvals remain project-local. Use **Use global primary and models** or **Use global reviewers** to remove a project's overrides. Global changes affect subsequent runs; a running job retains the configuration captured when it started.

### Codex compatibility and usage

The built-in Codex implementation command is `codex exec --sandbox workspace-write`, supported by the checked Codex CLI 0.157.1. Existing saved commands are not rewritten. If an older configuration still uses the rejected `--full-auto` flag, configure the supported commands yourself in a project terminal:

```sh
dlg config set-primary codex 'codex exec' 'codex exec --sandbox workspace-write'
```

Review and approve the displayed settings. `set-primary` resets model overrides, so select your preferred models again afterwards. To configure the same commands as global defaults, add `--global` immediately after `config`. Existing project command overrides still take precedence. See the [official Codex permission options](https://learn.chatgpt.com/docs/non-interactive-mode#permissions-and-safety).

**Codex usage tracking:** for direct `codex exec` / `codex e` commands (including `codex.exe`), dlg enables `--json` and reads the final answer separately from event telemetry. It records input, cached-input, output and reasoning-output tokens when reported, plus elapsed time, completed turns, thread ID and distinct tool-action items (commands, file changes, MCP calls and web searches). Tool counts include started actions even if they fail. Cached-input tokens are part of input tokens; reasoning-output tokens are part of output tokens. They are not added again to the totals. Wrapper commands that hide the Codex executable are not auto-detected.

The compact Usage summary appears in Overview and Activity; expand **Usage details** for totals by agent/model and individual runs. Claude and Cursor usage parsing remains supported. Unreported tokens and dollar cost are labelled as unavailable; no dollar amount is estimated. Usage already received is retained on failure, cancellation or timeout. Interrupted Codex runs are marked partial: unfinished turns may have consumed tokens that were never reported. Older runs are not backfilled. `dlg cost --json` includes `reasoning_output_tokens`, `tool_calls`, `turns`, `token_runs` and `partial_runs` in its aggregates. See the [official Codex output format](https://learn.chatgpt.com/docs/non-interactive-mode#make-output-machine-readable).

Account quota (percentage remaining and reset times) is separate from per-run token usage and is not currently displayed by dlg.

### Check for updates

Open **About → Check for updates** to query the latest stable release from the official `lijose/dlg` GitHub repository. There are no automatic startup checks, background downloads or installs. For a newer compatible release, dlg shows release notes and OS-specific terminal commands to download the binary, check its SHA256 checksum, and copy it to the detected executable location. Downloading is unprivileged; only the final install may require `sudo`. Package-manager paths show a reminder to update using the manager. Development builds are identified separately and aren't declared outdated. The existing release process provides checksums, not signed publisher verification.

## Concepts

| Term | Meaning |
|---|---|
| **Problem** | Something to decide. Can nest: a problem has subproblems. |
| **Alternative** | A candidate solution to a problem, by an agent or the user. |
| **Contribution** | A stored text record: `context` (user note, answer or ruling), `proposal` (raw analysis output), `analysis` (one round's validated JSON), `handoff` (text copied to a secondary), `review` (raw secondary reply), `final`, `implementation`. `brief` and `evaluation` exist only in older ledgers. |
| **Evidence** | A quote plus source (e.g. `path:line`), attached to an alternative or to the problem in general. |
| **Disagreement** | A point a reviewer raised against the proposals. Stays `open` until the user closes it. Carries the primary agent's *recommendation* (accept/reject) and reason, which is advice only. |
| **Selection** | The user's chosen alternative, with a required rationale, or a deferral. |
| **Approval** | A separate record, bound to one selection, that authorises implementation. |
| **Event** | Audit trail entry (who, what, when) written in the same transaction as every change. |

**Problem status** is derived, never stored:

```
open ──select──▶ selected ──approve──▶ approved ──implement──▶ sent
  │                  ▲                      │
  └─defer─▶ deferred │      re-select ──────┘ (approval goes stale → back to selected)
```

- `open`: no selection yet. `deferred`: selected "defer". `selected`: alternative chosen, not approved.
- `approved`: the latest approval matches the latest selection. `sent`: it was sent to the primary agent.

## Workflow

1. **Create a problem** and add notes (context the agents should know).
2. **Analyse**: the primary agent investigates the repo (read-only) and returns a summary, clarifying questions (each with the assumption it made), suggested sub-problems (added only if you click), one recommended solution with quoted evidence, other options, open points, and an **agreement board** of the points that matter.
3. **Answer** its questions inline, save each answer (or explicitly use its assumption), then re-analyse. Answers are stored as notes linked to their analysis and question.
4. **Copy** for each secondary model. The first copy is the full, self-contained context. Later copies are a **delta**: what changed, new notes and rulings, and only the board points that moved, including other reviewers' positions. Only **open and disputed** points are sent in full; agreed, decided and ruled points are listed by key ("Settled, no reply needed"), and a delta lists only those settled since the last copy. A change to the recipient's own position does not count as a change. Each copy is recorded as a `handoff`.
5. **Paste** each reply. It is stored verbatim before any agent sees it.
6. **Process replies**: the next round folds every new reply into the analysis. Each reviewer's position is recorded per point, and points are marked `agreed`, `disputed`, `open` or `decided`. Repeat steps 4–6 as often as you like.
7. **Rule** on a disputed point ("go with X", or your own call). The ruling is a note, and the next round marks the point `decided`.
8. **Generate the final proposal**: the primary agent writes it from the agreed and decided points. It is added as the `consensus` alternative and selected. You can still select another alternative or defer.
9. **Approve** the selection (optional note).
10. **Implement**: send one or more approved problems to the primary agent with write access. Deferred problems and open disagreements are left out; rejected alternatives are listed as out of scope. One approved problem can be sent while the rest of the tree is still open.

## Commands and configuration

```
dlg init                              create a ledger in the current directory
dlg serve [--port N] [--no-browser]   start the UI (default port 8765, bound to 127.0.0.1)
dlg problems [--json]                 list problems
dlg status [id] [--json]              state, reviewers waiting for a reply, new replies, cost
dlg cost [id] [--json]                what the agents' runs cost, per agent and per model
dlg models [--refresh] [--json]       list available model presets
dlg analyse <id> [--model KEY]        run one analysis round
dlg handoff <id> <to> [--json] [--peek]   print the next text for a reviewer (marks it sent unless --peek)
dlg review <id> <author> [--file F]   store a reply (stdin), warnings on stderr
dlg note <id> [text]                  add a note
dlg parse <review-id> [--json]        per-point positions of a stored reply
dlg run <id> <agent> [--yes] [--timeout D] [--model ID]   opt-in runner, see below
dlg config show | use <agent> | set-runner <agent> <command> | unset-runner <agent>
dlg config --global use <agent> | set-runner <identity> <command>
dlg config approve-runners          approve inherited reviewer commands for this project
dlg config codex-folder <git|plain>   require Git or approve plain-folder Codex analysis
dlg mcp                               MCP server over stdio
dlg --version | --help
```

- Read commands take `--json`. Output goes to stdout, diagnostics to stderr; exit status 1 on error, 2 on bad usage.
- Commands use a running `dlg serve` for the same ledger when one answers within 200 ms (address from `.decision-ledger/serve.url`, loopback only), and open SQLite directly otherwise. `DLG_NO_SERVER=1` forces direct access.
- `init` writes `.decision-ledger/ledger.db` and a `.gitignore` (`*`) inside it, so the ledger is not committed by accident. It preserves global inheritance and does not silently choose an installed tool or add reviewers; running it again is a no-op.
- `serve` looks for `.decision-ledger/` in the current directory and then in each parent, so it works from any subfolder of the project.

The **Agents** dialog edits model and reviewer preferences at project or global scope. Executable commands, model definitions and working directories are configured in the terminal. Effective settings combine built-in defaults, global defaults, and project overrides (stored in the `settings` table):

| Key | Default | Purpose |
|---|---|---|
| `primary_name` | `codex` fallback; no primary configured on init | Name recorded as the author of agent output |
| `primary_command` | `codex exec` fallback | Analyse/finalise command; explicitly configure a primary before using the guided UI |
| `implement_command` | `codex exec --sandbox workspace-write` fallback | Implementation command; see the Codex compatibility note above |
| `workdir` | directory of `dlg init` | Working directory for agent runs |
| `secondary_names` | none | Secondary models you copy to and paste from (any name, e.g. `cursor`) |
| `secondary_with_code` | none | Those of them that can open the repository themselves (an IDE such as Cursor): their copy points at `path:line` instead of quoting code |
| `last_problem_id` | none | Where `serve` resumes |
| `secondary_commands` | none | Opt-in runner: reviewer name → command that reads the handoff on stdin. **Command line only** (`dlg config set-runner`); the HTTP API refuses to write it |
| `runner_confirmed` | none | Commands you approved for `dlg run`. Command line only |
| `codex_plain_folder` | none | Canonical project workdir approved for standard read-only Codex analysis without Git; terminal-only and project-local |
| `default_analyse_model`, `default_implement_model` | tool default | Preset keys for the primary's stages; `none` explicitly selects the base command |
| `reviewer_labels` | none | Display labels for manual reviewers, including a user-entered tool/model description |
| `cycle_rounds` | 1 when unset | Number of automatic review rounds per click, from 1 to 3 |
| `review_models` | none | Reviewer → default model id for its runner, picked in the UI from the runner CLI's own list (Cursor CLI discovery or Claude/Codex preset keys). Model IDs must be one argument: no spaces or leading `-` |

The command is split like a POSIX shell (single quotes, double quotes, backslash escapes), run without a shell, and the prompt is passed on **stdin**. Any CLI that reads its prompt from stdin works (`codex exec`, `claude -p`). Passing it on stdin avoids the OS argument-length limit and prompts that start with `-` being read as flags.

## Decision rules

Enforced in `db.go`, not in the UI:

- Only the user resolves a disagreement; the actor on that event is always `user`.
- A board point counts as `decided` only when the user wrote a `Ruling on [Pn]` note. Explicitly linking a saved answer creates such a ruling with its answer reference. An agent's own `decided` without a ruling is treated as open (the UI explains the missing confirmation), and `agreed` is passed to the implementer labelled as the models' claim, not the user's.
- Regenerating the final proposal while the selection is approved adds it as an alternative without selecting it, so the approval does not go stale silently.
- Approval is bound to one selection. Selecting again makes the approval stale.
- Only `selected` problems can be approved; deferred ones cannot.
- The selected alternative cannot be deleted until another is selected or the problem is deferred.
- An alternative can only be referenced by its own problem; unknown alternative ids from an agent are dropped.
- Implementation payloads require `approved` or `sent` status.
- Audit events share the transaction of the change they describe, and outlive deleted problems (no foreign key on `events`).
- Deleting a problem cascades to its subproblems, alternatives, contributions, evidence, disagreements, selections and approvals.

## Data model

SQLite, schema created on open (`CREATE TABLE IF NOT EXISTS`). Timestamps are UTC ISO-8601 text.

| Table | Key columns |
|---|---|
| `settings` | `key`, `value` (JSON) |
| `problems` | `id`, `parent_id`, `title`, `context`, `author`, `created_at` |
| `alternatives` | `id`, `problem_id`, `title`, `description`, `author` |
| `contributions` | `id`, `problem_id`, `author`, `kind`, `body`, `meta?` (JSON: runner details, parsed reply) |
| `evidence` | `id`, `problem_id`, `alternative_id?`, `contribution_id?`, `quote`, `source`, `author` |
| `disagreements` | `id`, `problem_id`, `alternative_id?`, `contribution_id?`, `raised_by`, `summary`, `quote`, `recommendation?`, `recommendation_reason`, `status` (open/resolved/deferred), `resolution` (accept/reject/own/defer), `resolution_note`, `resolved_alternative_id`, `resolved_at` |
| `selections` | `id`, `problem_id`, `alternative_id?` (NULL = deferred), `rationale` |
| `approvals` | `id`, `problem_id`, `selection_id`, `note`, `sent_at?` |
| `events` | `id`, `ts`, `actor`, `action`, `problem_id?`, `detail` (JSON text) |

Common event actions: `create_problem`, `update_problem`, `delete_problem`, `add_alternative`, `update_alternative`, `delete_alternative`, `add_context|proposal|analysis|handoff|review|final`, `add_evidence`, `add_disagreement`, `resolve_disagreement`, `select`, `defer`, `approve`, `run_<op>`, `<op>_failed`, `send_for_implementation`, `update_settings`, `import`.

## HTTP API

JSON over HTTP, used by the embedded UI. Errors are `{"detail": "..."}`.

| Method and path | Purpose |
|---|---|
| `GET /` | The UI |
| `GET /api/setup` | Agent detection, configuration scope and global defaults |
| `GET /api/agent-models` | Scoped model choices; reviewer discovery requires project approval |
| `PUT /api/agent-preferences` | Validated model/reviewer preferences for a project or global defaults; never commands |
| `POST /api/settings/inherit` | Remove project overrides for the primary or reviewers |
| `POST /api/problems/{pid}/answers` | Save `analysis_id`, zero-based `index`, and `answer` (or `use_assumption: true`); optional user-selected `point_keys` also records rulings atomically; rejects stale questions and invalid points |
| `POST /api/problems/{pid}/answer-rulings` | Record an existing saved answer as a ruling: current `analysis_id`, `answer_id`, user-selected `point_keys`; rejects stale analyses, replaced answers and answers from other problems |
| `GET /api/about` / `GET /api/updates` | Local build information / stable release check; only `/api/updates` requires `X-Ledger`; `?refresh=1` bypasses its 24-hour cache |
| `GET /api/state` | Settings and the problem list (id, parent, title, status, open disagreement count, `needs_you` for open and selected problems) |
| `PUT /api/settings` | Update settings (unknown keys or wrong types are rejected) |
| `POST /api/problems` | Create (`title`, `context`, `parent_id`) |
| `GET` / `PATCH` / `DELETE /api/problems/{pid}` | Full detail (including `approval_change` when a newer final proposal differs from the approved selection; `handoffs`: per secondary, its state and the full or delta text to copy next; `cost`: totals per agent and model) / edit / delete |
| `POST /api/problems/{pid}/alternatives` | Add alternative |
| `PATCH` / `DELETE /api/alternatives/{aid}` | Edit / delete alternative |
| `POST /api/problems/{pid}/notes` | Add user note |
| `POST /api/problems/{pid}/evidence` | Add evidence |
| `POST /api/problems/{pid}/analyse` | Run the primary agent for the next round, folding in the replies pasted since the last one |
| `POST /api/problems/{pid}/cycle` | Start automatic review rounds: `rounds` (1–3), optional primary `model`; requires an approved reviewer runner |
| `POST /api/problems/{pid}/handoffs` | Record that the next handoff text for `to` was copied |
| `POST /api/problems/{pid}/reviews` | Store a pasted reply (`author`, `body`, optional `meta`); returns `id` and format `warnings` |
| `GET /api/problems/{pid}/status` | Round, board counts, reviewers waiting, replies and `unanswered_questions` (also in `dlg status --json`) |
| `GET /api/problems/{pid}/handoffs/{to}` | The next handoff text for a reviewer, without recording it |
| `POST /api/reviews/{rid}/parse` | Extract per-point positions from a stored reply |
| `POST /api/problems/{pid}/finalise` | Start a job: the primary agent writes the final proposal, added as the `consensus` alternative and selected (unless the current selection is approved). Returns `job_id` |
| `POST /api/problems/{pid}/runs/{agent}` | Start a review job with the reviewer's approved runner; optional `model` (an id from its list, `none` for the runner's default) |
| `GET /api/runners/{agent}/models` | The reviewer's models from its runner CLI (`?refresh=1`); runs a command, so it needs `X-Ledger` |
| `GET /api/jobs/{jid}`, `POST /api/jobs/{jid}/cancel` | Poll or stop an analyse, cycle, review, finalise or implement job; includes `progress` (auto-round position) and `cost` of its runs |
| `POST /api/disagreements/{did}/resolve` | `resolution` accept/reject/own/defer, `note`, and `title`/`description` for own |
| `POST /api/problems/{pid}/select` | `alternative_id` (null defers), `rationale` |
| `POST /api/problems/{pid}/approve` | `note` |
| `POST /api/implement` | `problem_ids` to send for implementation |
| `GET /api/export` / `POST /api/import` | Portable JSON |

Status codes: `400` rule violation or bad input, `403` guard rejection, `502` agent failure, `500` unexpected (e.g. database) error.

## Agent integration

- **For agents:** [AGENTS.md](AGENTS.md) and [llms.txt](llms.txt) give the reply format and a recipe per agent kind: CLI pipe (`dlg handoff 3 gemini | pbcopy`), MCP (`dlg mcp`), HTTP API with the `X-Ledger` header.
- **Opt-in runner:** `dlg config set-runner claude 'claude -p'`, then `dlg run 3 claude` pipes the handoff to the command's stdin and stores its stdout raw as a `review`. Safeguards: the command is set only from the command line, you confirm it the first time, a 10-minute timeout (`--timeout`), a 1 MiB output cap (the reply is flagged truncated), and empty output or a non-zero exit stores nothing. `run_id`, `command`, `exit_code`, `duration_ms` and `truncated` are kept in the review's `meta` column. Chat-only models stay on copy-paste.
- **Reviewer models:** Claude runners use built-in and configured presets; Codex runners also discover choices from the installed CLI catalog. For a cursor-agent runner, the **Reviews** destination lists `cursor-agent --list-models` (cached 5 minutes) as model, context and reasoning choices. The run appends `--model <id>` to the approved command; the id must be in that list (a saved default also runs when the list cannot be read) and the command itself never comes from the web. A runner whose command already has `--model` or `-m` keeps it. Other runners have no automatic model discovery: configure model arguments in their terminal-approved commands. Only locally approved reviewers are queried for model lists.
- **Cost and usage:** when an agent prints supported stream-json events (dlg adds the flags for `claude -p` and `cursor-agent -p`), reported cost, tokens, cache, duration, turns, models and session id are stored with the output (`meta.usage`) and as a `usage` audit event. The selected model is stored alongside any model the agent reports. Totals come from these events; an implementation run sent for several problems is split equally. Nothing is estimated: absent cost is shown as "no cost reported", never $0. Find totals under **Activity → Usage** or run `dlg cost`; round summaries, saved replies and the job panel also show available usage. Codex JSONL events supply token and run metrics as described above. Captured usage survives failures, cancellation and timeouts; unreported usage cannot be recovered, so totals are not a complete billing record.
- **Parsing is manual:** `dlg parse` (or the UI's *Parse positions* button) reads `[P1] AGREE|DISAGREE: why` lines into the review's `meta`; the UI previews them on the board until the next analysis round. Nothing parses in the background.
- **MCP:** `dlg mcp` serves `dlg_problems`, `dlg_status`, `dlg_handoff`, `dlg_review`, `dlg_note`, `dlg_parse` over stdio (JSON-RPC, protocol versions 2025-06-18, 2025-03-26, 2024-11-05).
- **Run:** the configured primary command receives the prompt on stdin, in `workdir`, with a 30-minute timeout. Primary failures retain the last 2000 characters of stderr (or stdout) and are logged as `<op>_failed`. Synchronous failures return `502`; background job failures appear in the job's `state` and `error` after the start request has returned `job_id`. Primary runs are logged as `run_<op>` with the agent and command; reviewers use `run_secondary` events and a 10-minute default timeout.
- **Raw output is kept first.** Analysis and final outputs are stored as contributions before parsing, so a parse failure never loses the answer.
- **Structured output is untrusted.** The agent must end with one fenced `json` block. The last valid block is used (falling back to the outermost `{...}`), and it is validated: a summary and a solution title are required, evidence needs quotes, and board points need unique keys and a status in `agreed|disputed|open|decided`.
- **Handoff text** (`agent.go`, `fullHandoff`/`deltaHandoff`) is built on the server, so a later direct integration with another model's API can send the same text and store the answer as a `review` without changing the flow.
- **Prompts** (`agent.go`): analyse (read-only, self-contained so it can be copied, previous round and verbatim replies included, evidence as `path:line`), final (from agreed and decided points), implement ("implement exactly these and nothing else", rejected alternatives listed out of scope, stop if infeasible).

## Security model

The ledger can run shell commands and edit files through the agent, so the local server is locked down:

- Binds to `127.0.0.1` only.
- The `Host` header must be `127.0.0.1`, `localhost` or `[::1]`. This blocks DNS rebinding.
- Every non-GET request needs `X-Ledger: 1`. A cross-site page cannot send it without a CORS preflight, which the server never approves. This blocks CSRF. The model-list endpoints (`/api/models`, `/api/runners/{agent}/models`, `/api/agent-models`) and `/api/updates` need the header too, because they can run discovery commands or contact the release service.
- Agent and imported text is escaped everywhere in the UI, including inside `onclick`/`ontoggle` handlers, and board point keys must be short safe tokens (`P1`, `auth.timeout`).
- Cancel-and-revert of an implementation only touches `workdir`, not the whole git repository.
- Executable settings and runner approvals cannot be written over HTTP. The UI can start previously configured commands, select validated model keys, and inherit terminal-configured global profiles. `dlg run` executes the configured command without a shell, in `workdir`. The header guard is not authentication against local programs or injected same-origin scripts; see [AGENTS.md](AGENTS.md) for residual risks.
- Imports never carry settings, so an imported file cannot change which command runs. Imported column names are filtered against the real schema before any SQL is built.
- There is no authentication: it is a single-user local tool. Do not expose the port.

## Import and export

`GET /api/export` returns `{"format": 1, "exported_at": ..., "tables": {...}}` for all data tables (not settings). `POST /api/import` accepts the same file and requires an **empty** ledger. Use it to move a ledger between machines or share a decision record. The `.decision-ledger/` folder itself is also portable across operating systems.

## Architecture and code map

```
main.go          init, serve, ledger discovery, browser launch, version
cli.go           CLI commands, HTTP-or-SQLite transport choice
core.go          operations shared by CLI, MCP and HTTP (status, handoff, review, parse) and the two Core transports
runner.go        `dlg run`: opt-in secondary runner with safeguards
mcp.go           `dlg mcp` stdio server
app.go           HTTP routes, host/CSRF guard, rounds, handoffs, orchestration of analyse/finalise/implement
cycle.go         auto rounds: run approved reviewer runners, then the primary, up to N rounds
jobs.go          in-memory background jobs (progress, cancel, revert)
stream.go        readable progress from stream-json agent output
agent.go         agent runner, command splitting, JSON parsing + validation, prompts
models.go, model_templates.go, cli_models.go   built-in choices and terminal-configured model presets
codex_models.go, reviewer_models.go   CLI model discovery and reviewer model selection
codex_folder.go  project-local plain-folder checks for standard Codex analysis
setup.go         scoped agent preferences and setup detection
answers.go       saved answers and explicit answer-to-point rulings
approval_changes.go  detect newer final proposals that differ from the approved selection
updates.go       manual release checks and update instructions
globalconfig.go  per-user defaults layered under the ledger's settings
db.go            schema, transactions, all ledger rules, import/export
static/index.html, static/workspace.js, static/workspace.css   embedded single-page UI
*_test.go        ledger, workspace, Codex discovery and plain-folder tests (the test binary doubles as a fake agent)
codex_usage_test.go  Codex telemetry and failure/cancellation regression tests
ui/              Node-based DOM tests; not required to build or run dlg
Makefile         build, test, dist
```

- **Storage:** `modernc.org/sqlite`, pure Go (no CGO), one connection, foreign keys on, 5 s busy timeout. This is what makes the binary static and cross-compilable.
- **Transactions:** every write goes through one helper; the first failed statement poisons the transaction and everything rolls back, audit event included.
- **Web:** standard library `net/http` with method+path patterns; no framework.
- **Rows** are generic maps so the JSON shape of the API is exactly the table columns; request and agent-output shapes are typed structs.

## Build, test, distribute

```sh
make build    # ./dlg for this machine
make test     # go test ./...
make test-ui  # install locked Node test dependencies and run DOM checks
make dist     # static binaries in dist/
```

**Versioning:** `VERSION` records the source version (`v0.2.0`); `make build` and `make dist` use it unless `VERSION` is supplied explicitly. Plain `go build` remains a development build, and tagged `go install` builds use their module version. See [CHANGELOG.md](CHANGELOG.md) for changes.

**Releasing:** after separate release approval, push a matching tag such as `v0.2.0`. `.github/workflows/release.yml` runs the tests with `DLG_RELEASE=1` (including the agent-template verification gate), builds every binary with that version, writes `SHA256SUMS`, and publishes a GitHub release. All template flags must be verified before this gate passes. CI (`ci.yml`) runs `go vet` and `go test` on Linux, macOS and Windows for every push to `main` and every pull request.

Codex template syntax was checked with Codex CLI 0.157.1. On 2026-10-28, a read-only smoke test with `-m gpt-6-luna -c model_reasoning_effort=low -c model_context_window=200000` completed successfully. This is the seeded Codex preset; model access depends on the user's account. The previous `gpt-5` seed was rejected by the tested ChatGPT account. Existing saved presets are not migrated automatically.

The Codex context setting overrides local context accounting; it does not increase a model's actual capacity. The unverified Codex `1m` template is omitted. Omit `--context` when adding a preset to use the CLI's model defaults, or supply raw CLI arguments after `--` for other supported models and settings.

| Target OS | File in `dist/` |
|---|---|
| macOS Apple Silicon / Intel | `dlg-darwin-arm64` / `dlg-darwin-amd64` |
| Linux x86-64 / ARM64 | `dlg-linux-amd64` / `dlg-linux-arm64` |
| Windows x86-64 | `dlg-windows-amd64.exe` |

Copy the file for the target machine; nothing else needs installing except the agent CLI you configure. Notes: `chmod +x` on Linux; on macOS a copied unsigned binary may need `xattr -d com.apple.quarantine <file>`. Cross-compilation alone does not verify execution on the target platform.

Building requires **Go 1.26** (a dependency needs it; Go 1.21+ downloads the right toolchain automatically).

Tests cover the ledger rules (stale approval, deferral, payload contents, closed disagreements, import safety), settings validation, command splitting, agent JSON parsing, and a full analyse → handoff → reply → re-analyse (delta handoff) → finalise → approve → implement run against a fake agent, including the host and header guards. UI DOM interaction tests run with `npm ci --prefix ui` and `npm test --prefix ui`. They cover rendering and interactions, not visual layout in a real browser. Production builds remain a single Go binary and do not require Node.

## Scope and limits

In scope: one developer, one primary agent, optional reviewers (manual or approved CLI runners), local UI.

Deliberately out: multiple users, authentication, hosted collaboration, browser automation, direct provider APIs, editing runner commands from the web UI, background auto-parsing of replies, and any implementation the user did not request after approval.

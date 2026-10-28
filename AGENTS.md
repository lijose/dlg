# Using the decision ledger from an agent

`dlg` keeps a ledger of engineering decisions. A primary agent analyses a problem, reviewers (other models) challenge it, the user decides. You are most likely a **reviewer**: get the text, answer in the format below, store the reply.

Run all commands inside the project (the directory containing `.decision-ledger/`, or a subdirectory). Install: a release binary from GitHub, or `make build` from source; check with `dlg --version`.

## The loop

```sh
dlg problems --json                  # find the problem id
dlg status 3 --json                  # who is waiting, which replies are new
dlg handoff 3 gemini                 # text to review (stdout); marks it sent. --peek to just read
dlg review 3 gemini < reply.md       # store the reply verbatim; warnings go to stderr
dlg parse 12 --json                  # review id -> per-point positions
dlg note 3 "Q: how big? A: 2M rows"  # add context
```

Without `--json` output is plain text; with it, one JSON document. Errors go to stderr, exit status 1 (2 for bad usage).

## Reply format

The handoff ends with this. Keep to it so `dlg parse` can read your answer:

```
SUMMARY: your view in 2-3 lines
SOLUTION: the approach you recommend (say if it matches mine)
[P1] AGREE | DISAGREE: why   (one line per point for review)
NEW: important points I missed (optional)
OPEN: questions you need answered (optional)
```

Rules: one `[Pn]` line per point under "Points for your review", using its key (settled points are only listed; reply to one only to reopen it); a reason after the colon; do not reply with the JSON of the analysis; do not change files unless asked.

## Recipes by agent kind

**CLI agent (shell access), pipe.** One command per step:

```sh
dlg handoff 3 claude | claude -p > reply.md && dlg review 3 claude < reply.md
```

**CLI agent, runner (opt-in).** The user sets this up once, on the command line only (never from the web UI):

```sh
dlg config set-runner claude 'claude -p'
dlg run 3 claude        # pipes the handoff to stdin, stores stdout as the review
```

`dlg run` asks for confirmation the first time a command is used, times out after 10 minutes (`--timeout`), keeps at most 1 MiB of output (the review is flagged truncated) and refuses empty output. The run id, command, exit code, duration and truncation are stored with the review.

**IDE or desktop agent with MCP.** Add a stdio server to the client's MCP config:

```json
{ "mcpServers": { "dlg": { "command": "dlg", "args": ["mcp"], "cwd": "/path/to/project" } } }
```

Tools: `dlg_problems`, `dlg_status`, `dlg_handoff` (`peek` to not mark sent), `dlg_review`, `dlg_note`, `dlg_parse`. A failing tool returns `isError: true` with the message.

**Raw HTTP / scripts.** With `dlg serve` running (default `http://127.0.0.1:8765`; the port is in `.decision-ledger/serve.url`). Writes need the `X-Ledger: 1` header; the `Host` must be local.

```sh
curl -s http://127.0.0.1:8765/api/problems/3/handoffs/gemini            # {"to","state","mode","text",...}
curl -s -X POST -H 'X-Ledger: 1' -H 'Content-Type: application/json' \
  -d '{"author":"gemini","body":"SUMMARY: ..."}' http://127.0.0.1:8765/api/problems/3/reviews   # {"id","warnings"}
curl -s -X POST -H 'X-Ledger: 1' http://127.0.0.1:8765/api/reviews/12/parse
```

**Chat-only model (no tools).** Copy-paste: `dlg handoff 3 gemini | pbcopy` (or the Copy button in the UI), paste the model's answer back with `dlg review 3 gemini` or the UI.

## What you must not do

- Do not resolve, select or approve anything: only the user decides. There is no command for it.
- Do not edit `.decision-ledger/` by hand. Settings that run commands (`secondary_commands`, `primary_command`, `implement_command`, and `workdir`, where they run) are changed with `dlg config` by the user, not by you.

## Agent commands and their residual risks (v1)

Agent commands are set only in a terminal: `dlg config use <agent>`, `set-primary <name> <command> [implement]`, `set-workdir <dir>`, `set-runner <agent> [command]`. Each prints what it will store (quoted) and asks `Allow? [y/N]` on a terminal, even with `--yes`; a y on `set-runner` also approves the runner. `PUT /api/settings` refuses these keys, and the Settings tab shows them read-only with lines to copy. Remaining risks, closed in v2 by a session token and a strict CSP:

- Local programs that send `X-Ledger: 1`, and script injected into the page, can start commands the user already approved, but cannot choose or approve one:
  - reviewer runners (`POST /api/problems/{pid}/runs/{agent}`);
  - auto rounds (`POST /api/problems/{pid}/cycle`): up to 3 rounds of approved reviewers plus `primary_command`, with the primary first when the problem has no analysis yet; never implement;
  - implement (`POST /api/implement`), with only the browser's confirm dialog in the way;
  - `GET /api/models`, which may run the model list command.
- In auto rounds, reviewer replies reach the primary without the user reading them, so one model can inject prompts into another. The primary is read-only, so the damage is limited to the analysis text and what is sent to the vendor.
- Injected script can swap the text the Settings tab's Copy button copies. Read the pasted line before Enter, and dlg's echo before typing y.
- `dlg run --yes` skips its prompt even on a terminal.
- In the setters only a leading `--yes` (right after the subcommand) is the flag. A trailing one becomes part of the command, and the echo shows it.
- Global setters (`dlg config --global ...`) store a quoted command without an echo or prompt. With 3 arguments, `--global set-primary <name> <command>` also sets `implement_command` to the same command. The web never offers global lines.
- `dlg serve` binds to 127.0.0.1:8765 (`--port` to change). Never run dlg as root.

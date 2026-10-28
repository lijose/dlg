# Contributing to Decision Ledger

Bug reports, documentation improvements and pull requests are welcome.
For larger changes, open an issue first to discuss the problem and proposed approach.

## Development

Install Go 1.26 or newer, clone this repository, then run:

```sh
make build
go test ./...
go vet ./...
```

On Windows without Make, use `go build -o dlg.exe .`.
The web UI is embedded from `static/index.html`; rebuild after changing it.
See the README for the architecture and AGENTS.md for agent integration.

Keep pull requests focused, format Go changes with `gofmt`, and add tests for
behavior changes. Describe the problem, the change and how you verified it.
For UI changes, include screenshots when helpful.

## Bug reports

Include your operating system, `dlg --version`, steps to reproduce, expected
behavior and actual behavior. Remove private prompts, credentials and project
details from logs. Do not attach your `.decision-ledger/` directory; it contains
your decision history and local settings.

Contributions are distributed under the repository's MIT license.

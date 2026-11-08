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
The web UI is embedded from `static/index.html`, `static/workspace.js` and
`static/workspace.css`; rebuild after changing any of them. There is no frontend
production build step and the distributed binary does not require Node.

For UI changes, install Node.js and run the DOM interaction checks:

```sh
make test-ui
```

This runs `npm ci --prefix ui` followed by `npm test --prefix ui`. These checks
exercise rendering, scoped persistent drafts and storage failure, workflow recommendations, saved-answer ruling controls,
structured revision comparison, accessible labels and keyboard tab activation; they do not validate browser layout, focus,
scrolling or responsive behavior. Check those in a browser at desktop and narrow
widths. CI currently runs Go vet, tests and build; run the UI checks separately.
See the README for the architecture and AGENTS.md for agent integration.

Keep pull requests focused, format Go changes with `gofmt`, and add tests for
behavior changes. Describe the problem, the change and how you verified it.
For UI changes, include screenshots when helpful.

Update the README, project summary, agent guides, CLI help and UI help when the
documented behavior changes. Follow the delivery and approval rule in AGENTS.md:
obtain approval for the specific implementation change and separate approval
after user testing before committing or releasing.

## Bug reports

Include your operating system, `dlg --version`, steps to reproduce, expected
behavior and actual behavior. Remove private prompts, credentials and project
details from logs. Do not attach your `.decision-ledger/` directory; it contains
your decision history and local settings.

Contributions are distributed under the repository's MIT license.

The final-proposal preview uses a pinned, locally bundled `markdown-it` browser asset.
After deliberately updating its version in `ui/package.json` and the lockfile, run
`npm run vendor:markdown --prefix ui` to regenerate `static/vendor/markdown-it.min.js`
and its combined license notice. The UI tests check bundle consistency, Markdown
formatting, inert HTML and unsafe links, and preservation of the original copy text.
Keep raw HTML and image loading disabled; no CDN is used at runtime.

`VERSION` is the source version used by `make build` and `make dist`; pass
`VERSION=...` to override it for a deliberate release build. A version change and
GitHub commit do not themselves create a release tag.
Approval-change tests cover audit ordering, identical proposals, selection preservation
and implementation confirmations; do not make a warning select or approve a proposal.

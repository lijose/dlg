// Command-line interface for scripts and agents: output goes to stdout, diagnostics to
// stderr, and every read command takes --json. Commands talk to a running `dlg serve`
// when there is one, and open the SQLite file directly otherwise.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	serveFile    = "serve.url" // written by `dlg serve` inside the ledger directory
	probeTimeout = 200 * time.Millisecond
)

// usageError is a mistake in how dlg was called (exit status 2).
type usageError string

func (e usageError) Error() string { return string(e) }

const helpText = `usage: dlg <command> [flags]

  init                              create a ledger in the current directory
  serve [--port N] [--no-browser]   start the local UI
  problems [--json]                 list problems
  status [id] [--json]              where a problem stands and who it is waiting for
  cost [id] [--json]                what the agents' runs cost, per agent and model
  handoff <id> <to> [--json] [--peek]
                                    print the next text for a reviewer (marks it sent unless --peek)
  review <id> <author> [--file F]   store a reviewer's reply, read from stdin or F
  note <id> [text]                  add a note (text or stdin)
  parse <review-id> [--json]        extract per-point positions from a stored reply
  run <id> <agent> [--yes] [--timeout D]
                                    pipe the handoff to the agent's configured command, store its stdout as a review
  config show | use <agent> | set-primary <name> <command> [implement-command]
         set-workdir <dir> | set-runner <agent> [command] | unset-runner <agent>
                                    agent commands are set only here: dlg prints what it will store
                                    and asks Allow? [y/N] (--yes right after the subcommand when not on a terminal)
         add-model <key> <label> <context-label> [args...] | remove-model <key>
         set-model-list <command> | unset-model-list
         set-default-model <analyse|implement> <key|none> | init-models [claude|codex]
                                    model presets, e.g.
                                    dlg config add-model fast "Fast" "200k" --model opus --context 1m --effort high
                                    raw arguments go after --: add-model fast "Fast" "200k" -- --model my-model-id
                                    (list command prints key<TAB>label<TAB>context<TAB>args per line)
  models [--refresh] [--json]       list model presets
  analyse <id> [--model KEY]        run one analysis round with a preset
  mcp                               serve the same operations over MCP (stdio)
  --version, --help

Pipe example:  dlg handoff 3 gemini | pbcopy
`

func dispatch(args []string) error {
	if len(args) == 0 {
		return usageError(strings.TrimRight(helpText, "\n"))
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "--version", "-version", "version":
		fmt.Println("dlg", version)
	case "--help", "-h", "-help", "help":
		fmt.Print(helpText)
	case "init":
		err = initLedger()
	case "serve":
		err = serve(rest)
	case "problems":
		err = cmdProblems(rest)
	case "status":
		err = cmdStatus(rest)
	case "cost":
		err = cmdCost(rest)
	case "handoff":
		err = cmdHandoff(rest)
	case "review":
		err = cmdReview(rest)
	case "note":
		err = cmdNote(rest)
	case "parse":
		err = cmdParse(rest)
	case "run":
		err = cmdRun(rest)
	case "config":
		err = cmdConfig(rest)
	case "models":
		err = cmdModels(rest)
	case "analyse":
		err = cmdAnalyse(rest)
	case "mcp":
		err = withCore(func(c Core) error { return serveMCP(os.Stdin, os.Stdout, c) })
	default:
		err = usageError("unknown command " + cmd + "\n\n" + strings.TrimRight(helpText, "\n"))
	}
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

// -- plumbing ----------------------------------------------------------------

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// parseArgs accepts flags before, between and after positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
}

func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil {
		return 0, usageError(fmt.Sprintf("%q is not a numeric id", s))
	}
	return id, nil
}

func ledgerPath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	path := findLedger(cwd)
	if path == "" {
		return "", fmt.Errorf("no %s/ found here or in a parent directory. Run `dlg init` first", ledgerDir)
	}
	return path, nil
}

func sameFile(a, b string) bool {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return a != "" && resolve(a) == resolve(b)
}

// probeRemote returns a client for a `dlg serve` that serves this very ledger, else nil.
// The address comes from the ledger directory and must be loopback: a ledger copied from
// elsewhere must not be able to point the CLI at another machine.
func probeRemote(path string) *remoteCore {
	if os.Getenv("DLG_NO_SERVER") != "" {
		return nil
	}
	base := "http://127.0.0.1:8765"
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(path), serveFile)); err == nil && strings.TrimSpace(string(b)) != "" {
		base = strings.TrimRight(strings.TrimSpace(string(b)), "/")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || !localHosts[u.Hostname()] {
		return nil
	}
	rc := remoteCore{base: base, client: &http.Client{Timeout: probeTimeout}}
	var st struct {
		Ledger string `json:"ledger"`
	}
	if rc.call("GET", "/api/state", nil, &st) != nil || !sameFile(st.Ledger, path) {
		return nil
	}
	rc.client.Timeout = 30 * time.Second
	return &rc
}

// openCore connects to a running server for this ledger, or opens the database directly.
func openCore() (Core, func(), error) {
	path, err := ledgerPath()
	if err != nil {
		return nil, nil, err
	}
	if rc := probeRemote(path); rc != nil {
		return rc, func() {}, nil
	}
	l, err := openLedger(path)
	if err != nil {
		return nil, nil, err
	}
	return localCore{l}, func() { l.close() }, nil
}

func withCore(fn func(c Core) error) error {
	c, done, err := openCore()
	if err != nil {
		return err
	}
	defer done()
	return fn(c)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// readBody returns the text from a file, or from stdin when no file is given.
func readBody(file string) (string, error) {
	var b []byte
	var err error
	switch {
	case file != "" && file != "-":
		b, err = os.ReadFile(file)
	case file == "" && isTerminal(os.Stdin):
		return "", usageError("no input: pipe the text in or pass --file")
	default:
		b, err = io.ReadAll(os.Stdin)
	}
	return string(b), err
}

func warn(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
}

// -- commands ----------------------------------------------------------------

func cmdProblems(args []string) error {
	fs := newFlags("problems")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	return withCore(func(c Core) error {
		problems, err := c.Problems()
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(problems)
		}
		for _, p := range problems {
			parent := ""
			if p.ParentID != nil {
				parent = fmt.Sprintf(" (in #%d)", *p.ParentID)
			}
			fmt.Printf("#%d\t%s\t%s%s\n", p.ID, p.Status, p.Title, parent)
		}
		return nil
	})
}

// problemStatusArg loads the problem named by pos, or the one last opened in the UI.
func problemStatusArg(c Core, cmd string, pos []string) (*StatusOut, error) {
	switch len(pos) {
	case 1:
		pid, err := parseID(pos[0])
		if err != nil {
			return nil, err
		}
		return c.Status(pid)
	case 0:
		cfg, err := c.Settings()
		if err != nil {
			return nil, err
		}
		if cfg.LastProblemID == nil {
			return nil, usageError(fmt.Sprintf("usage: dlg %s <id> (no problem was opened in the UI yet)", cmd))
		}
		return c.Status(*cfg.LastProblemID)
	}
	return nil, usageError(fmt.Sprintf("usage: dlg %s [id] [--json]", cmd))
}

// costText is "$1.23", with the runs that reported no cost called out rather than counted as free.
func costText(l CostLine) string {
	if l.Priced == 0 {
		return "no cost reported"
	}
	s := fmt.Sprintf("$%.2f", l.CostUSD)
	if n := l.Runs - l.Priced; n > 0 {
		s += fmt.Sprintf(" (+%d run(s) without a cost)", n)
	}
	return s
}

func cmdCost(args []string) error {
	fs := newFlags("cost")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	return withCore(func(c Core) error {
		s, err := problemStatusArg(c, "cost", pos)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(s.Cost)
		}
		if s.Cost == nil {
			fmt.Printf("#%d %s: no run reported usage yet\n", s.ID, s.Title)
			return nil
		}
		fmt.Printf("#%d %s\n", s.ID, s.Title)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		row := func(l CostLine) {
			fmt.Fprintf(w, "  %s\t%d run(s)\t%s\t%d in / %d out\t%d cache read\t%s\n", l.Name, l.Runs, costText(l),
				l.InputTokens, l.OutputTokens, l.CacheRead, time.Duration(l.DurationMS)*time.Millisecond)
		}
		fmt.Fprintln(w, "by agent:")
		for _, l := range s.Cost.ByAgent {
			row(l)
		}
		fmt.Fprintln(w, "by model:")
		for _, l := range s.Cost.ByModel {
			row(l)
		}
		total := s.Cost.Total
		total.Name = "total"
		row(total)
		return w.Flush()
	})
}

func cmdStatus(args []string) error {
	fs := newFlags("status")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	return withCore(func(c Core) error {
		s, err := problemStatusArg(c, "status", pos)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(s)
		}
		fmt.Printf("#%d %s [%s]  round %d\n", s.ID, s.Title, s.Status, s.Round)
		if s.Cost != nil {
			fmt.Printf("cost: %s over %d run(s)\n", costText(s.Cost.Total), s.Cost.Total.Runs)
		}
		if s.Points > 0 {
			fmt.Printf("board: %d points, %d settled, %d disputed\n", s.Points, s.Settled, s.Disputed)
		}
		for _, r := range s.Reviewers {
			fmt.Printf("  %s: %s\n", r.To, r.State)
		}
		if len(s.Waiting) > 0 {
			fmt.Println("waiting for reply from:", strings.Join(s.Waiting, ", "))
		}
		if len(s.ToSend) > 0 {
			fmt.Println("to send to:", strings.Join(s.ToSend, ", "))
		}
		if s.NewReplies > 0 {
			fmt.Printf("%d new reply(ies) not yet in an analysis\n", s.NewReplies)
		}
		for _, r := range s.Replies {
			line := fmt.Sprintf("  review %d by %s", r.ID, r.Author)
			if r.New {
				line += " (new)"
			}
			if r.Parsed {
				line += " (parsed)"
			}
			fmt.Println(line)
		}
		return nil
	})
}

func cmdHandoff(args []string) error {
	fs := newFlags("handoff")
	asJSON := fs.Bool("json", false, "print JSON instead of the bare text")
	peek := fs.Bool("peek", false, "do not mark the handoff as sent")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageError("usage: dlg handoff <id> <to> [--json] [--peek]")
	}
	pid, err := parseID(pos[0])
	if err != nil {
		return err
	}
	return withCore(func(c Core) error {
		h, err := c.Handoff(pid, pos[1], !*peek)
		if err != nil {
			return err
		}
		if strings.TrimSpace(h.Text) == "" {
			return fmt.Errorf("the handoff for %s is empty; analyse the problem first", pos[1])
		}
		if *asJSON {
			return printJSON(h)
		}
		fmt.Println(h.Text)
		return nil
	})
}

func cmdReview(args []string) error {
	fs := newFlags("review")
	file := fs.String("file", "", "read the reply from this file (default: stdin)")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageError("usage: dlg review <id> <author> [--file F]   (reply on stdin)")
	}
	pid, err := parseID(pos[0])
	if err != nil {
		return err
	}
	body, err := readBody(*file)
	if err != nil {
		return err
	}
	return withCore(func(c Core) error {
		out, err := c.Review(pid, pos[1], body, nil)
		if err != nil {
			return err
		}
		warn(out.Warnings)
		if *asJSON {
			return printJSON(out)
		}
		fmt.Println("review", out.ID)
		return nil
	})
}

func cmdNote(args []string) error {
	fs := newFlags("note")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError("usage: dlg note <id> [text]   (or text on stdin)")
	}
	pid, err := parseID(pos[0])
	if err != nil {
		return err
	}
	body := strings.Join(pos[1:], " ")
	if body == "" {
		if body, err = readBody(""); err != nil {
			return err
		}
	}
	return withCore(func(c Core) error {
		id, err := c.Note(pid, body)
		if err == nil {
			fmt.Println("note", id)
		}
		return err
	})
}

func cmdParse(args []string) error {
	fs := newFlags("parse")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("usage: dlg parse <review-id> [--json]")
	}
	id, err := parseID(pos[0])
	if err != nil {
		return err
	}
	return withCore(func(c Core) error {
		p, err := c.Parse(id)
		if err != nil {
			return err
		}
		warn(p.Warnings)
		if *asJSON {
			return printJSON(p)
		}
		fmt.Printf("review %d by %s\n", p.ReviewID, p.Author)
		for _, pt := range p.Points {
			fmt.Printf("[%s] %s: %s\n", pt.Key, pt.Stance, pt.Reason)
		}
		return nil
	})
}

// checkCommand refuses a command that cannot run as typed: no words, or a flag where the
// program should be (a stray --yes, say).
func checkCommand(command string) error {
	words, err := splitCommand(command)
	if err != nil {
		return err
	}
	if len(words) == 0 || strings.HasPrefix(words[0], "-") {
		return usageError(fmt.Sprintf("%q does not start with a program to run", command))
	}
	return nil
}

func checkName(name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return usageError(fmt.Sprintf("%q is not an agent name", name))
	}
	return nil
}

func knownAgent(name string) (primary, implement string, ok bool) {
	for _, a := range knownAgents {
		if a.name == name {
			return a.primary, a.implement, true
		}
	}
	return "", "", false
}

// confirmSetting prints each field a setter is about to store, quoted so hidden characters
// show, and asks on a terminal even when yes is set. Without a terminal the answer is yes.
func confirmSetting(header string, fields [][2]string, yes bool) bool {
	fmt.Fprintln(os.Stderr, header)
	for _, f := range fields {
		fmt.Fprintf(os.Stderr, "  %s = %q\n", f[0], f[1])
	}
	if !isTerminal(os.Stdin) {
		return yes
	}
	fmt.Fprint(os.Stderr, "Allow? [y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes"
}

// cmdSetCommand handles the setters that make the ledger run a command, plus workdir.
// Only a leading --yes is a flag: parsing stops at the first argument that is not one,
// so the command's own flags (-p, --full-auto) stay in the command.
func cmdSetCommand(l *Ledger, cfg Settings, args []string) error {
	fs := newFlags("config " + args[0])
	yes := fs.Bool("yes", false, "store without asking when not on a terminal")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	pos := fs.Args()
	fields := [][2]string{}
	switch {
	case args[0] == "use" && len(pos) == 1:
		primary, implement, ok := knownAgent(pos[0])
		if !ok {
			return fmt.Errorf("unknown agent %q; use dlg config set-primary <name> '<command>' ['<implement-command>']", pos[0])
		}
		fields = append(fields, [2]string{"primary_name", pos[0]}, [2]string{"primary_command", primary},
			[2]string{"implement_command", implement})
	case args[0] == "set-primary" && (len(pos) == 2 || len(pos) == 3):
		if err := checkName(pos[0]); err != nil {
			return err
		}
		fields = append(fields, [2]string{"primary_name", pos[0]}, [2]string{"primary_command", pos[1]})
		if len(pos) == 3 {
			fields = append(fields, [2]string{"implement_command", pos[2]})
		}
		for _, f := range fields[1:] {
			if err := checkCommand(f[1]); err != nil {
				return err
			}
		}
	case args[0] == "set-workdir" && len(pos) == 1:
		dir, err := filepath.Abs(pos[0])
		if err != nil {
			return err
		}
		fields = append(fields, [2]string{"workdir", dir})
	case args[0] == "set-runner" && len(pos) >= 1:
		return setRunner(l, cfg, pos)
	default:
		return usageError(configUsage)
	}
	if !confirmSetting("dlg will store:", fields, *yes) {
		if !isTerminal(os.Stdin) {
			return errors.New("nothing stored: not on a terminal, so pass --yes right after " + args[0])
		}
		return errors.New("not confirmed; nothing stored")
	}
	values := D{}
	for _, f := range fields {
		values[f[0]] = f[1]
	}
	_, err := l.updateSettings(values, "user")
	return err
}

// setRunner always stores the command; a y on a terminal also approves it, so the UI can run it.
func setRunner(l *Ledger, cfg Settings, pos []string) error {
	agent, command := pos[0], strings.Join(pos[1:], " ")
	if err := checkName(agent); err != nil {
		return err
	}
	if command == "" {
		primary, _, ok := knownAgent(agent)
		if !ok {
			return fmt.Errorf("no preset for %q; give the command: dlg config set-runner %s '<command>'", agent, agent)
		}
		command = primary
	}
	if err := checkCommand(command); err != nil {
		return err
	}
	runners := map[string]string{}
	for k, v := range cfg.SecondaryCommands {
		runners[k] = v
	}
	runners[agent] = command
	values := D{"secondary_commands": runners}
	header := fmt.Sprintf("dlg will store this runner and pipe handoffs to its stdin (in %s):", cfg.Workdir)
	// Without a terminal confirmSetting returns false: the command is stored unapproved.
	approved := confirmSetting(header, [][2]string{{"secondary_commands." + agent, command}}, false)
	if approved && !slices.Contains(cfg.RunnerConfirmed, command) {
		values["runner_confirmed"] = append(slices.Clone(cfg.RunnerConfirmed), command)
	}
	if _, err := l.updateSettings(values, "user"); err != nil {
		return err
	}
	if !approved {
		fmt.Fprintf(os.Stderr, "stored, not approved: run the same line in a terminal and answer y, or dlg run <id> %s\n", agent)
	}
	return nil
}

const configUsage = "usage: dlg config [--global] show | use <agent> | set-primary <name> <command> [implement-command]\n" +
	"       dlg config set-workdir <dir> | set-runner <agent> [command] | unset-runner <agent>\n" +
	"       (use, set-primary, set-workdir and set-runner take --yes right after the subcommand)\n" +
	"       dlg config add-model <key> <label> <context-label> [args...] | remove-model <key>\n" +
	"       dlg config set-model-list <command> | unset-model-list\n" +
	"       dlg config set-default-model <analyse|implement> <key|none> | init-models [claude|codex]"

// cmdConfig edits the settings that only the command line may change.
func cmdConfig(args []string) error {
	const use = configUsage
	if len(args) == 0 {
		return usageError(use)
	}
	if args[0] == "--global" {
		return cmdGlobalConfig(args[1:])
	}
	path, err := ledgerPath()
	if err != nil {
		return err
	}
	l, err := openLedger(path)
	if err != nil {
		return err
	}
	defer l.close()
	cfg, err := l.settings()
	if err != nil {
		return err
	}
	runners := map[string]string{}
	for k, v := range cfg.SecondaryCommands {
		runners[k] = v
	}
	switch {
	case args[0] == "show" && len(args) == 1:
		return printJSON(cfg)
	case slices.Contains([]string{"use", "set-primary", "set-workdir", "set-runner"}, args[0]):
		return cmdSetCommand(l, cfg, args)
	case args[0] == "unset-runner" && len(args) == 2:
		delete(runners, args[1])
	case args[0] == "add-model" && len(args) >= 4:
		m, err := addModelPreset(args[1:])
		if err != nil {
			return err
		}
		if _, err := splitCommand(m.Args); err != nil {
			return err
		}
		agent := agentOf(cfg.PrimaryCommand)
		warnings, err := validatePreset(agent, m)
		if err != nil {
			return err
		}
		warn(warnings)
		if m.structured() {
			m.CLIVersion = cliVersion(agent)
		}
		_, err = l.updateSettings(D{"models": withPreset(cfg.Models, m)}, "user")
		return err
	case args[0] == "set-default-model":
		return cmdSetDefaultModel(l, cfg, args[1:])
	case args[0] == "init-models":
		return cmdInitModels(l, cfg, args[1:])
	case args[0] == "remove-model" && len(args) == 2:
		models := slices.DeleteFunc(slices.Clone(cfg.Models), func(o ModelPreset) bool { return o.Key == args[1] })
		_, err = l.updateSettings(D{"models": models}, "user")
		return err
	case args[0] == "set-model-list" && len(args) >= 2:
		command := strings.Join(args[1:], " ")
		if _, err := splitCommand(command); err != nil {
			return err
		}
		_, err = l.updateSettings(D{"model_list_command": command}, "user")
		return err
	case args[0] == "unset-model-list" && len(args) == 1:
		_, err = l.updateSettings(D{"model_list_command": ""}, "user")
		return err
	default:
		return usageError(use)
	}
	_, err = l.updateSettings(D{"secondary_commands": runners}, "user")
	return err
}

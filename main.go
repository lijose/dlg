// `dlg init` creates the local ledger; `dlg serve` starts (or resumes) the UI.
// Other commands are in cli.go.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

// Investigation runs read-only; implementation needs write access.
var knownAgents = []struct{ name, primary, implement string }{
	{"codex", "codex exec", "codex exec --full-auto"},
	{"claude", "claude -p", "claude -p --permission-mode acceptEdits"},
}

func findLedger(start string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(dbPath(dir)); err == nil {
			return dbPath(dir)
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

func initLedger() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	path := dbPath(root)
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Ledger already exists at %s\n", path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return err
	}
	l, err := openLedger(path)
	if err != nil {
		return err
	}
	defer l.close()
	settings := D{"workdir": root}
	global, _ := readGlobalConfig() // a global primary replaces autodetection
	for _, a := range knownAgents {
		if _, ok := global["primary_command"]; ok {
			break
		}
		if _, err := exec.LookPath(a.name); err != nil {
			continue
		}
		others := []string{}
		for _, n := range []string{"claude", "gemini", "codex"} {
			if n != a.name {
				others = append(others, n)
			}
		}
		settings["primary_name"], settings["primary_command"], settings["implement_command"] = a.name, a.primary, a.implement
		settings["secondary_names"] = others[:2]
		break
	}
	if _, err := l.updateSettings(settings, "user"); err != nil {
		return err
	}
	fmt.Printf("Created %s\nRun `dlg serve` to open the UI.\n", path)
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start() // best effort: the URL is printed too
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8765, "port to listen on")
	noBrowser := fs.Bool("no-browser", false, "don't open the browser")
	fs.Parse(args)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	path := findLedger(cwd)
	if path == "" {
		return fmt.Errorf("no %s/ found here or in a parent directory. Run `dlg init` first", ledgerDir)
	}
	l, err := openLedger(path)
	if err != nil {
		return err
	}
	defer l.close()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	fmt.Printf("Decision Ledger: %s\nOpen %s\n", path, url)
	// Tells the CLI where to find this server; a stale file after a crash is harmless (the probe fails).
	urlFile := filepath.Join(filepath.Dir(path), serveFile)
	if err := os.WriteFile(urlFile, []byte(url+"\n"), 0o644); err == nil {
		defer os.Remove(urlFile)
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		go func() { <-stop; os.Remove(urlFile); os.Exit(0) }()
	}
	if !*noBrowser {
		time.AfterFunc(time.Second, func() { openBrowser(url) })
	}
	return http.Serve(ln, newServer(l))
}

// version is set at release time: go build -ldflags "-X main.version=v1.2.3".
// `go install ...@v1.2.3` builds fall back to the module version.
var version = "dev"

func versionString() string {
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
	}
	return version
}

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.As(err, new(usageError)) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

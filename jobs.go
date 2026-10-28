// Background jobs: analyse and implement runs happen in memory so the UI can poll their
// progress. Nothing here survives a restart; the stored agent output does.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

const jobTailBytes = 32 << 10

// Gap: cancel/revert is covered only by the end-to-end job test (ledger_test.go), not by a git-backed test.

// tailBuffer keeps the last max bytes written to it. Safe for concurrent writers
// (an agent's stdout and stderr arrive on different goroutines).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = slices.Clone(t.buf[len(t.buf)-t.max:])
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(string(t.buf), "")
}

type job struct {
	mu        sync.Mutex
	id, kind  string
	problems  []int64
	model     string
	agent     string // the reviewer, for "review" jobs
	started   time.Time
	ended     time.Time
	state     string // running | done | failed | cancelled
	errMsg    string
	result    any
	progress  any      // set while running, e.g. the auto-round position
	cost      CostLine // usage of the agent runs so far (several for auto rounds)
	tail      *tailBuffer
	cancel    context.CancelFunc
	cancelled bool
	done      chan struct{}
	revertDir string // the workdir; set only if its git repository was clean when the job started
}

type jobView struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	ProblemIDs []int64   `json:"problem_ids"`
	Model      string    `json:"model"`
	Agent      string    `json:"agent"`
	State      string    `json:"state"`
	ElapsedMS  int64     `json:"elapsed_ms"`
	Output     string    `json:"output"`
	Error      string    `json:"error"`
	CanRevert  bool      `json:"can_revert"`
	Result     any       `json:"result"`
	Progress   any       `json:"progress"`
	Cost       *CostLine `json:"cost,omitempty"`
}

func (j *job) view() jobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	end := j.ended
	if j.state == "running" {
		end = time.Now()
	}
	var cost *CostLine
	if j.cost.Runs > 0 {
		c := j.cost
		cost = &c
	}
	return jobView{Cost: cost, ID: j.id, Kind: j.kind, ProblemIDs: j.problems, Model: j.model, Agent: j.agent, State: j.state,
		ElapsedMS: end.Sub(j.started).Milliseconds(), Output: j.tail.String(), Error: j.errMsg,
		CanRevert: j.state == "running" && j.revertDir != "", Result: j.result, Progress: j.progress}
}

type jobManager struct {
	mu   sync.Mutex
	jobs []*job
}

var jobs = &jobManager{}

// cleanGitRoot returns the repository root of dir if it is a git work tree with no changes.
func cleanGitRoot(dir string) string {
	top, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(top))
	if status, err := exec.Command("git", "-C", root, "status", "--porcelain").Output(); err != nil || len(status) != 0 {
		return ""
	}
	return root
}

// start runs fn in the background. Two running jobs may not touch the same problem.
func (m *jobManager) start(kind string, problems []int64, model, workdir string, fn func(ctx context.Context, out io.Writer) (any, error)) (*job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.jobs {
		o.mu.Lock()
		busy := o.state == "running" && slices.ContainsFunc(problems, func(p int64) bool { return slices.Contains(o.problems, p) })
		o.mu.Unlock()
		if busy {
			return nil, ledgerErr("a %s job is already running for this problem", o.kind)
		}
	}
	id := make([]byte, 6)
	rand.Read(id)
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: hex.EncodeToString(id), kind: kind, problems: problems, model: model, started: time.Now(),
		state: "running", tail: &tailBuffer{max: jobTailBytes}, cancel: cancel, done: make(chan struct{})}
	if kind == "implement" && cleanGitRoot(workdir) != "" {
		j.revertDir = workdir
	}
	for len(m.jobs) >= 50 { // keep memory bounded: drop the oldest job that has finished
		i := slices.IndexFunc(m.jobs, func(o *job) bool {
			select {
			case <-o.done:
				return true
			default:
				return false
			}
		})
		if i < 0 {
			break
		}
		m.jobs = slices.Delete(m.jobs, i, i+1)
	}
	m.jobs = append(m.jobs, j)
	go func() {
		res, err := fn(context.WithValue(ctx, jobKey{}, j), j.tail)
		j.mu.Lock()
		j.ended, j.result = time.Now(), res
		switch {
		case j.cancelled:
			j.state = "cancelled"
		case err != nil:
			j.state, j.errMsg = "failed", err.Error()
		default:
			j.state = "done"
		}
		j.mu.Unlock()
		cancel()
		close(j.done)
	}()
	return j, nil
}

type jobKey struct{}

// reportProgress records where the job that owns ctx is; a no-op outside a job (e.g. the CLI).
func reportProgress(ctx context.Context, v any) {
	if j, ok := ctx.Value(jobKey{}).(*job); ok {
		j.mu.Lock()
		j.progress = v
		j.mu.Unlock()
	}
}

// addJobUsage adds a finished agent run to the job that owns ctx, if any.
func addJobUsage(ctx context.Context, u *Usage, failed bool) {
	if j, ok := ctx.Value(jobKey{}).(*job); ok && u != nil {
		j.mu.Lock()
		j.cost.add(*u, 1, failed)
		j.mu.Unlock()
	}
}

func (m *jobManager) get(id string) *job {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

// latestFor returns the newest job that touched the problem, or nil.
func (m *jobManager) latestFor(pid int64) *job {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.jobs) - 1; i >= 0; i-- {
		if slices.Contains(m.jobs[i].problems, pid) {
			return m.jobs[i]
		}
	}
	return nil
}

// stop kills the running job. With revert, and only if the workspace was a clean git
// repository at the start, it then discards every change made since under workdir
// (not the whole repository, when workdir is a subfolder).
func (m *jobManager) stop(id string, revert bool) (*job, error) {
	j := m.get(id)
	if j == nil {
		return nil, ledgerErr("unknown job")
	}
	j.mu.Lock()
	if j.state != "running" {
		j.mu.Unlock()
		return nil, ledgerErr("the job is not running")
	}
	if revert && j.revertDir == "" {
		j.mu.Unlock()
		return nil, ledgerErr("revert is only possible when the workspace was a clean git repository at the start")
	}
	j.cancelled = true
	j.mu.Unlock()
	j.cancel()
	select {
	case <-j.done:
	case <-time.After(30 * time.Second):
		return nil, ledgerErr("the job did not stop in time")
	}
	if revert {
		if out, err := exec.Command("git", "-C", j.revertDir, "checkout", "HEAD", "--", ".").CombinedOutput(); err != nil {
			return nil, ledgerErr("git checkout failed: %s", strings.TrimSpace(string(out)))
		}
		if out, err := exec.Command("git", "-C", j.revertDir, "clean", "-fd", "--", ".").CombinedOutput(); err != nil {
			return nil, ledgerErr("git clean failed: %s", strings.TrimSpace(string(out)))
		}
	}
	return j, nil
}

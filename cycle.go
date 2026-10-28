// Auto rounds: one click runs the reviewers that have an approved runner, then has the
// primary process their replies, for up to N rounds. It only gathers opinions: selecting,
// approving and resolving stay with the user.
package main

import (
	"fmt"
	"slices"
)

const maxCycleRounds = 3

// cycleRun stops early when the primary asks questions, every point is settled, a round
// changes nothing, a reviewer needs copy-paste, or something fails.
func (s *server) cycleRun(pid int64, rounds int, o runOpts) (any, error) {
	done := func(round int, why string) (any, error) {
		fmt.Fprintf(o.out, "\n■ %s\n", why)
		return D{"rounds": round, "stopped": why}, nil
	}
	d, err := s.l.problemDetail(pid)
	if err != nil {
		return nil, err
	}
	if _, a, _ := latestAnalysis(d); a == nil {
		fmt.Fprintf(o.out, "── first analysis\n")
		o.progress(0, rounds, "analysing", "")
		if _, err := s.analyseRun(pid, o); err != nil {
			return nil, err
		}
		if stop, why, err := s.cycleShouldStop(pid); err != nil {
			return nil, err
		} else if stop {
			return done(0, why)
		}
	}
	for round := 1; round <= rounds; round++ {
		cfg, err := s.l.settings()
		if err != nil {
			return nil, err
		}
		st, err := problemStatus(s.l, pid)
		if err != nil {
			return nil, err
		}
		manual := slices.Clone(st.Waiting)
		for _, to := range st.ToSend {
			if !runnable(cfg, to) {
				manual = append(manual, to)
				continue
			}
			fmt.Fprintf(o.out, "── round %d/%d: %s reviewing\n", round, rounds, to)
			o.progress(round, rounds, "reviewing", to)
			if _, _, err := runSecondary(o.ctx, s.l, localCore{s.l}, cfg, pid, to, "", runTimeout, o.out); err != nil {
				return nil, fmt.Errorf("%s: %w", to, err)
			}
		}
		if len(manual) > 0 {
			return done(round-1, fmt.Sprintf("paused: waiting for %v (no approved runner; copy-paste the handoff)", manual))
		}
		if st, err = problemStatus(s.l, pid); err != nil {
			return nil, err
		}
		if st.NewReplies == 0 {
			return done(round-1, "stopped: no reviewer had anything new to review")
		}
		fmt.Fprintf(o.out, "── round %d/%d: processing %d repl(ies)\n", round, rounds, st.NewReplies)
		o.progress(round, rounds, "processing", "")
		if _, err := s.analyseRun(pid, o); err != nil {
			return nil, err
		}
		if stop, why, err := s.cycleShouldStop(pid); err != nil {
			return nil, err
		} else if stop {
			return done(round, why)
		}
	}
	return done(rounds, fmt.Sprintf("done: %d round(s)", rounds))
}

// cycleShouldStop looks at the latest analysis for a reason to hand back to the user.
func (s *server) cycleShouldStop(pid int64) (bool, string, error) {
	d, err := s.l.problemDetail(pid)
	if err != nil {
		return true, "", err
	}
	_, a, round := latestAnalysis(d)
	st, err := problemStatus(s.l, pid)
	ruled := rulings(d)
	switch {
	case err != nil:
		return true, "", err
	case len(a.Questions) > 0:
		return true, fmt.Sprintf("paused: the primary has %d question(s) for you", len(a.Questions)), nil
	case st.Points > 0 && st.Settled == st.Points:
		return true, "stopped: every point is settled", nil
	case st.Points > 0 && !slices.ContainsFunc(a.Points, func(p PointOut) bool { return needsReview(p, ruled) }):
		return true, fmt.Sprintf("stopped: nothing left for reviewers; %d point(s) wait on your decision", st.Points-st.Settled), nil
	case round > 1 && len(a.Changes) == 0:
		return true, "stopped: the last round changed nothing", nil
	}
	return false, "", nil
}

// progress tells the UI which round and phase an auto-round job is in.
func (o runOpts) progress(round, rounds int, phase, agent string) {
	if o.ctx != nil {
		reportProgress(o.ctx, D{"round": round, "rounds": rounds, "phase": phase, "agent": agent})
	}
}

// runnable reports whether to has a runner the user approved on the command line.
func runnable(cfg Settings, to string) bool {
	cmd := cfg.SecondaryCommands[to]
	return cmd != "" && slices.Contains(cfg.RunnerConfirmed, cmd)
}

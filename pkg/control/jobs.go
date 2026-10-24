package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Job states.
const (
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCanceled  = "canceled"
)

// Step states.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

const maxLines = 5000

// Step is one stage of a job.
type Step struct {
	Title string `json:"title"`
	State string `json:"state"`
}

// Line is one line of job output.
type Line struct {
	N      int       `json:"n"`
	T      time.Time `json:"t"`
	Stream string    `json:"stream"` // out, err, info
	Text   string    `json:"text"`
}

// JobView is the JSON form of a job.
type JobView struct {
	ID        string     `json:"id"`
	Action    string     `json:"action"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Error     string     `json:"error"`
	Steps     []Step     `json:"steps"`
	Lines     int        `json:"lines"`
}

// Job is a running or finished operation.
type Job struct {
	mu     sync.Mutex
	view   JobView
	lines  []Line // the last maxLines lines
	cancel context.CancelFunc
	scrub  func(string) string
	env    []string
	done   chan struct{}
}

// View returns a snapshot.
func (j *Job) View() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := j.view
	v.Steps = append([]Step(nil), j.view.Steps...)
	return v
}

// Log returns lines numbered after `after`, and the next cursor.
func (j *Job) Log(after int) ([]Line, int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []Line{}
	for _, l := range j.lines {
		if l.N > after {
			out = append(out, l)
		}
	}
	return out, j.view.Lines
}

// Cancel stops the job; running commands get SIGTERM, then SIGKILL.
func (j *Job) Cancel() { j.cancel() }

func (j *Job) logf(stream, format string, args ...any) {
	j.line(stream, fmt.Sprintf(format, args...))
}

func (j *Job) line(stream, text string) {
	text = strings.TrimRight(text, "\r\n")
	if j.scrub != nil {
		text = j.scrub(text)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.view.Lines++
	j.lines = append(j.lines, Line{N: j.view.Lines, T: time.Now(), Stream: stream, Text: text})
	if len(j.lines) > maxLines {
		j.lines = j.lines[len(j.lines)-maxLines:]
	}
}

func (j *Job) setStep(i int, state string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if i >= 0 && i < len(j.view.Steps) {
		j.view.Steps[i].State = state
	}
}

// step runs fn as step i. A skip error marks it skipped and continues.
func (j *Job) step(ctx context.Context, i int, fn func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.setStep(i, StepRunning)
	j.logf("info", "▶ %s", j.View().Steps[i].Title)
	err := fn(ctx)
	switch {
	case errors.Is(err, errSkip):
		j.setStep(i, StepSkipped)
		return nil
	case err != nil:
		j.setStep(i, StepFailed)
		return err
	}
	j.setStep(i, StepDone)
	return nil
}

// errSkip marks a step as not needed.
var errSkip = errors.New("skipped")

// skipf logs why a step is skipped.
func (j *Job) skipf(format string, args ...any) error {
	j.logf("info", "  skipped: "+format, args...)
	return errSkip
}

// run executes a command, streaming its output into the job log.
func (j *Job) run(ctx context.Context, dir string, name string, args ...string) error {
	_, err := j.runOut(ctx, dir, false, name, args...)
	return err
}

// runOut is run that can also capture stdout.
func (j *Job) runOut(ctx context.Context, dir string, capture bool, name string, args ...string) (string, error) {
	j.logf("info", "$ %s", shellLine(name, args))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), j.env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole process group so children (make, docker build)
		// stop too.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		go func(pid int) {
			time.Sleep(5 * time.Second)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}(cmd.Process.Pid)
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var captured strings.Builder
	var wg sync.WaitGroup
	pump := func(r io.Reader, stream string, keep bool) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if keep {
				captured.WriteString(sc.Text())
				captured.WriteByte('\n')
			}
			j.line(stream, sc.Text())
		}
	}
	wg.Add(2)
	go pump(stdout, "out", capture)
	go pump(stderr, "err", false)
	wg.Wait()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return captured.String(), ctx.Err()
	}
	if err != nil {
		return captured.String(), fmt.Errorf("%s: %w", name, err)
	}
	return captured.String(), nil
}

// quiet runs a command without logging it and returns its combined output.
func quiet(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func shellLine(name string, args []string) string {
	parts := []string{name}
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'$&|;<>(){}*?") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// waitFor polls cond every interval until it returns true, ctx ends or the
// timeout passes.
func (j *Job) waitFor(ctx context.Context, what string, timeout, every time.Duration, cond func(ctx context.Context) (bool, error)) error {
	j.logf("info", "  waiting for %s (up to %s)…", what, timeout)
	deadline := time.Now().Add(timeout)
	start := time.Now()
	lastNote := time.Now()
	for {
		ok, err := cond(ctx)
		if ok {
			j.logf("info", "  %s: ready after %s", what, time.Since(start).Round(time.Second))
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("timed out waiting for %s: %w", what, err)
			}
			return fmt.Errorf("timed out waiting for %s", what)
		}
		if time.Since(lastNote) > 30*time.Second {
			lastNote = time.Now()
			msg := fmt.Sprintf("  still waiting for %s (%s)", what, time.Since(start).Round(time.Second))
			if err != nil {
				msg += ": " + err.Error()
			}
			j.line("info", msg)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

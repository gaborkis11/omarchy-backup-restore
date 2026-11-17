// Package runner abstracts running external commands so the rest of obr can be
// tested with a recording fake.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Cmd describes one external command invocation.
type Cmd struct {
	Name   string
	Args   []string
	Env    []string // extra KEY=VALUE pairs appended to the current environment
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer // optional streaming sink (in addition to the captured Result.Stdout)
	Stderr io.Writer // optional streaming sink
	Sudo   bool      // run through `sudo -n` (credentials must be cached beforehand)
}

// String renders the command for logs. Environment values are never printed.
func (c Cmd) String() string {
	parts := []string{}
	if c.Sudo {
		parts = append(parts, "sudo")
	}
	parts = append(parts, c.Name)
	for _, a := range c.Args {
		if strings.ContainsAny(a, " \t\"'") {
			a = fmt.Sprintf("%q", a)
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Result is the captured outcome of a command.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// ExitError is returned when a command exits with a non-zero status.
type ExitError struct {
	Cmd    string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 400 {
		msg = msg[len(msg)-400:]
	}
	if msg == "" {
		return fmt.Sprintf("%s: exit %d", e.Cmd, e.Code)
	}
	return fmt.Sprintf("%s: exit %d: %s", e.Cmd, e.Code, msg)
}

// Code extracts an exit code from an error, or -1.
func Code(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}

// Runner runs commands.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// Exec is the real implementation.
type Exec struct {
	// Logf, when set, receives one line per executed command.
	Logf func(format string, args ...any)
	// Env is appended to every command's environment (e.g. HOME when obr
	// works on a different home directory).
	Env []string
}

func (e *Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	name, args := c.Name, c.Args
	if c.Sudo && os.Geteuid() != 0 {
		args = append([]string{"-n", "--", name}, args...)
		name = "sudo"
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = c.Dir
	cmd.Env = append(append(os.Environ(), e.Env...), c.Env...)
	cmd.Stdin = c.Stdin
	var out, errb bytes.Buffer
	if c.Stdout != nil {
		cmd.Stdout = io.MultiWriter(&out, c.Stdout)
	} else {
		cmd.Stdout = &out
	}
	if c.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&errb, c.Stderr)
	} else {
		cmd.Stderr = &errb
	}
	if e.Logf != nil {
		e.Logf("exec: %s", c.String())
	}
	err := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, &ExitError{Cmd: c.String(), Code: res.ExitCode, Stderr: errb.String()}
		}
		res.ExitCode = -1
		return res, fmt.Errorf("%s: %w", c.String(), err)
	}
	return res, nil
}

// Fake records commands and answers them from scripted responses.
type Fake struct {
	mu        sync.Mutex
	Calls     []Cmd
	responses []fakeResp
	// Fallback is used when no scripted response matches. When nil, unmatched
	// commands succeed with empty output.
	Fallback func(c Cmd) (Result, error)
}

type fakeResp struct {
	prefix string
	res    Result
	err    error
}

// On scripts a response for commands whose String() starts with prefix.
func (f *Fake) On(prefix string, stdout string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, fakeResp{prefix: prefix, res: Result{Stdout: []byte(stdout)}, err: err})
}

func (f *Fake) Run(_ context.Context, c Cmd) (Result, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, c)
	resps := append([]fakeResp(nil), f.responses...)
	fb := f.Fallback
	f.mu.Unlock()
	s := c.String()
	for i := len(resps) - 1; i >= 0; i-- {
		if strings.HasPrefix(s, resps[i].prefix) {
			if c.Stdout != nil {
				_, _ = c.Stdout.Write(resps[i].res.Stdout)
			}
			return resps[i].res, resps[i].err
		}
	}
	if fb != nil {
		return fb(c)
	}
	return Result{}, nil
}

// Commands returns the recorded command strings.
func (f *Fake) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = c.String()
	}
	return out
}

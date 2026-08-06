package paseo

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

const defaultCommandTimeout = 15 * time.Second

type commandResult struct {
	stdout []byte
	stderr []byte
}

type runner interface {
	Run(ctx context.Context, args []string) (commandResult, error)
}

// CLIRunner invokes Paseo directly with an argv array and a scrubbed environment.
type CLIRunner struct {
	Binary  string
	Timeout time.Duration
}

// Run executes one argv-only Paseo command with separate output streams.
func (r CLIRunner) Run(ctx context.Context, args []string) (commandResult, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	binary := r.Binary
	if binary == "" {
		binary = "paseo"
	}
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // binary is operator configuration; args are structured below.
	cmd.Env = scrubPaseoEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}, err
}

func scrubPaseoEnv(environ []string) []string {
	clean := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "PASEO_") {
			continue
		}
		clean = append(clean, entry)
	}
	return clean
}

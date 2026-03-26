package remote

import (
	"bufio"
	"context"
	"os/exec"
)

// StreamLine represents a line from the remote Claude output.
type StreamLine struct {
	Line []byte
	Err  error
	Done bool
}

// StreamExec runs a command in the pod and streams its stdout line by line.
func StreamExec(ctx context.Context, cfg Config, podName string, cmd ...string) (<-chan StreamLine, error) {
	args := []string{
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"exec", podName, "--",
	}
	args = append(args, cmd...)
	c := exec.CommandContext(ctx, "kubectl", args...)

	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.Stderr = nil // ignore stderr

	if err := c.Start(); err != nil {
		return nil, err
	}

	ch := make(chan StreamLine, 64)
	go func() {
		defer close(ch)
		defer c.Wait()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := make([]byte, len(scanner.Bytes()))
			copy(line, scanner.Bytes())
			select {
			case ch <- StreamLine{Line: line}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case ch <- StreamLine{Err: err}:
			case <-ctx.Done():
			}
		}
		select {
		case ch <- StreamLine{Done: true}:
		case <-ctx.Done():
		}
	}()

	return ch, nil
}

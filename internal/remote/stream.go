package remote

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
)

// StreamLine represents a single line from the remote session stream.
type StreamLine struct {
	Line []byte // raw JSONL line
	Err  error  // stream error
	Done bool   // stream ended
}

// StreamLogs starts streaming logs from the pod's main container.
// Returns a channel that receives JSONL lines until the context is cancelled
// or the stream ends.
func StreamLogs(ctx context.Context, cfg Config, podName string) (<-chan StreamLine, error) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"logs", "-f", podName, "-c", "main")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start kubectl logs: %w", err)
	}

	ch := make(chan StreamLine, 64)
	go func() {
		defer close(ch)
		defer cmd.Wait()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for large JSONL lines
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

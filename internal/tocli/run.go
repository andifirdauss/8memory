package tocli

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// waitAfterExit bounds the wait for output pipes kept open by a background
// process that outlives the CLI.
const waitAfterExit = 5 * time.Second

const stderrTailBytes = 2048

// lineWriter passes each complete line to handle.
type lineWriter struct {
	pending []byte
	handle  func([]byte)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			return len(p), nil
		}
		w.handle(w.pending[:end])
		w.pending = w.pending[end+1:]
	}
}

// tailWriter keeps only the end of what is written to it.
type tailWriter struct{ data []byte }

func (w *tailWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	if extra := len(w.data) - stderrTailBytes; extra > 0 {
		w.data = w.data[extra:]
	}
	return len(p), nil
}

// Run executes one prompt and reports its progress through emit. It returns
// an error only for a failure no event has explained.
func Run(cmd *exec.Cmd, parser Parser, emit func(Event)) error {
	reported := false
	handle := func(line []byte) {
		for _, event := range parser.Parse(bytes.TrimSpace(line)) {
			reported = reported || event.Kind == EventError
			emit(event)
		}
	}
	stdout := &lineWriter{handle: handle}
	stderr := &tailWriter{}
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = stdout, stderr, waitAfterExit

	err := cmd.Run()
	if len(stdout.pending) > 0 {
		handle(stdout.pending)
	}
	if err == nil || errors.Is(err, exec.ErrWaitDelay) || reported {
		return nil
	}
	if detail := strings.TrimSpace(Clean(string(stderr.data))); detail != "" {
		return fmt.Errorf("%w: %s", err, detail)
	}
	return err
}

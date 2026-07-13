// Package ui holds small terminal helpers shared by the commands: prompting for
// input and a lightweight progress spinner. It deliberately avoids heavy TUI
// dependencies so the CLI stays scriptable and portable.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Prompt writes label to out and reads a single trimmed line from in. An empty
// line returns the empty string; callers decide whether that is acceptable.
func Prompt(in io.Reader, out io.Writer, label string) (string, error) {
	fmt.Fprint(out, label)
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// PromptDefault behaves like Prompt but returns fallback when the user enters
// nothing.
func PromptDefault(in io.Reader, out io.Writer, label, fallback string) (string, error) {
	value, err := Prompt(in, out, fmt.Sprintf("%s [%s]: ", label, fallback))
	if err != nil {
		return "", err
	}
	if value == "" {
		return fallback, nil
	}
	return value, nil
}

// Spinner is a minimal, thread-safe activity indicator. It no-ops when its
// writer is not a terminal-like stream (callers pass io.Discard to silence it).
type Spinner struct {
	out     io.Writer
	label   string
	mu      sync.Mutex
	stop    chan struct{}
	started bool
	stopped bool
}

// NewSpinner creates a spinner that renders to out with the given label.
func NewSpinner(out io.Writer, label string) *Spinner {
	return &Spinner{out: out, label: label, stop: make(chan struct{})}
}

// Start begins animating until Stop is called. It is a no-op if already started.
func (s *Spinner) Start() {
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	frames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	go func() {
		i := 0
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.mu.Lock()
				fmt.Fprintf(s.out, "\r%c %s", frames[i%len(frames)], s.label)
				s.mu.Unlock()
				i++
			}
		}
	}()
}

// SetLabel updates the text shown next to the spinner.
func (s *Spinner) SetLabel(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

// Stop halts the spinner and clears its line. It is safe to call more than once.
func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.stop)
	// Clear the spinner line.
	fmt.Fprintf(s.out, "\r%s\r", strings.Repeat(" ", len(s.label)+2))
}

// Package ui holds small terminal helpers shared by the commands: prompting for
// input and a lightweight progress spinner. It deliberately avoids heavy TUI
// dependencies so the CLI stays scriptable and portable.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// IsTTY reports whether w is an interactive terminal (a character device), so
// callers can enable in-place animation only when it will not corrupt piped or
// redirected output. It needs no external dependency.
func IsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

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

// ProgressBar renders an in-place [####----] progress bar for a known number of
// steps. When active is false (e.g. output is not a terminal or is piped) every
// method is a no-op except Println, so scripted runs stay clean.
type ProgressBar struct {
	out    io.Writer
	total  int
	width  int
	active bool
	last   int // width of the last drawn line, for clearing
}

// NewProgressBar builds a bar over total steps. Pass active=false to disable the
// in-place animation (Println still writes plain lines).
func NewProgressBar(out io.Writer, total int, active bool) *ProgressBar {
	return &ProgressBar{out: out, total: total, width: 24, active: active && total > 0}
}

// Update draws the bar at current/total steps with a trailing label.
func (p *ProgressBar) Update(current int, label string) { p.UpdateFrac(current, 0, label) }

// UpdateFrac draws the bar at (current+frac)/total steps, where frac in [0,1)
// is progress through the current step — letting the fill advance within a
// single item. Truncates the label to keep the line a reasonable width.
func (p *ProgressBar) UpdateFrac(current int, frac float64, label string) {
	if !p.active {
		return
	}
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	pos := (float64(current) + frac) / float64(p.total)
	if pos > 1 {
		pos = 1
	}
	filled := int(pos * float64(p.width))
	if filled > p.width {
		filled = p.width
	}
	pct := int(pos * 100)
	if len(label) > 48 {
		label = "…" + label[len(label)-47:]
	}
	line := fmt.Sprintf("[%s%s] %3d%% (%d/%d) %s",
		strings.Repeat("#", filled), strings.Repeat("-", p.width-filled),
		pct, current, p.total, label)
	pad := ""
	if len(line) < p.last {
		pad = strings.Repeat(" ", p.last-len(line))
	}
	p.last = len(line)
	fmt.Fprintf(p.out, "\r%s%s", line, pad)
}

// Println clears the bar line and writes a scrollback line above it. Use it for
// per-item results (failures, updates) that should survive above the bar.
func (p *ProgressBar) Println(a ...any) {
	if p.active && p.last > 0 {
		fmt.Fprintf(p.out, "\r%s\r", strings.Repeat(" ", p.last))
		p.last = 0
	}
	fmt.Fprintln(p.out, a...)
}

// Active reports whether the bar animates in place. When false, callers should
// fall back to plain per-item log lines.
func (p *ProgressBar) Active() bool { return p.active }

// Finish clears the bar line so following output starts clean.
func (p *ProgressBar) Finish() {
	if p.active && p.last > 0 {
		fmt.Fprintf(p.out, "\r%s\r", strings.Repeat(" ", p.last))
		p.last = 0
	}
}

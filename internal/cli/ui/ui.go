// Package ui holds the shared Lipgloss styling, tables and spinners for
// the `oort` CLI. All helpers degrade to plain text when Plain is set
// (via --plain, NO_COLOR, or a non-TTY stdout), so scripts and pipes keep
// working exactly as before.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	ltable "github.com/charmbracelet/lipgloss/table"
	"github.com/muesli/termenv"
)

// Plain disables all color and decoration. Set once from the root command
// (--plain, NO_COLOR, or non-TTY stdout).
var Plain bool

var (
	accent  = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	success = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	failure = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	warn    = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	dim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	code    = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	header  = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	box     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("12")).Padding(0, 2)
)

// Enabled reports whether styled output is active for w (default stdout).
func Enabled() bool {
	if Plain {
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return termenv.DefaultOutput().ColorProfile() != termenv.Ascii
}

func render(s string) string {
	if !Enabled() {
		return s
	}
	return s
}

// Title renders a section heading.
func Title(s string) string { return render(accent.Render(s)) }

// Success renders a green ok line.
func Success(s string) string { return render(success.Render(s)) }

// ErrLine renders a red error line.
func ErrLine(s string) string { return render(failure.Render(s)) }

// Warn renders a yellow warning.
func Warn(s string) string { return render(warn.Render(s)) }

// Dim renders de-emphasized text.
func Dim(s string) string { return render(dim.Render(s)) }

// Code renders a value (URL, id, version) in cyan.
func Code(s string) string { return render(code.Render(s)) }

// Faint is Dim with printf verbs.
func Faint(format string, a ...any) string { return Dim(fmt.Sprintf(format, a...)) }

// Status colorizes well-known status words; unknown words pass through.
func Status(s string) string {
	if !Enabled() {
		return s
	}
	switch strings.ToLower(s) {
	case "active", "warm", "ok", "approved":
		return success.Render(s)
	case "failed", "denied", "expired", "error":
		return failure.Render(s)
	case "cold", "pending", "queued", "-":
		return dim.Render(s)
	default:
		if strings.HasPrefix(strings.ToLower(s), "warmx") {
			return success.Render(s)
		}
		return s
	}
}

// HTTPStatus colorizes an HTTP status code by class.
func HTTPStatus(n int) string {
	s := fmt.Sprintf("%d", n)
	if !Enabled() {
		return s
	}
	switch {
	case n < 400:
		return success.Render(s)
	case n < 500:
		return warn.Render(s)
	default:
		return failure.Render(s)
	}
}

// Panel renders a bordered box (device-code login, one-time keys).
func Panel(body string) string {
	if !Enabled() {
		return body
	}
	return box.Render(body)
}

// Table renders headers + rows as a rounded Lipgloss table. statusCols
// lists column indexes whose cells go through Status(). Falls back to
// aligned plain text when styling is disabled.
func Table(headers []string, rows [][]string, statusCols ...int) string {
	if !Enabled() {
		widths := make([]int, len(headers))
		for i, h := range headers {
			widths[i] = len(h)
		}
		for _, r := range rows {
			for i, c := range r {
				if i < len(widths) && len(c) > widths[i] {
					widths[i] = len(c)
				}
			}
		}
		var b strings.Builder
		for i, h := range headers {
			fmt.Fprintf(&b, "%-*s  ", widths[i], h)
		}
		b.WriteString("\n")
		for _, r := range rows {
			for i := range headers {
				c := ""
				if i < len(r) {
					c = r[i]
				}
				fmt.Fprintf(&b, "%-*s  ", widths[i], c)
			}
			b.WriteString("\n")
		}
		return b.String()
	}
	mark := map[int]bool{}
	for _, c := range statusCols {
		mark[c] = true
	}
	styled := make([][]string, 0, len(rows))
	for _, r := range rows {
		out := make([]string, len(r))
		copy(out, r)
		for c := range out {
			if mark[c] {
				out[c] = Status(out[c])
			}
		}
		styled = append(styled, out)
	}
	hdr := make([]string, len(headers))
	for i, h := range headers {
		hdr[i] = header.Render(h)
	}
	t := ltable.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("8"))).
		Headers(hdr...).
		Rows(styled...)
	return lipgloss.NewStyle().Margin(0, 0, 1, 0).Render(t.Render()) + "\n"
}

// Spinner is a minimal stderr spinner built on bubbles frames. It is a
// no-op when styling is disabled, so non-TTY output stays clean.
type Spinner struct {
	w      io.Writer
	suffix string
	model  spinner.Model
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	msg    string
}

// NewSpinner creates a spinner writing frames to w (usually stderr).
func NewSpinner(w io.Writer, suffix string) *Spinner {
	return &Spinner{
		w:      w,
		suffix: suffix,
		model:  spinner.New(spinner.WithSpinner(spinner.Line)),
		done:   make(chan struct{}),
	}
}

// Start begins frame output. No-op when styling is disabled.
func (s *Spinner) Start() {
	if !Enabled() {
		return
	}
	go func() {
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		frames := s.model.Spinner.Frames
		if len(frames) == 0 {
			frames = []string{"-", "\\", "|", "/"}
		}
		i := 0
		for {
			select {
			case <-s.done:
				return
			case <-t.C:
				s.mu.Lock()
				msg := s.msg
				s.mu.Unlock()
				frame := frames[i%len(frames)]
				i++
				line := frame + " " + msg + s.suffix
				fmt.Fprintf(s.w, "\r\033[K%s", line)
			}
		}
	}()
}

// Message updates the spinner label.
func (s *Spinner) Message(m string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msg = m
}

// Stop clears the spinner line.
func (s *Spinner) Stop() {
	s.once.Do(func() { close(s.done) })
	if Enabled() {
		fmt.Fprint(s.w, "\r\033[K")
	}
}

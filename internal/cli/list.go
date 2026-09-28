package cli

import (
	"fmt"
	"strings"
	"time"

	"actions/internal/deploy"
	"actions/internal/runner"
)

// shortDur renders d compactly: 5s, 3m12s, 2h5m, 4d3h. Negative/zero -> "0s".
func shortDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d / time.Second)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	if m < 60 {
		return fmt.Sprintf("%dm%ds", m, s%60)
	}
	h := m / 60
	if h < 48 {
		return fmt.Sprintf("%dh%dm", h, m%60)
	}
	return fmt.Sprintf("%dd%dh", h/24, h%24)
}

// shortTime renders an RFC3339 timestamp as "01-02 15:04" (UTC), else raw.
func shortTime(raw string) string {
	if raw == "" {
		return "-"
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format("01-02 15:04")
	}
	if len(raw) > 16 {
		return raw[:16]
	}
	return raw
}

// triggers summarizes cron/queue triggers, e.g. "q:orders.created" or "-".
func triggers(c deploy.Config) string {
	var parts []string
	for _, q := range c.Queue {
		parts = append(parts, "q:"+q)
	}
	for _, cr := range c.Cron {
		parts = append(parts, "c:"+cr)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

// FormatFunctions renders `actions ls` rows as an aligned table.
// now is injected for tests.
func FormatFunctions(fns []Function, now time.Time) string {
	rows := make([][]string, 0, len(fns))
	for _, f := range fns {
		cfg, _ := deploy.ParseConfig(f.ConfigTOML) // empty on error; zeros mean defaults
		timeout := runner.DefaultTimeout
		if cfg.TimeoutMs != 0 {
			timeout = time.Duration(cfg.TimeoutMs) * time.Millisecond
		}
		mem := runner.DefaultMemory
		if cfg.MemoryMB != 0 {
			mem = cfg.MemoryMB
		}
		route := cfg.Route
		if route == "" {
			route = "/f/" + f.Name
		}
		egress := "no"
		if cfg.AllowEgress {
			egress = "yes"
		}
		status, uptime, last := "cold", "-", "-"
		if len(f.Warm) > 0 {
			status = "warm"
			if len(f.Warm) > 1 {
				status = fmt.Sprintf("warmx%d", len(f.Warm))
			}
			oldest, newest := f.Warm[0].StartedAt, f.Warm[0].LastUsed
			for _, w := range f.Warm[1:] {
				if w.StartedAt.Before(oldest) {
					oldest = w.StartedAt
				}
				if w.LastUsed.After(newest) {
					newest = w.LastUsed
				}
			}
			if !oldest.IsZero() {
				uptime = shortDur(now.Sub(oldest))
			}
			if !newest.IsZero() {
				last = shortDur(now.Sub(newest)) + " ago"
			}
		}
		rows = append(rows, []string{
			f.Name, f.ActiveVersion, cfg.Runtime, route, status, uptime, last,
			shortTime(f.DeployedAt), fmt.Sprintf("%d", mem), shortDur(timeout), egress, triggers(cfg),
		})
	}
	head := []string{"NAME", "VER", "RUNTIME", "ROUTE", "STATUS", "UPTIME", "LAST USED", "DEPLOYED", "MEM", "TIMEOUT", "EGRESS", "TRIGGERS"}
	widths := make([]int, len(head))
	for i, h := range head {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	var b strings.Builder
	for i, h := range head {
		fmt.Fprintf(&b, "%-*s  ", widths[i], h)
	}
	b.WriteString("\n")
	for _, r := range rows {
		for i, c := range r {
			fmt.Fprintf(&b, "%-*s  ", widths[i], c)
		}
		b.WriteString("\n")
	}
	return b.String()
}

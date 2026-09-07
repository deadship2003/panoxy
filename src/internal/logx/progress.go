// Progress display: a single-line refreshing progress bar under a TTY, milestone lines
// when not a TTY (so pipes and logs stay clean).
package logx

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Progress renders download/copy progress.
type Progress struct {
	label string
	total int64
	n     int64 // actual bytes transferred (when total is unknown, i.e. Content-Length=-1, completion info is based on this)
	last  time.Time
	marks map[int]bool // non-TTY milestones
	isTTY bool
}

func NewProgress(label string, total int64) *Progress {
	fi, err := os.Stderr.Stat()
	isTTY := err == nil && fi.Mode()&os.ModeCharDevice != 0
	return &Progress{label: label, total: total, isTTY: isTTY, marks: map[int]bool{}}
}

// Update reports the number of bytes transferred so far.
func (p *Progress) Update(n int64) {
	if p == nil {
		return
	}
	p.n = n
	now := time.Now()
	if p.isTTY {
		if now.Sub(p.last) < 100*time.Millisecond {
			return // throttled to 100ms
		}
		p.last = now
		fmt.Fprint(os.Stderr, "\r"+p.render(n))
		return
	}
	if p.total <= 0 {
		return
	}
	pct := int(n * 100 / p.total)
	for _, m := range []int{0, 25, 50, 75} {
		if pct >= m && !p.marks[m] {
			p.marks[m] = true
			Info("%s: %d%% (%s)", p.label, pct, humanBytes(n, p.total))
		}
	}
}

// Done finishes (must be called on both success and failure; completes the line/endpoint).
func (p *Progress) Done(err error) {
	if p == nil {
		return
	}
	if p.isTTY {
		fmt.Fprint(os.Stderr, "\r"+p.render(p.n)+"\n")
	}
	if err != nil {
		Warn("%s failed: %v", p.label, err)
	} else {
		Info("%s done (%s)", p.label, humanBytes(p.n, p.total))
	}
}

func (p *Progress) render(n int64) string {
	if p.total <= 0 {
		// Total unknown (server chunked transfer / proxy stripped Content-Length):
		// report actual bytes only, never draw a fake progress bar.
		return fmt.Sprintf("  %s %s", p.label, humanBytes(n, p.total))
	}
	pct := int(n * 100 / p.total)
	if pct > 100 {
		pct = 100
	}
	const width = 24
	filled := pct * width / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("─", width-filled)
	return fmt.Sprintf("  %s %3d%% [%s] %s", p.label, pct, bar, humanBytes(n, p.total))
}

func humanBytes(n, total int64) string {
	f := func(v int64) string {
		switch {
		case v >= 1<<20:
			return fmt.Sprintf("%.1fMB", float64(v)/(1<<20))
		case v >= 1<<10:
			return fmt.Sprintf("%.0fKB", float64(v)/(1<<10))
		default:
			return fmt.Sprintf("%dB", v)
		}
	}
	if total > 0 {
		return f(n) + "/" + f(total)
	}
	return f(n)
}

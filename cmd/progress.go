package cmd

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

const barWidth = 20

var isTTY = term.IsTerminal(int(os.Stdout.Fd()))

type progressLine struct {
	name   string
	status string // "uploading", "downloading", "done", "failed"
	pct    float64
	size   string
}

func renderProgress(lines []progressLine) {
	if !isTTY {
		// Non-interactive: print only final states, one line each, no ANSI
		for _, l := range lines {
			if l.status == "done" || l.status == "failed" || l.status == "unchanged" ||
				l.status == "CONFLICT" || l.status == "linked" || l.status == "renamed" ||
				l.status == "too large" {
				status := l.status
				if l.size != "" {
					status = fmt.Sprintf("%s (%s)", l.status, l.size)
				}
				fmt.Printf("  %s  %s\n", l.name, status)
			}
		}
		return
	}

	// Move cursor up to overwrite previous render
	if len(lines) > 0 {
		fmt.Printf("\033[%dA", len(lines))
	}

	maxName := 0
	for _, l := range lines {
		if len(l.name) > maxName {
			maxName = len(l.name)
		}
	}

	for _, l := range lines {
		bar := renderBar(l.pct)
		status := l.status
		if l.size != "" {
			status = fmt.Sprintf("%s (%s)", l.status, l.size)
		}

		// Clear line + write
		fmt.Printf("\033[2K  %-*s  %s  %s\n", maxName, l.name, bar, status)
	}
}

func renderBar(pct float64) string {
	filled := int(pct * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	empty := barWidth - filled
	return fmt.Sprintf("%s%s", strings.Repeat("█", filled), strings.Repeat("░", empty))
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

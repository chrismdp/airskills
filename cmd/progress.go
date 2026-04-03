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
	name    string
	status  string // "uploading", "downloading", "done", "failed"
	pct     float64
	size    string
	printed bool // piped mode: already printed final state
}

var finalStatuses = map[string]bool{
	"done": true, "failed": true, "unchanged": true,
	"CONFLICT": true, "linked": true, "renamed": true, "too large": true,
}

func renderProgress(lines []progressLine) {
	if !isTTY {
		for i := range lines {
			if finalStatuses[lines[i].status] && !lines[i].printed {
				status := lines[i].status
				if lines[i].size != "" {
					status = fmt.Sprintf("%s (%s)", lines[i].status, lines[i].size)
				}
				fmt.Printf("  %s  %s\n", lines[i].name, status)
				lines[i].printed = true
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

		// Colour the status
		coloredStatus := status
		switch l.status {
		case "done", "linked", "renamed":
			coloredStatus = green(status)
		case "unchanged":
			coloredStatus = dim(status)
		case "failed", "CONFLICT", "too large":
			coloredStatus = red(status)
		case "uploading", "downloading", "compressing", "creating":
			coloredStatus = cyan(status)
		}

		// Clear line + write
		fmt.Printf("\033[2K  %-*s  %s  %s\n", maxName, l.name, bar, coloredStatus)
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

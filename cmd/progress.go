package cmd

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

const barWidth = 20

var isTTY = term.IsTerminal(int(os.Stdout.Fd()))
var verbose bool // set by sync --verbose; push/pull check this

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
		// Piped: print final states only, once each
		for i := range lines {
			if finalStatuses[lines[i].status] && !lines[i].printed {
				status := lines[i].status
				if lines[i].size != "" {
					status = fmt.Sprintf("%s (%s)", lines[i].status, lines[i].size)
				}
				if verbose {
					fmt.Printf("  %s  %s\n", lines[i].name, status)
				}
				lines[i].printed = true
			}
		}
		return
	}

	if !verbose {
		// Compact: single updating counter line
		var done, total int
		for _, l := range lines {
			total++
			if finalStatuses[l.status] {
				done++
			}
		}
		fmt.Printf("\033[1A\033[2K  %s %d/%d skills\n", dim("·"), done, total)
		return
	}

	// Verbose: full per-skill progress with cursor movement
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

package cmd

import "fmt"

// ANSI color helpers — only emit codes when isTTY is true.

func cyan(s string) string {
	if !isTTY {
		return s
	}
	return fmt.Sprintf("\033[36m%s\033[0m", s)
}

func green(s string) string {
	if !isTTY {
		return s
	}
	return fmt.Sprintf("\033[32m%s\033[0m", s)
}

func yellow(s string) string {
	if !isTTY {
		return s
	}
	return fmt.Sprintf("\033[33m%s\033[0m", s)
}

func red(s string) string {
	if !isTTY {
		return s
	}
	return fmt.Sprintf("\033[31m%s\033[0m", s)
}

func dim(s string) string {
	if !isTTY {
		return s
	}
	return fmt.Sprintf("\033[2m%s\033[0m", s)
}

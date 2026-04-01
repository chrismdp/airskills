package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chrismdp/airskills/config"
)

var logFile *os.File

func initLogging() {
	dir, err := config.Dir()
	if err != nil {
		return
	}

	logsDir := filepath.Join(dir, "logs")
	os.MkdirAll(logsDir, 0700)

	// One log file per day
	name := time.Now().Format("2006-01-02") + ".log"
	path := filepath.Join(logsDir, name)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	logFile = f

	// Clean up old logs (keep last 7 days)
	entries, _ := os.ReadDir(logsDir)
	if len(entries) > 7 {
		for _, e := range entries[:len(entries)-7] {
			os.Remove(filepath.Join(logsDir, e.Name()))
		}
	}
}

func logInfo(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(msg)
	if logFile != nil {
		fmt.Fprintf(logFile, "%s [INFO] %s\n", time.Now().Format(time.RFC3339), msg)
	}
}

func logError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	if logFile != nil {
		fmt.Fprintf(logFile, "%s [ERROR] %s\n", time.Now().Format(time.RFC3339), msg)
	}
}

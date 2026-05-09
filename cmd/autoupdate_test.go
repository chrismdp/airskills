package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsAutoUpdateSafePath_AcceptsUserDir(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "airskills")
	if err := os.WriteFile(binPath, []byte("dummy"), 0755); err != nil {
		t.Fatal(err)
	}
	if !isAutoUpdateSafePath(binPath) {
		t.Errorf("user tmpdir %s should be safe; got false", dir)
	}
}

func TestIsAutoUpdateSafePath_RejectsSystemPaths(t *testing.T) {
	cases := []string{
		"/usr/bin/airskills",
		"/usr/local/bin/airskills",
		"/opt/homebrew/bin/airskills",
		"/opt/homebrew/Cellar/airskills/0.5.30/bin/airskills",
		"/snap/bin/airskills",
		"/Applications/Airskills.app/Contents/MacOS/airskills",
	}
	for _, p := range cases {
		if isAutoUpdateSafePath(p) {
			t.Errorf("%s: expected unsafe (system path), got safe", p)
		}
	}
}

func TestIsAutoUpdateSafePath_RejectsReadOnlyParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0555 doesn't reliably block writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can always write — chmod test irrelevant")
	}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "airskills")
	if err := os.WriteFile(binPath, []byte("dummy"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0755) })
	if isAutoUpdateSafePath(binPath) {
		t.Errorf("read-only parent should be unsafe; got safe")
	}
}

func TestClassifyUpdateError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("checksum mismatch: expected X, got Y"), "checksum mismatch"},
		{errors.New("extraction failed: bad gzip"), "extraction error"},
		{errors.New("cannot write new binary (try: sudo airskills self-update)"), "permission denied"},
		{errors.New("cannot replace binary"), "permission denied"},
		{errors.New("download failed: connection refused"), "network error"},
		{errors.New("GitHub API returned 503"), "network error"},
		{errors.New("Get \"https://api.github.com/...\": dial tcp: lookup api.github.com: no such host"), "network error"},
		{errors.New("context deadline exceeded"), "network error"},
		{errors.New("something completely unexpected"), "error"},
		{nil, "ok"},
	}
	for _, c := range cases {
		got := classifyUpdateError(c.err)
		if got != c.want {
			label := "<nil>"
			if c.err != nil {
				label = c.err.Error()
			}
			t.Errorf("classifyUpdateError(%q) = %q, want %q", label, got, c.want)
		}
	}
}

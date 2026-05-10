package cmd

import (
	"strings"
	"testing"
)

func TestRootHelpMentionsFeedback(t *testing.T) {
	long := rootCmd.Long
	if !strings.Contains(long, "airskills feedback") {
		t.Errorf("rootCmd.Long should mention `airskills feedback` so new users know how to send feedback / report a bug; got:\n%s", long)
	}
}

package cmd

import "testing"

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{2621440, "2.5 MB"},
	}

	for _, tt := range tests {
		got := formatSize(tt.bytes)
		if got != tt.expected {
			t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.expected)
		}
	}
}

func TestRenderBar(t *testing.T) {
	bar0 := renderBar(0)
	if len(bar0) == 0 {
		t.Error("renderBar(0) should not be empty")
	}

	bar100 := renderBar(1.0)
	if len(bar100) == 0 {
		t.Error("renderBar(1.0) should not be empty")
	}

	// Bar at 50% should have some filled and some empty
	bar50 := renderBar(0.5)
	if bar50 == bar0 || bar50 == bar100 {
		t.Errorf("renderBar(0.5) should differ from 0%% and 100%%")
	}
}

package cmd

import "os"

// fileMode returns the permission bits to use when materialising a downloaded
// skill file to disk. Files whose contents start with a `#!` shebang are
// treated as executable scripts and written 0755; everything else is 0644.
//
// This is a heuristic — the platform's tar payload currently doesn't carry
// per-file mode bits, so we infer intent from content. Covers the canonical
// case of skills shipping runnable bash/python helpers (e.g. `ralph.sh`).
func fileMode(content []byte) os.FileMode {
	if isExecutableContent(content) {
		return 0755
	}
	return 0644
}

func isExecutableContent(content []byte) bool {
	return len(content) >= 2 && content[0] == '#' && content[1] == '!'
}

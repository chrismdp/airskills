package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type agentDef struct {
	Key        string
	Name       string
	ProjectDir string // relative to project root
	GlobalDir  string // relative to home dir (unix), or absolute pattern
}

// Agent registry — mirrors vercel-labs/skills agent paths
var agents = []agentDef{
	{"claude-code", "Claude Code", ".claude/skills", ".claude/skills"},
	{"cursor", "Cursor", ".agents/skills", ".cursor/skills"},
	{"github-copilot", "GitHub Copilot", ".agents/skills", ".copilot/skills"},
	{"windsurf", "Windsurf", ".windsurf/skills", ".codeium/windsurf/skills"},
	{"codex", "Codex", ".agents/skills", ".codex/skills"},
	{"cline", "Cline", ".agents/skills", ".agents/skills"},
	{"roo", "Roo Code", ".roo/skills", ".roo/skills"},
	{"continue", "Continue", ".continue/skills", ".continue/skills"},
	{"gemini-cli", "Gemini CLI", ".agents/skills", ".gemini/skills"},
	{"augment", "Augment", ".augment/skills", ".augment/skills"},
	{"kiro-cli", "Kiro CLI", ".kiro/skills", ".kiro/skills"},
	{"junie", "Junie", ".junie/skills", ".junie/skills"},
	{"goose", "Goose", ".goose/skills", ".config/goose/skills"},
	{"trae", "Trae", ".trae/skills", ".trae/skills"},
	{"amp", "Amp", ".agents/skills", ".config/agents/skills"},
	{"opencode", "OpenCode", ".agents/skills", ".config/opencode/skills"},
	{"aider", "Aider", ".agents/skills", ".aider/skills"},
	{"amazon-q", "Amazon Q", ".amazonq/skills", ".amazonq/skills"},
}

// detectInstalledAgents returns agents whose global skills directory exists
func detectInstalledAgents() []agentDef {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var found []agentDef
	seen := map[string]bool{} // dedupe by global dir

	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seen[globalPath] {
			continue
		}

		// Check if the parent dir exists (e.g. ~/.claude/ for claude-code)
		parent := filepath.Dir(globalPath)
		if _, err := os.Stat(parent); err == nil {
			found = append(found, a)
			seen[globalPath] = true
		}
	}

	return found
}

// installSkillToAgents writes a skill folder to all detected agents
func installSkillToAgents(slug string, files map[string][]byte) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	detected := detectInstalledAgents()
	if len(detected) == 0 {
		// Fallback to Claude Code only
		detected = []agentDef{agents[0]}
	}

	var installed []string
	seen := map[string]bool{}

	for _, a := range detected {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seen[globalPath] {
			continue
		}
		seen[globalPath] = true

		skillDir := filepath.Join(globalPath, slug)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			continue
		}

		for name, content := range files {
			target := filepath.Join(skillDir, name)
			os.MkdirAll(filepath.Dir(target), 0755)
			if err := os.WriteFile(target, content, 0644); err != nil {
				continue
			}
		}

		installed = append(installed, fmt.Sprintf("  → %-16s %s", a.Name, skillDir))
	}

	return installed, nil
}

// scanSkillsFromAgents finds all local skills across all detected agents
func scanSkillsFromAgents() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// map of slug -> path (first found wins)
	skills := map[string]string{}
	seen := map[string]bool{}

	for _, a := range agents {
		globalPath := resolveGlobalDir(home, a.GlobalDir)
		if seen[globalPath] {
			continue
		}
		seen[globalPath] = true

		entries, err := os.ReadDir(globalPath)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillMd := filepath.Join(globalPath, e.Name(), "SKILL.md")
			if _, err := os.Stat(skillMd); err == nil {
				if _, exists := skills[e.Name()]; !exists {
					skills[e.Name()] = filepath.Join(globalPath, e.Name())
				}
			}
		}
	}

	return skills, nil
}

func resolveGlobalDir(home, relDir string) string {
	if runtime.GOOS == "windows" {
		// On Windows, use %USERPROFILE% (same as home)
		return filepath.Join(home, relDir)
	}
	return filepath.Join(home, relDir)
}

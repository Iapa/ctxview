package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	CategoryCoding  = "coding"
	CategoryLobster = "lobster"
)

// ToolAdapter describes an AI agent's global skills directory and detection rules.
type ToolAdapter struct {
	Key                      string   `json:"key"`
	DisplayName              string   `json:"display_name"`
	RelativeSkillsDir        string   `json:"relative_skills_dir"`
	ProjectRelativeSkillsDir string   `json:"project_relative_skills_dir,omitempty"`
	RelativeDetectDir        string   `json:"relative_detect_dir"`
	AdditionalScanDirs       []string `json:"additional_scan_dirs,omitempty"`
	OverrideSkillsDir        string   `json:"override_skills_dir,omitempty"`
	IsCustom                 bool     `json:"is_custom"`
	RecursiveScan            bool     `json:"recursive_scan"`
	Category                 string   `json:"category"`
	Icon                     string   `json:"icon,omitempty"`
}

// CustomToolDef is a user-defined agent persisted in skills_state.json.
type CustomToolDef struct {
	Key                      string `json:"key"`
	DisplayName              string `json:"display_name"`
	SkillsDir                string `json:"skills_dir"`
	ProjectRelativeSkillsDir string `json:"project_relative_skills_dir,omitempty"`
	Category                 string `json:"category,omitempty"`
	RecursiveScan            bool   `json:"recursive_scan,omitempty"`
}

func candidatePaths(homeDir, configDir, relative string) []string {
	if relative == "" {
		return nil
	}
	candidates := []string{filepath.Join(homeDir, filepath.FromSlash(relative))}
	if suffix, ok := strings.CutPrefix(relative, ".config/"); ok && configDir != "" {
		cfgPath := filepath.Join(configDir, filepath.FromSlash(suffix))
		if !slices.Contains(candidates, cfgPath) {
			candidates = append(candidates, cfgPath)
		}
	}
	return candidates
}

// SkillsDir resolves the global skills directory for this adapter.
func (a ToolAdapter) SkillsDir(homeDir, configDir string) string {
	if a.OverrideSkillsDir != "" {
		return expandPathWithHome(a.OverrideSkillsDir, homeDir)
	}
	candidates := candidatePaths(homeDir, configDir, a.RelativeSkillsDir)
	if len(candidates) == 0 {
		return ""
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return candidates[0]
}

// ProjectSkillsRelDir returns the project-relative skills directory (falling back to RelativeSkillsDir).
func (a ToolAdapter) ProjectSkillsRelDir() string {
	if strings.TrimSpace(a.ProjectRelativeSkillsDir) != "" {
		return strings.TrimSpace(a.ProjectRelativeSkillsDir)
	}
	return a.RelativeSkillsDir
}

// IsInstalled reports whether this agent is detected on the current machine.
func (a ToolAdapter) IsInstalled(homeDir, configDir string) bool {
	if a.IsCustom || a.OverrideSkillsDir != "" {
		return true
	}
	for _, p := range candidatePaths(homeDir, configDir, a.RelativeDetectDir) {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// AllScanDirs returns the primary skills dir plus any existing additional discovery roots.
func (a ToolAdapter) AllScanDirs(homeDir, configDir string) []string {
	var dirs []string
	if primary := a.SkillsDir(homeDir, configDir); primary != "" {
		dirs = append(dirs, primary)
	}
	for _, rel := range a.AdditionalScanDirs {
		for _, c := range candidatePaths(homeDir, configDir, rel) {
			if _, err := os.Stat(c); err == nil && !slices.Contains(dirs, c) {
				dirs = append(dirs, c)
			}
		}
	}
	return dirs
}

func expandPathWithHome(p, homeDir string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" {
		if homeDir != "" {
			return filepath.Clean(homeDir)
		}
		return expandPath(p)
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if homeDir != "" {
			return filepath.Clean(filepath.Join(homeDir, p[2:]))
		}
		return expandPath(p)
	}
	return filepath.Clean(p)
}

// DefaultToolAdapters returns all 56 built-in tool adapters mirroring src-tauri/src/core/tool_adapters.rs.
func DefaultToolAdapters() []ToolAdapter {
	return []ToolAdapter{
		{
			Key:               "cursor",
			DisplayName:       "Cursor",
			RelativeSkillsDir: ".cursor/skills",
			RelativeDetectDir: ".cursor",
			Category:          CategoryCoding,
			Icon:              "cursor",
		},
		{
			Key:               "claude_code",
			DisplayName:       "Claude Code",
			RelativeSkillsDir: ".claude/skills",
			RelativeDetectDir: ".claude",
			Category:          CategoryCoding,
			Icon:              "claudecode-color",
		},
		{
			Key:                      "omp_agent",
			DisplayName:              "OMP Agent",
			RelativeSkillsDir:        ".omp/agent/skills",
			ProjectRelativeSkillsDir: ".omp/skills",
			RelativeDetectDir:        ".omp/agent",
			Category:                 CategoryCoding,
			Icon:                     "omp",
		},
		{
			Key:                "codex",
			DisplayName:        "Codex",
			RelativeSkillsDir:  ".codex/skills",
			RelativeDetectDir:  ".codex",
			AdditionalScanDirs: []string{".agents/skills"},
			Category:           CategoryCoding,
			Icon:               "codex-color",
		},
		{
			Key:               "grok",
			DisplayName:       "Grok",
			RelativeSkillsDir: ".grok/skills",
			RelativeDetectDir: ".grok",
			Category:          CategoryCoding,
			Icon:              "grok",
		},
		{
			Key:                      "opencode",
			DisplayName:              "OpenCode",
			RelativeSkillsDir:        ".config/opencode/skills",
			ProjectRelativeSkillsDir: ".opencode/skills",
			RelativeDetectDir:        ".config/opencode",
			Category:                 CategoryCoding,
			Icon:                     "opencode",
		},
		{
			Key:                      "antigravity",
			DisplayName:              "Antigravity",
			RelativeSkillsDir:        ".gemini/config/skills",
			ProjectRelativeSkillsDir: ".agents/skills",
			RelativeDetectDir:        ".gemini/antigravity",
			Category:                 CategoryCoding,
			Icon:                     "antigravity-color",
		},
		{
			Key:               "amp",
			DisplayName:       "Amp",
			RelativeSkillsDir: ".config/agents/skills",
			RelativeDetectDir: ".config/agents",
			Category:          CategoryCoding,
		},
		{
			Key:               "kilo_code",
			DisplayName:       "Kilo Code",
			RelativeSkillsDir: ".kilocode/skills",
			RelativeDetectDir: ".kilocode",
			Category:          CategoryCoding,
			Icon:              "kilocode",
		},
		{
			Key:               "roo_code",
			DisplayName:       "Roo Code",
			RelativeSkillsDir: ".roo/skills",
			RelativeDetectDir: ".roo",
			Category:          CategoryCoding,
			Icon:              "roocode",
		},
		{
			Key:               "goose",
			DisplayName:       "Goose",
			RelativeSkillsDir: ".config/goose/skills",
			RelativeDetectDir: ".config/goose",
			Category:          CategoryCoding,
			Icon:              "goose",
		},
		{
			Key:               "gemini_cli",
			DisplayName:       "Gemini CLI",
			RelativeSkillsDir: ".gemini/skills",
			RelativeDetectDir: ".gemini",
			Category:          CategoryCoding,
			Icon:              "geminicli-color",
		},
		{
			Key:                "github_copilot",
			DisplayName:        "GitHub Copilot",
			RelativeSkillsDir:  ".copilot/skills",
			RelativeDetectDir:  ".copilot",
			AdditionalScanDirs: []string{".agents/skills"},
			Category:           CategoryCoding,
			Icon:               "githubcopilot",
		},
		{
			Key:               "openclaw",
			DisplayName:       "OpenClaw",
			RelativeSkillsDir: ".openclaw/skills",
			RelativeDetectDir: ".openclaw",
			Category:          CategoryLobster,
			Icon:              "openclaw",
		},
		{
			Key:               "droid",
			DisplayName:       "Droid",
			RelativeSkillsDir: ".factory/skills",
			RelativeDetectDir: ".factory",
			Category:          CategoryCoding,
			Icon:              "factory",
		},
		{
			Key:               "windsurf",
			DisplayName:       "Windsurf",
			RelativeSkillsDir: ".codeium/windsurf/skills",
			RelativeDetectDir: ".codeium/windsurf",
			Category:          CategoryCoding,
			Icon:              "windsurf",
		},
		{
			Key:               "trae",
			DisplayName:       "TRAE IDE",
			RelativeSkillsDir: ".trae/skills",
			RelativeDetectDir: ".trae",
			Category:          CategoryCoding,
			Icon:              "trae",
		},
		{
			Key:               "cline",
			DisplayName:       "Cline",
			RelativeSkillsDir: ".agents/skills",
			RelativeDetectDir: ".cline",
			Category:          CategoryCoding,
			Icon:              "cline",
		},
		{
			Key:               "deepagents",
			DisplayName:       "Deep Agents",
			RelativeSkillsDir: ".deepagents/agent/skills",
			RelativeDetectDir: ".deepagents",
			Category:          CategoryCoding,
		},
		{
			Key:               "firebender",
			DisplayName:       "Firebender",
			RelativeSkillsDir: ".firebender/skills",
			RelativeDetectDir: ".firebender",
			Category:          CategoryCoding,
		},
		{
			Key:                      "kimi",
			DisplayName:              "Kimi Code CLI",
			RelativeSkillsDir:        ".kimi-code/skills",
			ProjectRelativeSkillsDir: ".kimi-code/skills",
			RelativeDetectDir:        ".kimi-code",
			Category:                 CategoryCoding,
			Icon:                     "kimi",
		},
		{
			Key:               "replit",
			DisplayName:       "Replit",
			RelativeSkillsDir: ".config/agents/skills",
			RelativeDetectDir: ".replit",
			Category:          CategoryCoding,
		},
		{
			Key:               "warp",
			DisplayName:       "Warp",
			RelativeSkillsDir: ".agents/skills",
			RelativeDetectDir: ".warp",
			Category:          CategoryCoding,
		},
		{
			Key:               "augment",
			DisplayName:       "Augment",
			RelativeSkillsDir: ".augment/skills",
			RelativeDetectDir: ".augment",
			Category:          CategoryCoding,
		},
		{
			Key:               "bob",
			DisplayName:       "IBM Bob",
			RelativeSkillsDir: ".bob/skills",
			RelativeDetectDir: ".bob",
			Category:          CategoryCoding,
			Icon:              "bob-color",
		},
		{
			Key:               "codebuddy",
			DisplayName:       "CodeBuddy",
			RelativeSkillsDir: ".codebuddy/skills",
			RelativeDetectDir: ".codebuddy",
			Category:          CategoryCoding,
			Icon:              "codebuddy-color",
		},
		{
			Key:               "command_code",
			DisplayName:       "Command Code",
			RelativeSkillsDir: ".commandcode/skills",
			RelativeDetectDir: ".commandcode",
			Category:          CategoryCoding,
			Icon:              "commandcode",
		},
		{
			Key:               "continue",
			DisplayName:       "Continue",
			RelativeSkillsDir: ".continue/skills",
			RelativeDetectDir: ".continue",
			Category:          CategoryCoding,
		},
		{
			Key:               "cortex",
			DisplayName:       "Cortex Code",
			RelativeSkillsDir: ".snowflake/cortex/skills",
			RelativeDetectDir: ".snowflake/cortex",
			Category:          CategoryCoding,
			Icon:              "snowflake-color",
		},
		{
			Key:               "crush",
			DisplayName:       "Crush",
			RelativeSkillsDir: ".config/crush/skills",
			RelativeDetectDir: ".config/crush",
			Category:          CategoryCoding,
		},
		{
			Key:               "iflow",
			DisplayName:       "iFlow CLI",
			RelativeSkillsDir: ".iflow/skills",
			RelativeDetectDir: ".iflow",
			Category:          CategoryCoding,
		},
		{
			Key:               "junie",
			DisplayName:       "Junie",
			RelativeSkillsDir: ".junie/skills",
			RelativeDetectDir: ".junie",
			Category:          CategoryCoding,
		},
		{
			Key:               "kiro",
			DisplayName:       "Kiro CLI",
			RelativeSkillsDir: ".kiro/skills",
			RelativeDetectDir: ".kiro",
			Category:          CategoryCoding,
			Icon:              "kiro-color",
		},
		{
			Key:               "kode",
			DisplayName:       "Kode",
			RelativeSkillsDir: ".kode/skills",
			RelativeDetectDir: ".kode",
			Category:          CategoryCoding,
		},
		{
			Key:               "mcpjam",
			DisplayName:       "MCPJam",
			RelativeSkillsDir: ".mcpjam/skills",
			RelativeDetectDir: ".mcpjam",
			Category:          CategoryCoding,
		},
		{
			Key:               "mistral_vibe",
			DisplayName:       "Mistral Vibe",
			RelativeSkillsDir: ".vibe/skills",
			RelativeDetectDir: ".vibe",
			Category:          CategoryCoding,
			Icon:              "mistral-color",
		},
		{
			Key:               "mux",
			DisplayName:       "Mux",
			RelativeSkillsDir: ".mux/skills",
			RelativeDetectDir: ".mux",
			Category:          CategoryCoding,
		},
		{
			Key:               "neovate",
			DisplayName:       "Neovate",
			RelativeSkillsDir: ".neovate/skills",
			RelativeDetectDir: ".neovate",
			Category:          CategoryCoding,
		},
		{
			Key:               "openhands",
			DisplayName:       "OpenHands",
			RelativeSkillsDir: ".openhands/skills",
			RelativeDetectDir: ".openhands",
			Category:          CategoryCoding,
		},
		{
			Key:                      "pi",
			DisplayName:              "Pi",
			RelativeSkillsDir:        ".pi/agent/skills",
			ProjectRelativeSkillsDir: ".pi/skills",
			RelativeDetectDir:        ".pi/agent",
			AdditionalScanDirs:       []string{".agents/skills"},
			Category:                 CategoryCoding,
			Icon:                     "pi",
		},
		{
			Key:               "pochi",
			DisplayName:       "Pochi",
			RelativeSkillsDir: ".pochi/skills",
			RelativeDetectDir: ".pochi",
			Category:          CategoryCoding,
		},
		{
			Key:               "qoder",
			DisplayName:       "Qoder",
			RelativeSkillsDir: ".qoder/skills",
			RelativeDetectDir: ".qoder",
			Category:          CategoryCoding,
			Icon:              "qoder",
		},
		{
			Key:               "qwen_code",
			DisplayName:       "Qwen Code",
			RelativeSkillsDir: ".qwen/skills",
			RelativeDetectDir: ".qwen",
			Category:          CategoryCoding,
			Icon:              "qwen-color",
		},
		{
			Key:                      "trae_cn",
			DisplayName:              "TRAE CN",
			RelativeSkillsDir:        ".trae-cn/skills",
			ProjectRelativeSkillsDir: ".trae/skills",
			RelativeDetectDir:        ".trae-cn",
			Category:                 CategoryCoding,
			Icon:                     "trae",
		},
		{
			Key:               "zencoder",
			DisplayName:       "Zencoder",
			RelativeSkillsDir: ".zencoder/skills",
			RelativeDetectDir: ".zencoder",
			Category:          CategoryCoding,
		},
		{
			Key:               "zcode",
			DisplayName:       "ZCode",
			RelativeSkillsDir: ".zcode/skills",
			RelativeDetectDir: ".zcode",
			Category:          CategoryCoding,
		},
		{
			Key:               "adal",
			DisplayName:       "AdaL",
			RelativeSkillsDir: ".adal/skills",
			RelativeDetectDir: ".adal",
			Category:          CategoryCoding,
		},
		{
			Key:               "hermes",
			DisplayName:       "Hermes Agent",
			RelativeSkillsDir: ".hermes/skills",
			RelativeDetectDir: ".hermes",
			RecursiveScan:     true,
			Category:          CategoryLobster,
			Icon:              "hermes",
		},
		{
			Key:               "qclaw",
			DisplayName:       "QClaw",
			RelativeSkillsDir: ".qclaw/skills",
			RelativeDetectDir: ".qclaw",
			Category:          CategoryLobster,
		},
		{
			Key:               "easyclaw",
			DisplayName:       "EasyClaw",
			RelativeSkillsDir: ".easyclaw/skills",
			RelativeDetectDir: ".easyclaw",
			Category:          CategoryLobster,
		},
		{
			Key:               "autoclaw",
			DisplayName:       "AutoClaw",
			RelativeSkillsDir: ".openclaw-autoclaw/skills",
			RelativeDetectDir: ".openclaw-autoclaw",
			Category:          CategoryLobster,
		},
		{
			Key:               "workbuddy",
			DisplayName:       "WorkBuddy",
			RelativeSkillsDir: ".workbuddy/skills",
			RelativeDetectDir: ".workbuddy",
			Category:          CategoryLobster,
			Icon:              "workbuddy-color",
		},
		{
			Key:               "workbuddy_overseas",
			DisplayName:       "WorkBuddy AI",
			RelativeSkillsDir: ".workbuddy-ai/skills",
			RelativeDetectDir: ".workbuddy-ai",
			Category:          CategoryLobster,
			Icon:              "workbuddy-color",
		},
		{
			Key:                "deepseek_harness",
			DisplayName:        "DeepSeek Harness",
			RelativeSkillsDir:  ".dsh/skills",
			RelativeDetectDir:  ".dsh",
			AdditionalScanDirs: []string{".agents/skills"},
			Category:           CategoryCoding,
			Icon:               "deepseek-color",
		},
		{
			Key:                      "gitlab_duo",
			DisplayName:              "GitLab Duo",
			RelativeSkillsDir:        ".gitlab/duo/skills",
			ProjectRelativeSkillsDir: ".agents/skills",
			RelativeDetectDir:        ".gitlab/duo",
			AdditionalScanDirs:       []string{".agents/skills"},
			Category:                 CategoryCoding,
		},
		{
			Key:                "minimax_code",
			DisplayName:        "MiniMax Code",
			RelativeSkillsDir:  ".minimax/skills",
			RelativeDetectDir:  ".minimax",
			AdditionalScanDirs: []string{".agents/skills"},
			Category:           CategoryCoding,
			Icon:               "minimax-color",
		},
	}
}

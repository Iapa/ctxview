package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// AgentSkill represents one skill discovered inside an agent's global skills directory.
type AgentSkill struct {
	Name             string   `json:"name"`
	DirName          string   `json:"dir_name"`
	RelativePath     string   `json:"relative_path"`
	Description      string   `json:"description,omitempty"`
	Path             string   `json:"path"`
	Files            []string `json:"files"`
	Enabled          bool     `json:"enabled"`
	Agent            string   `json:"agent"`
	AgentDisplayName string   `json:"agent_display_name"`
	Tags             []string `json:"tags"`
	InCenter         bool     `json:"in_center"`
	Managed          bool     `json:"managed"`
	SyncStatus       string   `json:"sync_status"`
	CenterSkillID    string   `json:"center_skill_id,omitempty"`
	IsSymlink        bool     `json:"is_symlink"`
	LinkTarget       string   `json:"link_target,omitempty"`
	ContentHash      string   `json:"-"`
	LastModifiedMs   int64    `json:"last_modified_ms,omitempty"`
}

var skillDirMarkers = []string{"SKILL.md", "skill.md"}
var skillDocCandidates = []string{"SKILL.md", "skill.md", "CLAUDE.md", "README.md"}

var ignoredContentNames = map[string]bool{
	".git":        true,
	".DS_Store":   true,
	"Thumbs.db":   true,
	".gitignore":  true,
	"__pycache__": true,
}

var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

type contentEntry struct {
	relPath    string
	absPath    string
	execBits   uint32
	modifiedMs int64
}

func isIgnoredContentName(name string) bool {
	return ignoredContentNames[name] || strings.HasSuffix(name, ".pyc")
}

func isDirFollow(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func hasNamedFileExact(dir, targetName string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.Name() != targetName {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		st, err := os.Stat(full)
		if err == nil && !st.IsDir() {
			return full, true
		}
	}
	return "", false
}

func isValidSkillDir(dir string) bool {
	if !isDirFollow(dir) {
		return false
	}
	for _, marker := range skillDirMarkers {
		if _, ok := hasNamedFileExact(dir, marker); ok {
			return true
		}
	}
	return false
}

func parseSkillMD(dir string) (name, description string) {
	for _, marker := range skillDirMarkers {
		if full, ok := hasNamedFileExact(dir, marker); ok {
			data, err := os.ReadFile(full)
			if err != nil {
				return "", ""
			}
			return parseFrontmatter(string(data))
		}
	}
	return "", ""
}

func unquoteYAMLValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			inner := v[1 : len(v)-1]
			if v[0] == '"' {
				inner = strings.ReplaceAll(inner, `\"`, `"`)
				inner = strings.ReplaceAll(inner, `\\`, `\`)
			} else {
				inner = strings.ReplaceAll(inner, `''`, `'`)
			}
			return inner
		}
	}
	return v
}

func parseFrontmatter(content string) (name, description string) {
	content = strings.TrimPrefix(content, "\uFEFF")
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---") {
		return "", ""
	}
	lines := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return "", ""
	}

	fmLines := lines[1:endIdx]
	for i := 0; i < len(fmLines); i++ {
		line := fmLines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])

		if key != "name" && key != "description" {
			continue
		}

		// Handle YAML block scalars: |, |-, |+, >, >-, >+
		if val == "|" || val == "|-" || val == "|+" || val == ">" || val == ">-" || val == ">+" {
			folded := strings.HasPrefix(val, ">")
			var block []string
			baseIndent := -1
			for j := i + 1; j < len(fmLines); j++ {
				next := fmLines[j]
				if strings.TrimSpace(next) == "" {
					block = append(block, "")
					i = j
					continue
				}
				indent := len(next) - len(strings.TrimLeft(next, " "))
				if indent == 0 {
					break
				}
				if baseIndent < 0 || indent < baseIndent {
					baseIndent = indent
				}
				if len(next) >= baseIndent {
					block = append(block, next[baseIndent:])
				} else {
					block = append(block, strings.TrimLeft(next, " "))
				}
				i = j
			}
			for len(block) > 0 && block[len(block)-1] == "" {
				block = block[:len(block)-1]
			}
			sep := "\n"
			if folded {
				sep = " "
			}
			val = strings.TrimSpace(strings.Join(block, sep))
		} else {
			val = unquoteYAMLValue(val)
		}

		if key == "name" && name == "" {
			name = val
		} else if key == "description" && description == "" {
			description = val
		}
	}
	return name, description
}

func sanitizeSkillName(name string) (string, bool) {
	cleanedPath := strings.ReplaceAll(name, `\`, "/")
	parts := strings.Split(strings.TrimRight(cleanedPath, "/"), "/")
	last := parts[len(parts)-1]
	if last == "" || last == "." || last == ".." {
		return "", false
	}
	var b strings.Builder
	for _, r := range last {
		if r < 32 || r == 127 || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	trimmed := strings.TrimRight(strings.TrimSpace(b.String()), ".")
	if trimmed == "" {
		return "", false
	}
	base := strings.ToUpper(strings.SplitN(trimmed, ".", 2)[0])
	if windowsReservedNames[base] {
		return "_" + trimmed, true
	}
	return trimmed, true
}

func inferSkillName(dir string) string {
	name, _ := parseSkillMD(dir)
	if name != "" {
		if sanitized, ok := sanitizeSkillName(name); ok {
			return sanitized
		}
	}
	base := filepath.Base(dir)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "unknown-skill"
	}
	return base
}

func listContentFiles(dir string) []contentEntry {
	walkRoot := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		walkRoot = resolved
	}
	if !isDirFollow(walkRoot) {
		return nil
	}
	var entries []contentEntry
	_ = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := d.Name()
		if path != walkRoot && isIgnoredContentName(name) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(walkRoot, path)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entries = append(entries, contentEntry{
			relPath:    filepath.ToSlash(rel),
			absPath:    path,
			execBits:   uint32(info.Mode().Perm() & 0o111),
			modifiedMs: info.ModTime().UnixMilli(),
		})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].relPath < entries[j].relPath
	})
	return entries
}

func hashEntries(entries []contentEntry) string {
	h := sha256.New()
	var bitBuf [4]byte
	for _, e := range entries {
		h.Write([]byte(e.relPath))
		if data, err := os.ReadFile(e.absPath); err == nil {
			h.Write(data)
		}
		if runtime.GOOS != "windows" {
			binary.LittleEndian.PutUint32(bitBuf[:], e.execBits)
			h.Write(bitBuf[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func latestModifiedMs(entries []contentEntry) int64 {
	var latest int64
	for _, e := range entries {
		if e.modifiedMs > latest {
			latest = e.modifiedMs
		}
	}
	return latest
}

func hashDirectory(dir string) string {
	return hashEntries(listContentFiles(dir))
}

func listSkillTopFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	var files []string
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	if files == nil {
		return []string{}
	}
	return files
}

func shouldSkipWorkspaceDir(root, dir string) bool {
	name := filepath.Base(dir)
	if strings.HasPrefix(name, ".") {
		return true
	}
	return dir != root && isDirFollow(filepath.Join(dir, "skills"))
}

// readAgentLocalSkills scans an agent's global skills directory without touching non-skill directories.
func readAgentLocalSkills(skillsRoot, agentKey, displayName string, recursive bool) []AgentSkill {
	return readAgentLocalSkillsWithEnabled(skillsRoot, true, agentKey, displayName, recursive)
}

func readAgentLocalSkillsWithEnabled(skillsRoot string, enabled bool, agentKey, displayName string, recursive bool) []AgentSkill {
	if !isDirFollow(skillsRoot) {
		return []AgentSkill{}
	}
	visited := map[string]bool{}
	if canon, err := filepath.EvalSymlinks(skillsRoot); err == nil {
		visited[canon] = true
	}

	var out []AgentSkill
	var walk func(current string)
	walk = func(current string) {
		dirEntries, err := os.ReadDir(current)
		if err != nil {
			return
		}
		for _, entry := range dirEntries {
			child := filepath.Join(current, entry.Name())
			if !isDirFollow(child) {
				continue
			}

			lstat, lerr := os.Lstat(child)
			isSymlink := lerr == nil && (lstat.Mode()&os.ModeSymlink != 0)
			linkTarget := ""
			if isSymlink {
				if lt, err := os.Readlink(child); err == nil {
					linkTarget = lt
				}
			}

			if isValidSkillDir(child) {
				dirName := entry.Name()
				rel, err := filepath.Rel(skillsRoot, child)
				if err != nil {
					rel = dirName
				}
				relSlash := filepath.ToSlash(rel)
				name, desc := parseSkillMD(child)
				if name == "" {
					name = dirName
				}
				entries := listContentFiles(child)
				out = append(out, AgentSkill{
					Name:             name,
					DirName:          dirName,
					RelativePath:     relSlash,
					Description:      desc,
					Path:             child,
					Files:            listSkillTopFiles(child),
					Enabled:          enabled,
					Agent:            agentKey,
					AgentDisplayName: displayName,
					Tags:             []string{},
					InCenter:         false,
					SyncStatus:       "project_only",
					IsSymlink:        isSymlink,
					LinkTarget:       linkTarget,
					ContentHash:      hashEntries(entries),
					LastModifiedMs:   latestModifiedMs(entries),
				})
				continue
			}

			if !recursive || shouldSkipWorkspaceDir(skillsRoot, child) {
				continue
			}
			canon, err := filepath.EvalSymlinks(child)
			if err != nil || visited[canon] {
				continue
			}
			visited[canon] = true
			walk(child)
		}
	}

	walk(skillsRoot)
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].RelativePath < out[j].RelativePath
	})
	return out
}

func ensureSafeSkillRelPath(rel string) error {
	if strings.TrimSpace(rel) == "" {
		return errors.New("invalid skill directory path")
	}
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) || filepath.IsAbs(rel) {
		return errors.New("invalid skill directory path")
	}
	normalized := strings.ReplaceAll(rel, `\`, "/")
	parts := strings.Split(normalized, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return errors.New("invalid skill directory path")
		}
		if len(p) >= 2 && p[1] == ':' {
			return errors.New("invalid skill directory path")
		}
	}
	return nil
}

func ensureDirWithinRoot(path, root string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("skill directory escapes allowed root")
	}
	return nil
}

func readSkillDocument(skillDir string, allowedRoots []string) (filename, content string, err error) {
	if !isDirFollow(skillDir) {
		return "", "", os.ErrNotExist
	}
	for _, candidate := range skillDocCandidates {
		fp := filepath.Join(skillDir, candidate)
		lstat, lerr := os.Lstat(fp)
		if lerr != nil {
			continue
		}
		if lstat.Mode()&os.ModeSymlink != 0 {
			resolved, rerr := filepath.EvalSymlinks(fp)
			if rerr != nil {
				continue
			}
			inAllowed := false
			for _, root := range allowedRoots {
				rootCanon, err := filepath.EvalSymlinks(root)
				if err != nil {
					continue
				}
				rel, err := filepath.Rel(rootCanon, resolved)
				if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					inAllowed = true
					break
				}
			}
			if !inAllowed {
				continue
			}
		}
		st, serr := os.Stat(fp)
		if serr != nil || st.IsDir() {
			continue
		}
		raw, rerr := os.ReadFile(fp)
		if rerr != nil {
			return "", "", rerr
		}
		return candidate, string(raw), nil
	}
	return "", "", fmt.Errorf("no document file found in skill directory")
}

func pathMatchesSkillPath(skillPath, skillCanonical, other string) bool {
	if other == "" {
		return false
	}
	if other == skillPath {
		return true
	}
	if skillCanonical == "" {
		return false
	}
	otherCanon, err := filepath.EvalSymlinks(other)
	if err != nil {
		return false
	}
	return otherCanon == skillCanonical
}

// findVerifiedCenterMatch mirrors agent_workspace.rs::find_verified_center_match:
// strong identity signals only (source_ref or skill_targets path match score 3; content_hash score 2).
func findVerifiedCenterMatch(skill *AgentSkill, allManaged []CenterSkill, allTargets []SkillTarget) *CenterSkill {
	canonSkillPath, _ := filepath.EvalSymlinks(skill.Path)
	bestIdx := -1
	bestScore := 0

	for i := range allManaged {
		m := &allManaged[i]
		score := 0
		if pathMatchesSkillPath(skill.Path, canonSkillPath, m.SourceRef) {
			score = 3
		} else {
			for _, t := range allTargets {
				if t.SkillID == m.ID && pathMatchesSkillPath(skill.Path, canonSkillPath, t.TargetPath) {
					score = 3
					break
				}
			}
		}
		if score == 0 && skill.ContentHash != "" && m.ContentHash == skill.ContentHash {
			score = 2
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return nil
	}
	return &allManaged[bestIdx]
}

func slugifySkillDirName(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		isASCIIAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isASCIIAlnum || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	trimmed := strings.Trim(b.String(), "-_.")
	if trimmed == "" {
		return "skill"
	}
	return trimmed
}

func uniqueProjectCenterMatch(candidates []*CenterSkill, skillHash string) *CenterSkill {
	switch len(candidates) {
	case 0:
		return nil
	case 1:
		return candidates[0]
	default:
		if skillHash == "" {
			return nil
		}
		var matched *CenterSkill
		for _, c := range candidates {
			if c.ContentHash == skillHash {
				if matched != nil {
					return nil
				}
				matched = c
			}
		}
		return matched
	}
}

// findBestProjectCenterMatch mirrors projects.rs::find_best_center_match:
// 1. source_ref exact/canonical match, 2. unique ContentHash match,
// 3. central directory basename match (disambiguated by hash),
// 4. slugified central skill name match (disambiguated by hash).
func findBestProjectCenterMatch(skill *AgentSkill, allManaged []CenterSkill) *CenterSkill {
	canonSkillPath, _ := filepath.EvalSymlinks(skill.Path)
	for i := range allManaged {
		m := &allManaged[i]
		if pathMatchesSkillPath(skill.Path, canonSkillPath, m.SourceRef) {
			return m
		}
	}
	if skill.ContentHash != "" {
		var byHash []*CenterSkill
		for i := range allManaged {
			if allManaged[i].ContentHash == skill.ContentHash {
				byHash = append(byHash, &allManaged[i])
			}
		}
		if len(byHash) == 1 {
			return byHash[0]
		}
	}
	var byCentralDir []*CenterSkill
	for i := range allManaged {
		m := &allManaged[i]
		if m.CentralPath != "" && strings.EqualFold(filepath.Base(m.CentralPath), skill.DirName) {
			byCentralDir = append(byCentralDir, m)
		}
	}
	if m := uniqueProjectCenterMatch(byCentralDir, skill.ContentHash); m != nil {
		return m
	}
	var byName []*CenterSkill
	for i := range allManaged {
		m := &allManaged[i]
		if strings.EqualFold(slugifySkillDirName(m.Name), skill.DirName) {
			byName = append(byName, m)
		}
	}
	return uniqueProjectCenterMatch(byName, skill.ContentHash)
}

// classifySyncStatus compares local skill content hash and mtime against the matched central skill.
func classifySyncStatus(skill *AgentSkill, matched *CenterSkill) string {
	if matched == nil {
		return "project_only"
	}
	if skill.ContentHash != "" && matched.ContentHash == skill.ContentHash {
		return "in_sync"
	}
	centerEntries := listContentFiles(matched.CentralPath)
	if skill.ContentHash != "" && skill.ContentHash == hashEntries(centerEntries) {
		return "in_sync"
	}
	if skill.LastModifiedMs == 0 {
		return "diverged"
	}
	centerModMs := latestModifiedMs(centerEntries)
	if centerModMs == 0 {
		return "diverged"
	}
	const thresholdMs int64 = 1000
	if skill.LastModifiedMs > centerModMs+thresholdMs {
		return "project_newer"
	}
	if centerModMs > skill.LastModifiedMs+thresholdMs {
		return "center_newer"
	}
	return "diverged"
}

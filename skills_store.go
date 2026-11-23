package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// CenterSkill represents a skill stored in ctxview's central skill repository.
type CenterSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	SourceType  string   `json:"source_type"`
	SourceRef   string   `json:"source_ref,omitempty"`
	CentralPath string   `json:"central_path"`
	ContentHash string   `json:"content_hash,omitempty"`
	Tags        []string `json:"tags"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// SkillTarget records a managed deployment from a central skill to an agent directory.
type SkillTarget struct {
	ID         string `json:"id"`
	SkillID    string `json:"skill_id"`
	Tool       string `json:"tool"`
	TargetPath string `json:"target_path"`
	Mode       string `json:"mode"`
	SyncedAt   int64  `json:"synced_at"`
	SourceHash string `json:"source_hash,omitempty"`
}

// SkillPreset groups central skills for one-click apply/remove in workspaces.
type SkillPreset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	SkillIDs    []string `json:"skill_ids"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// CenterSkillDTO is returned to the UI with attached deployment targets and preset IDs.
type CenterSkillDTO struct {
	CenterSkill
	Targets   []SkillTarget `json:"targets"`
	PresetIDs []string      `json:"preset_ids"`
}

// AgentOverviewItem summarizes an agent's global workspace status.
type AgentOverviewItem struct {
	Key               string `json:"key"`
	DisplayName       string `json:"display_name"`
	Installed         bool   `json:"installed"`
	Enabled           bool   `json:"enabled"`
	SkillsDir         string `json:"skills_dir"`
	DisplaySkillsDir  string `json:"display_skills_dir"`
	HasPathOverride   bool   `json:"has_path_override"`
	IsCustom          bool   `json:"is_custom"`
	RecursiveScan     bool   `json:"recursive_scan"`
	Category          string `json:"category"`
	Icon              string `json:"icon,omitempty"`
	ManagedCount      int    `json:"managed_count"`
	LocalCount        int    `json:"local_count"`
	LocalCountScanned bool   `json:"local_count_scanned"`
}

type AgentsOverviewResp struct {
	Agents     []AgentOverviewItem `json:"agents"`
	SyncMode   string              `json:"sync_mode"`
	CentralDir string              `json:"central_dir"`
	ToolOrder  []string            `json:"tool_order"`
}

type SkillDocumentResp struct {
	SkillName string `json:"skill_name"`
	Filename  string `json:"filename"`
	Content   string `json:"content"`
}

type TargetConflictDTO struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type SyncPairFailure struct {
	SkillID string `json:"skill_id"`
	Agent   string `json:"agent"`
	Error   string `json:"error"`
}

type SyncBatchResult struct {
	OK        bool                `json:"ok"`
	Applied   int                 `json:"applied"`
	Skipped   int                 `json:"skipped"`
	Failed    int                 `json:"failed"`
	Conflicts []TargetConflictDTO `json:"conflicts,omitempty"`
	Failures  []SyncPairFailure   `json:"failures,omitempty"`
}

// SkillsAPIError carries an HTTP status code and optional structured conflicts.
type SkillsAPIError struct {
	Status    int
	Code      string
	Message   string
	Conflicts []TargetConflictDTO
}

func (e *SkillsAPIError) Error() string { return e.Message }

func newSkillsErr(status int, code, msg string) *SkillsAPIError {
	return &SkillsAPIError{Status: status, Code: code, Message: msg}
}

type skillsStateDisk struct {
	Skills          []CenterSkill     `json:"skills"`
	Targets         []SkillTarget     `json:"targets"`
	Presets         []SkillPreset     `json:"presets"`
	Projects        []ProjectRecord   `json:"projects,omitempty"`
	SyncMode        string            `json:"sync_mode"`
	DisabledTools   []string          `json:"disabled_tools"`
	CustomToolPaths map[string]string `json:"custom_tool_paths"`
	CustomTools     []CustomToolDef   `json:"custom_tools"`
	ToolOrder       []string          `json:"tool_order"`
}

// SkillsManager owns the central skills library, target tracking, presets, and global agent workspaces.
type SkillsManager struct {
	mu          sync.Mutex
	dataDir     string
	centralDir  string
	statePath   string
	homeDirFn   func() (string, error)
	configDirFn func() (string, error)

	state skillsStateDisk
}

func NewSkillsManager(dataDir, skillsDirOverride string) *SkillsManager {
	m := &SkillsManager{
		dataDir:     dataDir,
		homeDirFn:   os.UserHomeDir,
		configDirFn: os.UserConfigDir,
		state: skillsStateDisk{
			SyncMode:        SyncModeSymlink,
			CustomToolPaths: map[string]string{},
		},
	}
	if strings.TrimSpace(skillsDirOverride) != "" {
		m.centralDir = expandPath(skillsDirOverride)
	} else if dataDir != "" {
		m.centralDir = filepath.Join(dataDir, "skills")
	}
	if dataDir != "" {
		m.statePath = filepath.Join(dataDir, "skills_state.json")
	}
	m.load()
	return m
}

func (m *SkillsManager) dirs() (homeDir, configDir string) {
	if m.homeDirFn != nil {
		homeDir, _ = m.homeDirFn()
	}
	if m.configDirFn != nil {
		configDir, _ = m.configDirFn()
	}
	return filepath.Clean(homeDir), configDir
}

func (m *SkillsManager) redactWithHome(p string, redactPaths bool) string {
	if !redactPaths || p == "" {
		return p
	}
	homeDir, _ := m.dirs()
	if homeDir != "" && homeDir != "." {
		cleanP := filepath.Clean(p)
		candidates := []string{homeDir}
		if canonHome, err := filepath.EvalSymlinks(homeDir); err == nil && canonHome != "" && canonHome != homeDir {
			candidates = append(candidates, filepath.Clean(canonHome))
		}
		sep := string(os.PathSeparator)
		for _, base := range candidates {
			if cleanP == base {
				return "~"
			}
			if strings.HasPrefix(cleanP, base+sep) {
				return "~/" + filepath.ToSlash(cleanP[len(base)+1:])
			}
		}
	}
	return replaceHomePaths(p)
}

func (m *SkillsManager) load() {
	if m.statePath == "" {
		return
	}
	raw, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}
	var disk skillsStateDisk
	if json.Unmarshal(raw, &disk) != nil {
		return
	}
	if disk.CustomToolPaths == nil {
		disk.CustomToolPaths = map[string]string{}
	}
	disk.SyncMode = normalizeSyncMode(disk.SyncMode)
	m.state = disk
}

func (m *SkillsManager) saveLocked() error {
	if m.statePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o755); err != nil {
		return err
	}
	if m.state.CustomToolPaths == nil {
		m.state.CustomToolPaths = map[string]string{}
	}
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath)
}

func newSkillUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		b[6] = (b[6] & 0x0f) | 0x40
		b[8] = (b[8] & 0x3f) | 0x80
		hx := hex.EncodeToString(b[:])
		return hx[0:8] + "-" + hx[8:12] + "-" + hx[12:16] + "-" + hx[16:20] + "-" + hx[20:32]
	}
	return fmt.Sprintf("id-%d", time.Now().UnixNano())
}

func (m *SkillsManager) allAdaptersLocked() []ToolAdapter {
	defaults := DefaultToolAdapters()
	out := make([]ToolAdapter, 0, len(defaults)+len(m.state.CustomTools))
	seen := map[string]bool{}
	for _, a := range defaults {
		if override, ok := m.state.CustomToolPaths[a.Key]; ok && override != "" {
			a.OverrideSkillsDir = override
		}
		seen[a.Key] = true
		out = append(out, a)
	}
	for _, ct := range m.state.CustomTools {
		if seen[ct.Key] {
			continue
		}
		cat := ct.Category
		if cat != CategoryLobster {
			cat = CategoryCoding
		}
		seen[ct.Key] = true
		out = append(out, ToolAdapter{
			Key:                      ct.Key,
			DisplayName:              ct.DisplayName,
			OverrideSkillsDir:        ct.SkillsDir,
			ProjectRelativeSkillsDir: ct.ProjectRelativeSkillsDir,
			IsCustom:                 true,
			RecursiveScan:            ct.RecursiveScan,
			Category:                 cat,
		})
	}
	if len(m.state.ToolOrder) > 0 {
		rank := make(map[string]int, len(m.state.ToolOrder))
		for i, k := range m.state.ToolOrder {
			rank[k] = i
		}
		sort.SliceStable(out, func(i, j int) bool {
			ri, oki := rank[out[i].Key]
			rj, okj := rank[out[j].Key]
			if oki && okj {
				return ri < rj
			}
			if oki != okj {
				return oki
			}
			return false
		})
	}
	return out
}

func (m *SkillsManager) findAdapterLocked(agentKey string) (ToolAdapter, error) {
	for _, a := range m.allAdaptersLocked() {
		if a.Key == agentKey {
			return a, nil
		}
	}
	return ToolAdapter{}, newSkillsErr(http.StatusNotFound, "UNKNOWN_AGENT", "Unknown agent: "+agentKey)
}

func (m *SkillsManager) allocateCentralDirLocked(preferredName string) (string, error) {
	if m.centralDir == "" {
		return "", newSkillsErr(http.StatusInternalServerError, "NO_CENTRAL_DIR", "central skills directory is not configured")
	}
	if err := os.MkdirAll(m.centralDir, 0o755); err != nil {
		return "", err
	}
	baseName, ok := sanitizeSkillName(preferredName)
	if !ok {
		baseName = "skill"
	}
	candidate := filepath.Join(m.centralDir, baseName)
	if _, err := os.Lstat(candidate); os.IsNotExist(err) {
		return candidate, nil
	}
	for n := 2; n < 10000; n++ {
		next := filepath.Join(m.centralDir, fmt.Sprintf("%s-%d", baseName, n))
		if _, err := os.Lstat(next); os.IsNotExist(err) {
			return next, nil
		}
	}
	return filepath.Join(m.centralDir, newSkillUUID()), nil
}

func (m *SkillsManager) upsertTargetLocked(t SkillTarget) {
	for i := range m.state.Targets {
		if m.state.Targets[i].SkillID == t.SkillID && m.state.Targets[i].Tool == t.Tool {
			m.state.Targets[i] = t
			return
		}
	}
	m.state.Targets = append(m.state.Targets, t)
}

func (m *SkillsManager) deleteTargetLocked(skillID, tool string) {
	next := m.state.Targets[:0]
	for _, t := range m.state.Targets {
		if t.SkillID == skillID && t.Tool == tool {
			continue
		}
		next = append(next, t)
	}
	m.state.Targets = next
}

func (m *SkillsManager) targetPathReferencedElsewhereLocked(skillID, tool, targetPath string, memo map[string]string) bool {
	key := ownershipKey(targetPath, memo)
	for _, other := range m.state.Targets {
		if other.SkillID == skillID && other.Tool == tool {
			continue
		}
		if ownershipKey(other.TargetPath, memo) == key {
			return true
		}
	}
	return false
}

func (m *SkillsManager) detachSourceRefsLocked(targetPath string) {
	lstat, lerr := os.Lstat(targetPath)
	isLink := lerr == nil && lstat.Mode()&os.ModeSymlink != 0
	var targetCanon string
	if !isLink {
		targetCanon, _ = filepath.EvalSymlinks(targetPath)
	}
	for i := range m.state.Skills {
		sk := &m.state.Skills[i]
		if sk.SourceRef == "" || sk.SourceRef == sk.CentralPath {
			continue
		}
		match := sk.SourceRef == targetPath
		if !match && targetCanon != "" {
			if sc, err := filepath.EvalSymlinks(sk.SourceRef); err == nil && sc == targetCanon {
				match = true
			}
		}
		if match {
			sk.SourceRef = sk.CentralPath
		}
	}
}

func targetDirName(centralPath, skillName string) string {
	base := filepath.Base(centralPath)
	if base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	return skillName
}

func syncStatusPriority(status string) int {
	switch status {
	case "project_only":
		return 0
	case "project_newer":
		return 1
	case "diverged":
		return 2
	case "center_newer":
		return 3
	case "in_sync":
		return 4
	default:
		return 5
	}
}

// ListAgents returns all built-in and custom agents with installed status and live on-disk counts.
func (m *SkillsManager) ListAgents(redactPaths bool) AgentsOverviewResp {
	m.mu.Lock()
	adapters := m.allAdaptersLocked()
	disabled := slices.Clone(m.state.DisabledTools)
	targets := slices.Clone(m.state.Targets)
	syncMode := normalizeSyncMode(m.state.SyncMode)
	toolOrder := slices.Clone(m.state.ToolOrder)
	homeDir, configDir := m.dirs()
	centralDir := m.centralDir
	m.mu.Unlock()

	managedByTool := map[string]int{}
	for _, t := range targets {
		managedByTool[t.Tool]++
	}

	items := make([]AgentOverviewItem, 0, len(adapters))
	for _, a := range adapters {
		installed := a.IsInstalled(homeDir, configDir)
		enabled := !slices.Contains(disabled, a.Key)
		skillsDir := a.SkillsDir(homeDir, configDir)
		localCount := 0
		scanned := false
		if installed && enabled && skillsDir != "" {
			localCount = len(readAgentLocalSkills(skillsDir, a.Key, a.DisplayName, a.RecursiveScan))
			scanned = true
		}
		displayDir := skillsDir
		if redactPaths {
			displayDir = m.redactWithHome(skillsDir, true)
		}
		items = append(items, AgentOverviewItem{
			Key:               a.Key,
			DisplayName:       a.DisplayName,
			Installed:         installed,
			Enabled:           enabled,
			SkillsDir:         m.redactWithHome(skillsDir, redactPaths),
			DisplaySkillsDir:  displayDir,
			HasPathOverride:   a.OverrideSkillsDir != "" && !a.IsCustom,
			IsCustom:          a.IsCustom,
			RecursiveScan:     a.RecursiveScan,
			Category:          a.Category,
			Icon:              a.Icon,
			ManagedCount:      managedByTool[a.Key],
			LocalCount:        localCount,
			LocalCountScanned: scanned,
		})
	}

	if toolOrder == nil {
		toolOrder = []string{}
	}
	return AgentsOverviewResp{
		Agents:     items,
		SyncMode:   syncMode,
		CentralDir: m.redactWithHome(centralDir, redactPaths),
		ToolOrder:  toolOrder,
	}
}

// GetAgentSkills scans a single agent's global skills directory and classifies against the central library.
func (m *SkillsManager) GetAgentSkills(agentKey string, redactPaths bool) ([]AgentSkill, error) {
	m.mu.Lock()
	adapter, err := m.findAdapterLocked(agentKey)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	homeDir, configDir := m.dirs()
	skillsDir := adapter.SkillsDir(homeDir, configDir)
	managed := slices.Clone(m.state.Skills)
	targets := slices.Clone(m.state.Targets)
	m.mu.Unlock()

	skills := readAgentLocalSkills(skillsDir, adapter.Key, adapter.DisplayName, adapter.RecursiveScan)
	managedSet := map[string]bool{}
	for _, t := range targets {
		if t.Tool == agentKey {
			managedSet[t.SkillID] = true
		}
	}

	for i := range skills {
		sk := &skills[i]
		matched := findVerifiedCenterMatch(sk, managed, targets)
		if matched != nil {
			sk.InCenter = true
			sk.CenterSkillID = matched.ID
			sk.Managed = managedSet[matched.ID]
			if len(matched.Tags) > 0 {
				sk.Tags = slices.Clone(matched.Tags)
			}
		}
		sk.SyncStatus = classifySyncStatus(sk, matched)
		if redactPaths {
			sk.Path = m.redactWithHome(sk.Path, true)
			sk.LinkTarget = m.redactWithHome(sk.LinkTarget, true)
		}
	}

	sort.Slice(skills, func(i, j int) bool {
		pi := syncStatusPriority(skills[i].SyncStatus)
		pj := syncStatusPriority(skills[j].SyncStatus)
		if pi != pj {
			return pi < pj
		}
		return strings.ToLower(skills[i].Name) < strings.ToLower(skills[j].Name)
	})
	return skills, nil
}

// GetAgentSkillDocument returns the markdown documentation for one skill in an agent's directory.
func (m *SkillsManager) GetAgentSkillDocument(agentKey, relPath string, redactContent bool) (SkillDocumentResp, error) {
	if err := ensureSafeSkillRelPath(relPath); err != nil {
		return SkillDocumentResp{}, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}
	m.mu.Lock()
	adapter, err := m.findAdapterLocked(agentKey)
	if err != nil {
		m.mu.Unlock()
		return SkillDocumentResp{}, err
	}
	homeDir, configDir := m.dirs()
	skillsRoot := adapter.SkillsDir(homeDir, configDir)
	centralDir := m.centralDir
	m.mu.Unlock()

	skillDir := filepath.Join(skillsRoot, filepath.FromSlash(relPath))
	if err := ensureDirWithinRoot(skillDir, skillsRoot); err != nil {
		return SkillDocumentResp{}, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}
	allowed := []string{skillsRoot}
	if centralDir != "" {
		allowed = append(allowed, centralDir)
	}
	// If the skill dir itself is a symlink to central (or elsewhere inside skillsRoot),
	// allow reading regular doc files inside that resolved skill directory.
	if resolvedDir, err := filepath.EvalSymlinks(skillDir); err == nil {
		allowed = append(allowed, resolvedDir)
	}
	filename, content, err := readSkillDocument(skillDir, allowed)
	if err != nil {
		return SkillDocumentResp{}, newSkillsErr(http.StatusNotFound, "DOC_NOT_FOUND", err.Error())
	}
	if redactContent {
		content = redactText(content)
	}
	return SkillDocumentResp{
		SkillName: relPath,
		Filename:  filename,
		Content:   content,
	}, nil
}

// GetCenterSkillDocument reads the markdown documentation for a central skill.
func (m *SkillsManager) GetCenterSkillDocument(skillID string, redactContent bool) (SkillDocumentResp, error) {
	m.mu.Lock()
	var found *CenterSkill
	for i := range m.state.Skills {
		if m.state.Skills[i].ID == skillID {
			cp := m.state.Skills[i]
			found = &cp
			break
		}
	}
	m.mu.Unlock()
	if found == nil {
		return SkillDocumentResp{}, newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found")
	}
	filename, content, err := readSkillDocument(found.CentralPath, []string{found.CentralPath})
	if err != nil {
		return SkillDocumentResp{}, newSkillsErr(http.StatusNotFound, "DOC_NOT_FOUND", err.Error())
	}
	if redactContent {
		content = redactText(content)
	}
	return SkillDocumentResp{
		SkillName: found.Name,
		Filename:  filename,
		Content:   content,
	}, nil
}

// ImportAgentLocalSkillToCenter imports or updates a skill from an agent's global directory into the central library,
// then adopts the agent directory under managed deployment tracking.
func (m *SkillsManager) ImportAgentLocalSkillToCenter(agentKey, relPath string) (*CenterSkill, error) {
	if err := ensureSafeSkillRelPath(relPath); err != nil {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	adapter, err := m.findAdapterLocked(agentKey)
	if err != nil {
		return nil, err
	}
	homeDir, configDir := m.dirs()
	skillsRoot := adapter.SkillsDir(homeDir, configDir)
	localList := readAgentLocalSkills(skillsRoot, adapter.Key, adapter.DisplayName, adapter.RecursiveScan)
	var found *AgentSkill
	for i := range localList {
		if localList[i].RelativePath == relPath {
			found = &localList[i]
			break
		}
	}
	if found == nil {
		return nil, newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found in agent local directory")
	}
	sourcePath := found.Path
	if err := ensureDirWithinRoot(sourcePath, skillsRoot); err != nil {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}

	now := time.Now().UnixMilli()
	syncMode := normalizeSyncMode(m.state.SyncMode)
	existing := findVerifiedCenterMatch(found, m.state.Skills, m.state.Targets)

	if existing != nil {
		var targetSkill *CenterSkill
		for i := range m.state.Skills {
			if m.state.Skills[i].ID == existing.ID {
				targetSkill = &m.state.Skills[i]
				break
			}
		}
		if targetSkill != nil {
			// Copy source to a staging location or directly replace central_path when source != central_path.
			if canonSrc, err1 := filepath.EvalSymlinks(sourcePath); err1 != nil || canonSrc != targetSkill.CentralPath {
				stageDir := targetSkill.CentralPath + ".importing-" + newSkillUUID()
				if err := copyDirRecursive(sourcePath, stageDir); err != nil {
					_ = os.RemoveAll(stageDir)
					return nil, err
				}
				_ = os.RemoveAll(targetSkill.CentralPath)
				if err := os.Rename(stageDir, targetSkill.CentralPath); err != nil {
					_ = os.RemoveAll(stageDir)
					return nil, err
				}
			}
			_, desc := parseSkillMD(targetSkill.CentralPath)
			hash := hashDirectory(targetSkill.CentralPath)
			targetSkill.Description = desc
			targetSkill.ContentHash = hash
			targetSkill.UpdatedAt = now
			if targetSkill.SourceType == "local" && pathMatchesSkillPath(found.Path, "", targetSkill.SourceRef) {
				targetSkill.SourceRef = found.Path
			}

			m.detachSourceRefsLocked(sourcePath)
			actualMode, err := syncSkill(targetSkill.CentralPath, sourcePath, syncMode, ReplacePolicy{Kind: PolicyUserConfirmed})
			if err != nil {
				return nil, err
			}
			m.upsertTargetLocked(SkillTarget{
				ID:         newSkillUUID(),
				SkillID:    targetSkill.ID,
				Tool:       agentKey,
				TargetPath: sourcePath,
				Mode:       actualMode,
				SyncedAt:   now,
				SourceHash: hash,
			})
			if err := m.saveLocked(); err != nil {
				return nil, err
			}
			cp := *targetSkill
			return &cp, nil
		}
	}

	name := inferSkillName(sourcePath)
	_, desc := parseSkillMD(sourcePath)
	destDir, err := m.allocateCentralDirLocked(name)
	if err != nil {
		return nil, err
	}
	if err := copyDirRecursive(sourcePath, destDir); err != nil {
		_ = os.RemoveAll(destDir)
		return nil, err
	}
	hash := hashDirectory(destDir)
	rec := CenterSkill{
		ID:          newSkillUUID(),
		Name:        name,
		Description: desc,
		SourceType:  "local",
		SourceRef:   sourcePath,
		CentralPath: destDir,
		ContentHash: hash,
		Tags:        []string{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	m.state.Skills = append(m.state.Skills, rec)
	m.detachSourceRefsLocked(sourcePath)

	actualMode, err := syncSkill(destDir, sourcePath, syncMode, ReplacePolicy{Kind: PolicyUserConfirmed})
	if err != nil {
		// Roll back skill row on adopt failure so we don't leave an orphaned button-less skill.
		m.state.Skills = m.state.Skills[:len(m.state.Skills)-1]
		return nil, err
	}
	m.upsertTargetLocked(SkillTarget{
		ID:         newSkillUUID(),
		SkillID:    rec.ID,
		Tool:       agentKey,
		TargetPath: sourcePath,
		Mode:       actualMode,
		SyncedAt:   now,
		SourceHash: hash,
	})
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	cp := rec
	return &cp, nil
}

// PullAgentLocalSkillFromCenter overwrites an agent's local skill copy from its matched central skill.
func (m *SkillsManager) PullAgentLocalSkillFromCenter(agentKey, relPath string) error {
	if err := ensureSafeSkillRelPath(relPath); err != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	adapter, err := m.findAdapterLocked(agentKey)
	if err != nil {
		return err
	}
	homeDir, configDir := m.dirs()
	skillsRoot := adapter.SkillsDir(homeDir, configDir)
	localList := readAgentLocalSkills(skillsRoot, adapter.Key, adapter.DisplayName, adapter.RecursiveScan)
	var found *AgentSkill
	for i := range localList {
		if localList[i].RelativePath == relPath {
			found = &localList[i]
			break
		}
	}
	if found == nil {
		return newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found in agent local directory")
	}
	matched := findVerifiedCenterMatch(found, m.state.Skills, m.state.Targets)
	if matched == nil {
		return newSkillsErr(http.StatusNotFound, "CENTER_NOT_FOUND", "No matching managed skill in center")
	}
	if classifySyncStatus(found, matched) == "project_newer" {
		return newSkillsErr(http.StatusBadRequest, "LOCAL_NEWER", "Local skill is newer than the Skills Center version")
	}
	targetPath := found.Path
	if err := ensureDirWithinRoot(targetPath, skillsRoot); err != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}
	m.detachSourceRefsLocked(targetPath)
	syncMode := normalizeSyncMode(m.state.SyncMode)
	actualMode, err := syncSkill(matched.CentralPath, targetPath, syncMode, ReplacePolicy{Kind: PolicyUserConfirmed})
	if err != nil {
		return err
	}
	for i := range m.state.Targets {
		if m.state.Targets[i].SkillID == matched.ID && m.state.Targets[i].Tool == agentKey {
			m.state.Targets[i].Mode = actualMode
			m.state.Targets[i].SyncedAt = time.Now().UnixMilli()
			m.state.Targets[i].SourceHash = matched.ContentHash
			m.state.Targets[i].TargetPath = targetPath
		}
	}
	return m.saveLocked()
}

// DeleteAgentLocalSkill permanently deletes an unmanaged local skill directory from an agent's directory.
func (m *SkillsManager) DeleteAgentLocalSkill(agentKey, relPath string) error {
	if err := ensureSafeSkillRelPath(relPath); err != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	adapter, err := m.findAdapterLocked(agentKey)
	if err != nil {
		return err
	}
	homeDir, configDir := m.dirs()
	skillsRoot := adapter.SkillsDir(homeDir, configDir)
	localList := readAgentLocalSkills(skillsRoot, adapter.Key, adapter.DisplayName, adapter.RecursiveScan)
	var found *AgentSkill
	for i := range localList {
		if localList[i].RelativePath == relPath {
			found = &localList[i]
			break
		}
	}
	if found == nil {
		return newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found in agent local directory")
	}
	if matched := findVerifiedCenterMatch(found, m.state.Skills, m.state.Targets); matched != nil {
		canonSkill, _ := filepath.EvalSymlinks(found.Path)
		for _, t := range m.state.Targets {
			if t.SkillID == matched.ID && pathMatchesSkillPath(found.Path, canonSkill, t.TargetPath) {
				return newSkillsErr(http.StatusBadRequest, "SKILL_IS_MANAGED", "Skill is managed by Skills Center — remove from the agent first.")
			}
		}
	}
	if err := ensureDirWithinRoot(found.Path, skillsRoot); err != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}
	return removeTarget(found.Path)
}

func (m *SkillsManager) syncSingleLocked(skillID, agentKey string, evidence *pathEvidence) (bool, error) {
	adapter, err := m.findAdapterLocked(agentKey)
	if err != nil {
		return false, err
	}
	homeDir, configDir := m.dirs()
	if !adapter.IsInstalled(homeDir, configDir) {
		return false, newSkillsErr(http.StatusNotFound, "AGENT_NOT_INSTALLED", adapter.DisplayName+" is not installed")
	}
	if slices.Contains(m.state.DisabledTools, agentKey) {
		return false, newSkillsErr(http.StatusBadRequest, "AGENT_DISABLED", adapter.DisplayName+" is disabled")
	}
	var skill *CenterSkill
	for i := range m.state.Skills {
		if m.state.Skills[i].ID == skillID {
			skill = &m.state.Skills[i]
			break
		}
	}
	if skill == nil {
		return false, newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found")
	}
	source := skill.CentralPath
	if !isDirFollow(source) {
		return false, newSkillsErr(http.StatusNotFound, "CENTRAL_MISSING", "Central skill directory is missing")
	}

	root := adapter.SkillsDir(homeDir, configDir)
	var existingTarget *SkillTarget
	for i := range m.state.Targets {
		if m.state.Targets[i].SkillID == skillID && m.state.Targets[i].Tool == agentKey {
			existingTarget = &m.state.Targets[i]
			break
		}
	}

	target := filepath.Join(root, targetDirName(source, skill.Name))
	if existingTarget != nil {
		if err := ensureDirWithinRoot(existingTarget.TargetPath, root); err == nil {
			target = existingTarget.TargetPath
		}
	}

	desiredMode := normalizeSyncMode(m.state.SyncMode)
	if existingTarget != nil && existingTarget.TargetPath == target {
		checkMode := ""
		switch {
		case existingTarget.Mode == SyncModeSymlink && desiredMode == SyncModeSymlink:
			checkMode = SyncModeSymlink
		case existingTarget.Mode == SyncModeCopy && (desiredMode == SyncModeCopy || desiredMode == SyncModeSymlink):
			checkMode = SyncModeCopy
		}
		if checkMode != "" {
			if checkMode == SyncModeCopy {
				if st, _ := classifyTarget(target, source); st == TargetRealDir || st == TargetLinkToSource {
					if isTargetCurrent(source, target, checkMode, existingTarget.SourceHash, skill.ContentHash) {
						return false, nil
					}
				}
			} else if isTargetCurrent(source, target, checkMode, existingTarget.SourceHash, skill.ContentHash) {
				return false, nil
			}
		}
	}

	evKind, evMode := evidence.judge(skillID, agentKey, target)
	if evKind == evidenceBorrowed && copyMatchesLibrary(evMode, source, target) {
		m.upsertTargetLocked(SkillTarget{
			ID:         newSkillUUID(),
			SkillID:    skillID,
			Tool:       agentKey,
			TargetPath: target,
			Mode:       evMode,
			SyncedAt:   time.Now().UnixMilli(),
			SourceHash: skill.ContentHash,
		})
		return true, nil
	}

	policy := ReplacePolicy{Kind: PolicyNoClobber}
	if evKind == evidenceOwn {
		policy = ReplacePolicy{Kind: PolicyRecorded, Mode: evMode}
	}
	actualMode, err := syncSkill(source, target, desiredMode, policy)
	if err != nil {
		return false, err
	}
	evidence.recordWrite(skillID, target, actualMode)
	m.upsertTargetLocked(SkillTarget{
		ID:         newSkillUUID(),
		SkillID:    skillID,
		Tool:       agentKey,
		TargetPath: target,
		Mode:       actualMode,
		SyncedAt:   time.Now().UnixMilli(),
		SourceHash: skill.ContentHash,
	})
	return true, nil
}

// SyncSkills deploys one or more central skills to one or more agents with ownership verification.
func (m *SkillsManager) SyncSkills(skillIDs, agentKeys []string) (SyncBatchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	res := SyncBatchResult{OK: true}
	evidence := newPathEvidence(m.state.Targets)
	singlePair := len(skillIDs) == 1 && len(agentKeys) == 1

	for _, sid := range skillIDs {
		for _, aid := range agentKeys {
			changed, err := m.syncSingleLocked(sid, aid, evidence)
			if err != nil {
				if ref, ok := isReplaceRefused(err); ok {
					res.Conflicts = append(res.Conflicts, TargetConflictDTO{
						Path:   ref.Target,
						Reason: ref.Reason,
					})
					if singlePair {
						return res, &SkillsAPIError{
							Status:    http.StatusConflict,
							Code:      "TARGET_CONFLICT",
							Message:   ref.Error(),
							Conflicts: res.Conflicts,
						}
					}
				}
				if singlePair {
					return res, err
				}
				res.Failed++
				res.Failures = append(res.Failures, SyncPairFailure{
					SkillID: sid,
					Agent:   aid,
					Error:   err.Error(),
				})
				continue
			}
			if changed {
				res.Applied++
			} else {
				res.Skipped++
			}
		}
	}
	res.OK = res.Failed == 0 && len(res.Conflicts) == 0
	if err := m.saveLocked(); err != nil {
		return res, err
	}
	return res, nil
}

// UnsyncSkills removes managed deployments for skillIDs × agentKeys while preserving shared paths and central copies.
func (m *SkillsManager) UnsyncSkills(skillIDs, agentKeys []string) (SyncBatchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	res := SyncBatchResult{OK: true}
	memo := map[string]string{}

	for _, sid := range skillIDs {
		for _, aid := range agentKeys {
			var found *SkillTarget
			for i := range m.state.Targets {
				if m.state.Targets[i].SkillID == sid && m.state.Targets[i].Tool == aid {
					cp := m.state.Targets[i]
					found = &cp
					break
				}
			}
			if found == nil {
				res.Skipped++
				continue
			}
			if !m.targetPathReferencedElsewhereLocked(sid, aid, found.TargetPath, memo) {
				if _, err := removeRecordedTarget(found.TargetPath, found.Mode); err != nil {
					res.Failed++
					res.Failures = append(res.Failures, SyncPairFailure{
						SkillID: sid,
						Agent:   aid,
						Error:   err.Error(),
					})
					continue
				}
			}
			m.deleteTargetLocked(sid, aid)
			res.Applied++
		}
	}
	res.OK = res.Failed == 0
	if err := m.saveLocked(); err != nil {
		return res, err
	}
	return res, nil
}

// ListCenterSkills returns all central skills enriched with deployment targets, presets, and tags.
func (m *SkillsManager) ListCenterSkills(redactPaths bool) []CenterSkillDTO {
	m.mu.Lock()
	skills := slices.Clone(m.state.Skills)
	targets := slices.Clone(m.state.Targets)
	presets := slices.Clone(m.state.Presets)
	m.mu.Unlock()

	targetsBySkill := map[string][]SkillTarget{}
	for _, t := range targets {
		if redactPaths {
			t.TargetPath = m.redactWithHome(t.TargetPath, true)
		}
		targetsBySkill[t.SkillID] = append(targetsBySkill[t.SkillID], t)
	}
	presetsBySkill := map[string][]string{}
	for _, p := range presets {
		for _, sid := range p.SkillIDs {
			presetsBySkill[sid] = append(presetsBySkill[sid], p.ID)
		}
	}

	out := make([]CenterSkillDTO, 0, len(skills))
	for _, sk := range skills {
		if redactPaths {
			sk.SourceRef = m.redactWithHome(sk.SourceRef, true)
			sk.CentralPath = m.redactWithHome(sk.CentralPath, true)
		}
		if sk.Tags == nil {
			sk.Tags = []string{}
		}
		tg := targetsBySkill[sk.ID]
		if tg == nil {
			tg = []SkillTarget{}
		}
		pids := presetsBySkill[sk.ID]
		if pids == nil {
			pids = []string{}
		}
		out = append(out, CenterSkillDTO{
			CenterSkill: sk,
			Targets:     tg,
			PresetIDs:   pids,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// ImportPathToCenter installs a local skill directory into the central library without deploying it to agents.
func (m *SkillsManager) ImportPathToCenter(rawPath string) (*CenterSkill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	homeDir, _ := m.dirs()
	src := expandPathWithHome(rawPath, homeDir)
	if src == "" || !isValidSkillDir(src) {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_SKILL_DIR", "Directory must exist and contain SKILL.md or skill.md")
	}
	name := inferSkillName(src)
	_, desc := parseSkillMD(src)
	destDir, err := m.allocateCentralDirLocked(name)
	if err != nil {
		return nil, err
	}
	if err := copyDirRecursive(src, destDir); err != nil {
		_ = os.RemoveAll(destDir)
		return nil, err
	}
	now := time.Now().UnixMilli()
	rec := CenterSkill{
		ID:          newSkillUUID(),
		Name:        name,
		Description: desc,
		SourceType:  "local",
		SourceRef:   src,
		CentralPath: destDir,
		ContentHash: hashDirectory(destDir),
		Tags:        []string{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	m.state.Skills = append(m.state.Skills, rec)
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	cp := rec
	return &cp, nil
}

// DeleteCenterSkill deletes a skill from the central library and removes any managed deployments that still match.
func (m *SkillsManager) DeleteCenterSkill(skillID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	for i := range m.state.Skills {
		if m.state.Skills[i].ID == skillID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found")
	}
	centralPath := m.state.Skills[idx].CentralPath
	memo := map[string]string{}

	var keptTargets []SkillTarget
	for _, t := range m.state.Targets {
		if t.SkillID != skillID {
			keptTargets = append(keptTargets, t)
			continue
		}
		if !m.targetPathReferencedElsewhereLocked(skillID, t.Tool, t.TargetPath, memo) {
			_, _ = removeRecordedTarget(t.TargetPath, t.Mode)
		}
	}
	m.state.Targets = keptTargets
	m.state.Skills = append(m.state.Skills[:idx], m.state.Skills[idx+1:]...)

	for i := range m.state.Presets {
		p := &m.state.Presets[i]
		filtered := p.SkillIDs[:0]
		for _, sid := range p.SkillIDs {
			if sid != skillID {
				filtered = append(filtered, sid)
			}
		}
		p.SkillIDs = filtered
	}

	if centralPath != "" {
		_ = os.RemoveAll(centralPath)
	}
	return m.saveLocked()
}

// SetSkillTags replaces the tags of a central skill.
func (m *SkillsManager) SetSkillTags(skillID string, tags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.state.Skills {
		if m.state.Skills[i].ID == skillID {
			clean := make([]string, 0, len(tags))
			seen := map[string]bool{}
			for _, t := range tags {
				t = strings.TrimSpace(t)
				if t != "" && !seen[t] {
					seen[t] = true
					clean = append(clean, t)
				}
			}
			m.state.Skills[i].Tags = clean
			m.state.Skills[i].UpdatedAt = time.Now().UnixMilli()
			return m.saveLocked()
		}
	}
	return newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found")
}

// ListPresets returns all skill presets sorted by creation time / name.
func (m *SkillsManager) ListPresets() []SkillPreset {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(m.state.Presets)
	for i := range out {
		if out[i].SkillIDs == nil {
			out[i].SkillIDs = []string{}
		}
	}
	return out
}

type SavePresetReq struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SkillIDs    []string `json:"skill_ids"`
}

// SavePreset creates or updates a named skill preset.
func (m *SkillsManager) SavePreset(req SavePresetReq) (*SkillPreset, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_NAME", "Preset name is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	validSkill := map[string]bool{}
	for _, s := range m.state.Skills {
		validSkill[s.ID] = true
	}
	dedupIDs := make([]string, 0, len(req.SkillIDs))
	seen := map[string]bool{}
	for _, sid := range req.SkillIDs {
		if validSkill[sid] && !seen[sid] {
			seen[sid] = true
			dedupIDs = append(dedupIDs, sid)
		}
	}
	now := time.Now().UnixMilli()

	if req.ID != "" {
		for i := range m.state.Presets {
			if m.state.Presets[i].ID == req.ID {
				m.state.Presets[i].Name = name
				m.state.Presets[i].Description = strings.TrimSpace(req.Description)
				m.state.Presets[i].SkillIDs = dedupIDs
				m.state.Presets[i].UpdatedAt = now
				if err := m.saveLocked(); err != nil {
					return nil, err
				}
				cp := m.state.Presets[i]
				return &cp, nil
			}
		}
		return nil, newSkillsErr(http.StatusNotFound, "PRESET_NOT_FOUND", "Preset not found")
	}

	for _, existing := range m.state.Presets {
		if strings.EqualFold(existing.Name, name) {
			return nil, newSkillsErr(http.StatusConflict, "DUPLICATE_PRESET", "Preset name already exists")
		}
	}
	p := SkillPreset{
		ID:          newSkillUUID(),
		Name:        name,
		Description: strings.TrimSpace(req.Description),
		SkillIDs:    dedupIDs,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	m.state.Presets = append(m.state.Presets, p)
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePreset deletes a preset without touching deployed skills.
func (m *SkillsManager) DeletePreset(presetID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make([]SkillPreset, 0, len(m.state.Presets))
	found := false
	for _, p := range m.state.Presets {
		if p.ID == presetID {
			found = true
			continue
		}
		next = append(next, p)
	}
	if !found {
		return newSkillsErr(http.StatusNotFound, "PRESET_NOT_FOUND", "Preset not found")
	}
	m.state.Presets = next
	return m.saveLocked()
}

type SkillsSettingsReq struct {
	Action        string   `json:"action"`
	SyncMode      string   `json:"sync_mode"`
	Key           string   `json:"key"`
	DisplayName   string   `json:"display_name"`
	SkillsDir     string   `json:"skills_dir"`
	Category      string   `json:"category"`
	RecursiveScan bool     `json:"recursive_scan"`
	Enabled       *bool    `json:"enabled"`
	Order         []string `json:"order"`
}

// UpdateSettings updates global workspace configuration (sync mode, agent enablement, path overrides, custom tools, ordering).
func (m *SkillsManager) UpdateSettings(req SkillsSettingsReq) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	homeDir, _ := m.dirs()
	switch req.Action {
	case "set_sync_mode":
		m.state.SyncMode = normalizeSyncMode(req.SyncMode)
	case "set_tool_enabled":
		if _, err := m.findAdapterLocked(req.Key); err != nil {
			return err
		}
		enable := req.Enabled == nil || *req.Enabled
		if enable {
			next := m.state.DisabledTools[:0]
			for _, k := range m.state.DisabledTools {
				if k != req.Key {
					next = append(next, k)
				}
			}
			m.state.DisabledTools = next
		} else {
			if !slices.Contains(m.state.DisabledTools, req.Key) {
				m.state.DisabledTools = append(m.state.DisabledTools, req.Key)
			}
			memo := map[string]string{}
			kept := make([]SkillTarget, 0, len(m.state.Targets))
			for _, t := range m.state.Targets {
				if t.Tool != req.Key {
					kept = append(kept, t)
					continue
				}
				if !m.targetPathReferencedElsewhereLocked(t.SkillID, req.Key, t.TargetPath, memo) {
					_, _ = removeRecordedTarget(t.TargetPath, t.Mode)
				}
			}
			m.state.Targets = kept
		}
	case "set_tool_path":
		adapter, err := m.findAdapterLocked(req.Key)
		if err != nil {
			return err
		}
		p := expandPathWithHome(req.SkillsDir, homeDir)
		if p == "" {
			return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", "Skills directory path is required")
		}
		if adapter.IsCustom {
			for i := range m.state.CustomTools {
				if m.state.CustomTools[i].Key == req.Key {
					m.state.CustomTools[i].SkillsDir = p
					break
				}
			}
		} else {
			if m.state.CustomToolPaths == nil {
				m.state.CustomToolPaths = map[string]string{}
			}
			m.state.CustomToolPaths[req.Key] = p
		}
	case "reset_tool_path":
		if _, err := m.findAdapterLocked(req.Key); err != nil {
			return err
		}
		delete(m.state.CustomToolPaths, req.Key)
	case "add_custom_tool":
		key := strings.TrimSpace(req.Key)
		displayName := strings.TrimSpace(req.DisplayName)
		skillsDir := expandPathWithHome(req.SkillsDir, homeDir)
		if key == "" || displayName == "" || skillsDir == "" {
			return newSkillsErr(http.StatusBadRequest, "INVALID_INPUT", "Agent key, display name, and skills directory are required")
		}
		for _, a := range m.allAdaptersLocked() {
			if a.Key == key {
				return newSkillsErr(http.StatusConflict, "DUPLICATE_AGENT", fmt.Sprintf("Agent key %q already exists", key))
			}
		}
		cat := req.Category
		if cat != CategoryLobster {
			cat = CategoryCoding
		}
		m.state.CustomTools = append(m.state.CustomTools, CustomToolDef{
			Key:           key,
			DisplayName:   displayName,
			SkillsDir:     skillsDir,
			Category:      cat,
			RecursiveScan: req.RecursiveScan,
		})
	case "remove_custom_tool":
		key := strings.TrimSpace(req.Key)
		nextCustoms := make([]CustomToolDef, 0, len(m.state.CustomTools))
		found := false
		for _, ct := range m.state.CustomTools {
			if ct.Key == key {
				found = true
				continue
			}
			nextCustoms = append(nextCustoms, ct)
		}
		if !found {
			return newSkillsErr(http.StatusNotFound, "CUSTOM_TOOL_NOT_FOUND", "Custom agent not found")
		}
		m.state.CustomTools = nextCustoms
		delete(m.state.CustomToolPaths, key)
		nextDisabled := m.state.DisabledTools[:0]
		for _, d := range m.state.DisabledTools {
			if d != key {
				nextDisabled = append(nextDisabled, d)
			}
		}
		m.state.DisabledTools = nextDisabled
		memo := map[string]string{}
		kept := make([]SkillTarget, 0, len(m.state.Targets))
		for _, t := range m.state.Targets {
			if t.Tool != key {
				kept = append(kept, t)
				continue
			}
			if !m.targetPathReferencedElsewhereLocked(t.SkillID, key, t.TargetPath, memo) {
				_, _ = removeRecordedTarget(t.TargetPath, t.Mode)
			}
		}
		m.state.Targets = kept
	case "set_tool_order":
		m.state.ToolOrder = slices.Clone(req.Order)
	default:
		return newSkillsErr(http.StatusBadRequest, "UNKNOWN_ACTION", "Unknown settings action: "+req.Action)
	}
	return m.saveLocked()
}

func writeSkillsError(w http.ResponseWriter, err error) {
	var apiErr *SkillsAPIError
	if errors.As(err, &apiErr) {
		payload := map[string]any{
			"error": apiErr.Message,
		}
		if apiErr.Code != "" {
			payload["code"] = apiErr.Code
		}
		if len(apiErr.Conflicts) > 0 {
			payload["conflicts"] = apiErr.Conflicts
		}
		writeJSON(w, apiErr.Status, payload)
		return
	}
	if ref, ok := isReplaceRefused(err); ok {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": ref.Error(),
			"code":  "TARGET_CONFLICT",
			"conflicts": []TargetConflictDTO{{
				Path:   ref.Target,
				Reason: ref.Reason,
			}},
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

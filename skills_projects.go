package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// ProjectRecord stores a registered project workspace or linked skills directory in skills_state.json.
type ProjectRecord struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Path            string `json:"path"`
	WorkspaceType   string `json:"workspace_type"` // "project" | "linked"
	LinkedAgentKey  string `json:"linked_agent_key,omitempty"`
	LinkedAgentName string `json:"linked_agent_name,omitempty"`
	DisabledPath    string `json:"disabled_path,omitempty"`
	SortOrder       int    `json:"sort_order"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// SyncHealthDTO summarizes folded skill sync statuses across a workspace.
type SyncHealthDTO struct {
	InSync       int `json:"in_sync"`
	ProjectNewer int `json:"project_newer"`
	CenterNewer  int `json:"center_newer"`
	Diverged     int `json:"diverged"`
	ProjectOnly  int `json:"project_only"`
}

// ProjectDTO represents a project or linked workspace returned to the UI.
type ProjectDTO struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Path                string        `json:"path"`
	DisplayPath         string        `json:"display_path"`
	WorkspaceType       string        `json:"workspace_type"`
	LinkedAgentName     string        `json:"linked_agent_name,omitempty"`
	SupportsSkillToggle bool          `json:"supports_skill_toggle"`
	SortOrder           int           `json:"sort_order"`
	SkillCount          int           `json:"skill_count"`
	SyncHealth          SyncHealthDTO `json:"sync_health"`
	CreatedAt           int64         `json:"created_at"`
	UpdatedAt           int64         `json:"updated_at"`
}

// ProjectAgentConfig describes one project-relative skills directory and the agents sharing it.
type ProjectAgentConfig struct {
	Key               string
	DisplayName       string
	RelativeSkillsDir string
	SharedBy          []string
}

// ProjectAgentTargetDTO describes an agent directory target in a workspace.
type ProjectAgentTargetDTO struct {
	Key               string   `json:"key"`
	DisplayName       string   `json:"display_name"`
	RelativeSkillsDir string   `json:"relative_skills_dir"`
	SharedBy          []string `json:"shared_by"`
	Enabled           bool     `json:"enabled"`
	Installed         bool     `json:"installed"`
	IsCustom          bool     `json:"is_custom"`
}

// ScanProjectItem represents one discovered candidate project directory.
type ScanProjectItem struct {
	Path         string `json:"path"`
	DisplayPath  string `json:"display_path"`
	AlreadyAdded bool   `json:"already_added"`
}

type projectAgentGroup struct {
	Key               string
	SharedBy          []string
	RelativeSkillsDir string
	SkillsRoot        string
	DisabledRoot      string
}

func (m *SkillsManager) projectAgentConfigsLocked() []ProjectAgentConfig {
	type groupItem struct {
		relDir       string
		keys         []string
		displayNames []string
	}
	var grouped []groupItem
	idxByDir := map[string]int{}

	for _, a := range m.allAdaptersLocked() {
		relDir := filepath.ToSlash(strings.TrimSpace(a.ProjectSkillsRelDir()))
		if relDir == "" {
			continue
		}
		if idx, ok := idxByDir[relDir]; ok {
			grouped[idx].keys = append(grouped[idx].keys, a.Key)
			grouped[idx].displayNames = append(grouped[idx].displayNames, a.DisplayName)
		} else {
			idxByDir[relDir] = len(grouped)
			grouped = append(grouped, groupItem{
				relDir:       relDir,
				keys:         []string{a.Key},
				displayNames: []string{a.DisplayName},
			})
		}
	}

	out := make([]ProjectAgentConfig, 0, len(grouped))
	for _, g := range grouped {
		out = append(out, ProjectAgentConfig{
			Key:               g.keys[0],
			DisplayName:       strings.Join(g.displayNames, " / "),
			RelativeSkillsDir: g.relDir,
			SharedBy:          g.keys,
		})
	}
	return out
}

func linkedWorkspaceAgentKey(rec ProjectRecord) string {
	if strings.TrimSpace(rec.LinkedAgentKey) != "" {
		return rec.LinkedAgentKey
	}
	return slugifySkillDirName(rec.Name)
}

func linkedWorkspaceAgentName(rec ProjectRecord) string {
	if strings.TrimSpace(rec.LinkedAgentName) != "" {
		return rec.LinkedAgentName
	}
	return rec.Name
}

func readWorkspaceSkills(rec ProjectRecord, configs []ProjectAgentConfig) []AgentSkill {
	var skills []AgentSkill
	if rec.WorkspaceType == "linked" {
		key := linkedWorkspaceAgentKey(rec)
		name := linkedWorkspaceAgentName(rec)
		skills = append(skills, readAgentLocalSkillsWithEnabled(rec.Path, true, key, name, true)...)
		if rec.DisabledPath != "" {
			skills = append(skills, readAgentLocalSkillsWithEnabled(rec.DisabledPath, false, key, name, true)...)
		}
	} else {
		for _, cfg := range configs {
			relFS := filepath.FromSlash(cfg.RelativeSkillsDir)
			skillsDir := filepath.Join(rec.Path, relFS)
			disabledDir := filepath.Join(rec.Path, relFS+"-disabled")
			skills = append(skills, readAgentLocalSkillsWithEnabled(skillsDir, true, cfg.Key, cfg.DisplayName, true)...)
			skills = append(skills, readAgentLocalSkillsWithEnabled(disabledDir, false, cfg.Key, cfg.DisplayName, true)...)
		}
	}
	sort.Slice(skills, func(i, j int) bool {
		li, lj := strings.ToLower(skills[i].Name), strings.ToLower(skills[j].Name)
		if li != lj {
			return li < lj
		}
		if skills[i].Agent != skills[j].Agent {
			return skills[i].Agent < skills[j].Agent
		}
		return skills[i].RelativePath < skills[j].RelativePath
	})
	return skills
}

func projectSyncStatusPriority(status string) int {
	switch status {
	case "diverged":
		return 5
	case "project_newer":
		return 4
	case "center_newer":
		return 3
	case "project_only":
		return 2
	case "in_sync":
		return 1
	default:
		return 0
	}
}

func (m *SkillsManager) projectToDTO(rec ProjectRecord, allManaged []CenterSkill, configs []ProjectAgentConfig, redactPaths bool) ProjectDTO {
	skills := readWorkspaceSkills(rec, configs)
	grouped := map[string]string{}
	for i := range skills {
		sk := &skills[i]
		matched := findBestProjectCenterMatch(sk, allManaged)
		status := classifySyncStatus(sk, matched)
		key := strings.ToLower(sk.RelativePath)
		prev, exists := grouped[key]
		if !exists || projectSyncStatusPriority(status) > projectSyncStatusPriority(prev) {
			grouped[key] = status
		}
	}

	var health SyncHealthDTO
	for _, st := range grouped {
		switch st {
		case "in_sync":
			health.InSync++
		case "project_newer":
			health.ProjectNewer++
		case "center_newer":
			health.CenterNewer++
		case "diverged":
			health.Diverged++
		default:
			health.ProjectOnly++
		}
	}

	displayPath := rec.Path
	if redactPaths {
		displayPath = m.redactWithHome(rec.Path, true)
	}
	wType := rec.WorkspaceType
	if wType == "" {
		wType = "project"
	}

	return ProjectDTO{
		ID:                  rec.ID,
		Name:                rec.Name,
		Path:                displayPath,
		DisplayPath:         displayPath,
		WorkspaceType:       wType,
		LinkedAgentName:     rec.LinkedAgentName,
		SupportsSkillToggle: wType != "linked" || rec.DisabledPath != "",
		SortOrder:           rec.SortOrder,
		SkillCount:          len(grouped),
		SyncHealth:          health,
		CreatedAt:           rec.CreatedAt,
		UpdatedAt:           rec.UpdatedAt,
	}
}

func hasAnyAgentSkills(dir string, configs []ProjectAgentConfig) bool {
	for _, cfg := range configs {
		if isDirFollow(filepath.Join(dir, filepath.FromSlash(cfg.RelativeSkillsDir))) {
			return true
		}
	}
	return false
}

func scanProjectsInDir(root string, maxDepth int, configs []ProjectAgentConfig) []string {
	var results []string
	visited := map[string]bool{}

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxDepth {
			return
		}
		if canon, err := filepath.EvalSymlinks(dir); err == nil {
			if visited[canon] {
				return
			}
			visited[canon] = true
		}
		if hasAnyAgentSkills(dir, configs) {
			results = append(results, dir)
			return
		}
		if depth == maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") ||
				name == "node_modules" ||
				name == "target" ||
				name == "__pycache__" ||
				name == "vendor" {
				continue
			}
			child := filepath.Join(dir, name)
			if !isDirFollow(child) {
				continue
			}
			walk(child, depth+1)
		}
	}

	walk(root, 0)
	sort.Strings(results)
	return results
}

func (m *SkillsManager) findProjectLocked(projectID string) (int, ProjectRecord, error) {
	for i, p := range m.state.Projects {
		if p.ID == projectID {
			return i, p, nil
		}
	}
	return -1, ProjectRecord{}, newSkillsErr(http.StatusNotFound, "PROJECT_NOT_FOUND", "Workspace not found")
}

func canonicalPathOrAbs(p string) (string, error) {
	if canon, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(canon), nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func (m *SkillsManager) hasDuplicateProjectLocked(canonPath string) *ProjectRecord {
	for i := range m.state.Projects {
		existing := &m.state.Projects[i]
		if existing.Path == canonPath {
			return existing
		}
		if existingCanon, err := filepath.EvalSymlinks(existing.Path); err == nil && existingCanon == canonPath {
			return existing
		}
	}
	return nil
}

func (m *SkillsManager) projectAgentTargetsForRecordLocked(rec ProjectRecord) []ProjectAgentTargetDTO {
	if rec.WorkspaceType == "linked" {
		return []ProjectAgentTargetDTO{{
			Key:         linkedWorkspaceAgentKey(rec),
			DisplayName: linkedWorkspaceAgentName(rec),
			SharedBy:    []string{linkedWorkspaceAgentKey(rec)},
			Enabled:     true,
			Installed:   true,
			IsCustom:    false,
		}}
	}

	homeDir, configDir := m.dirs()
	adapters := m.allAdaptersLocked()
	configs := m.projectAgentConfigsLocked()
	disabled := m.state.DisabledTools

	out := make([]ProjectAgentTargetDTO, 0, len(configs))
	for _, cfg := range configs {
		var sharing []ToolAdapter
		var installedFlags []bool
		anyInstalled := false
		for _, a := range adapters {
			if filepath.ToSlash(strings.TrimSpace(a.ProjectSkillsRelDir())) == cfg.RelativeSkillsDir {
				inst := a.IsInstalled(homeDir, configDir)
				sharing = append(sharing, a)
				installedFlags = append(installedFlags, inst)
				if inst {
					anyInstalled = true
				}
			}
		}
		enabled := false
		for i, a := range sharing {
			if (installedFlags[i] || !anyInstalled) && !slices.Contains(disabled, a.Key) {
				enabled = true
				break
			}
		}
		isCustom := len(sharing) > 0 && sharing[0].IsCustom
		out = append(out, ProjectAgentTargetDTO{
			Key:               cfg.Key,
			DisplayName:       cfg.DisplayName,
			RelativeSkillsDir: cfg.RelativeSkillsDir,
			SharedBy:          slices.Clone(cfg.SharedBy),
			Enabled:           enabled,
			Installed:         anyInstalled,
			IsCustom:          isCustom,
		})
	}
	return out
}

func (m *SkillsManager) resolveProjectAgentGroupLocked(rec ProjectRecord, agent string) (projectAgentGroup, bool) {
	if rec.WorkspaceType == "linked" {
		key := linkedWorkspaceAgentKey(rec)
		if agent != key {
			return projectAgentGroup{}, false
		}
		return projectAgentGroup{
			Key:          key,
			SharedBy:     []string{key},
			SkillsRoot:   rec.Path,
			DisabledRoot: rec.DisabledPath,
		}, true
	}

	configs := m.projectAgentConfigsLocked()
	for _, cfg := range configs {
		if cfg.Key == agent || slices.Contains(cfg.SharedBy, agent) {
			relFS := filepath.FromSlash(cfg.RelativeSkillsDir)
			return projectAgentGroup{
				Key:               cfg.Key,
				SharedBy:          slices.Clone(cfg.SharedBy),
				RelativeSkillsDir: cfg.RelativeSkillsDir,
				SkillsRoot:        filepath.Join(rec.Path, relFS),
				DisabledRoot:      filepath.Join(rec.Path, relFS+"-disabled"),
			}, true
		}
	}
	return projectAgentGroup{}, false
}

// ListProjects returns all registered project and linked workspaces sorted by SortOrder then CreatedAt.
func (m *SkillsManager) ListProjects(redactPaths bool) []ProjectDTO {
	m.mu.Lock()
	projects := slices.Clone(m.state.Projects)
	managed := slices.Clone(m.state.Skills)
	configs := m.projectAgentConfigsLocked()
	m.mu.Unlock()

	sort.SliceStable(projects, func(i, j int) bool {
		if projects[i].SortOrder != projects[j].SortOrder {
			return projects[i].SortOrder < projects[j].SortOrder
		}
		return projects[i].CreatedAt < projects[j].CreatedAt
	})

	out := make([]ProjectDTO, 0, len(projects))
	for _, rec := range projects {
		out = append(out, m.projectToDTO(rec, managed, configs, redactPaths))
	}
	return out
}

// AddProject registers an existing local directory as a project workspace and initializes .claude/skills{,-disabled}.
func (m *SkillsManager) AddProject(rawPath string, redactPaths bool) (*ProjectDTO, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	homeDir, _ := m.dirs()
	expanded := expandPathWithHome(rawPath, homeDir)
	if expanded == "" || !isDirFollow(expanded) {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_DIR", "Directory does not exist")
	}
	canon, err := canonicalPathOrAbs(expanded)
	if err != nil {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_DIR", err.Error())
	}
	if dup := m.hasDuplicateProjectLocked(canon); dup != nil {
		return nil, newSkillsErr(http.StatusConflict, "DUPLICATE_PROJECT",
			fmt.Sprintf("Project %q is already registered at %s", dup.Name, m.redactWithHome(dup.Path, redactPaths)))
	}

	claudeDir := filepath.Join(canon, ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "skills"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(claudeDir, "skills-disabled"), 0o755); err != nil {
		return nil, err
	}

	name := filepath.Base(canon)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "project"
	}
	now := time.Now().UnixMilli()
	rec := ProjectRecord{
		ID:            newSkillUUID(),
		Name:          name,
		Path:          canon,
		WorkspaceType: "project",
		SortOrder:     len(m.state.Projects),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	m.state.Projects = append(m.state.Projects, rec)
	if err := m.saveLocked(); err != nil {
		m.state.Projects = m.state.Projects[:len(m.state.Projects)-1]
		return nil, err
	}
	dto := m.projectToDTO(rec, m.state.Skills, m.projectAgentConfigsLocked(), redactPaths)
	return &dto, nil
}

func ensureDistinctLinkedRoots(skillsRoot, disabledRoot string) error {
	c1, err1 := filepath.EvalSymlinks(skillsRoot)
	if err1 != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err1.Error())
	}
	c2, err2 := filepath.EvalSymlinks(disabledRoot)
	if err2 != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err2.Error())
	}
	if c1 == c2 || isSubpath(c1, c2) || isSubpath(c2, c1) {
		return newSkillsErr(http.StatusBadRequest, "OVERLAPPING_ROOTS", "Skills directory and disabled skills directory must not overlap")
	}
	return nil
}

// AddLinkedWorkspace registers an arbitrary external skills directory as a linked workspace.
func (m *SkillsManager) AddLinkedWorkspace(name, rawPath, rawDisabledPath string, redactPaths bool) (*ProjectDTO, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_NAME", "Workspace name is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	homeDir, _ := m.dirs()
	expanded := expandPathWithHome(rawPath, homeDir)
	if expanded == "" || !isDirFollow(expanded) {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_DIR", "Skills directory does not exist")
	}
	canonSkills, err := canonicalPathOrAbs(expanded)
	if err != nil {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_DIR", err.Error())
	}
	if dup := m.hasDuplicateProjectLocked(canonSkills); dup != nil {
		return nil, newSkillsErr(http.StatusConflict, "DUPLICATE_PROJECT",
			fmt.Sprintf("Workspace %q is already registered at %s", dup.Name, m.redactWithHome(dup.Path, redactPaths)))
	}

	disabledPath := ""
	if strings.TrimSpace(rawDisabledPath) != "" {
		expDisabled := expandPathWithHome(rawDisabledPath, homeDir)
		if expDisabled == "" || !isDirFollow(expDisabled) {
			return nil, newSkillsErr(http.StatusBadRequest, "INVALID_DISABLED_DIR", "Disabled skills directory does not exist")
		}
		if err := ensureDistinctLinkedRoots(canonSkills, expDisabled); err != nil {
			return nil, err
		}
		canonDis, err := canonicalPathOrAbs(expDisabled)
		if err != nil {
			return nil, newSkillsErr(http.StatusBadRequest, "INVALID_DISABLED_DIR", err.Error())
		}
		disabledPath = canonDis
	} else {
		base := filepath.Base(expanded)
		if base != "" && base != "." && base != string(filepath.Separator) {
			candidate := filepath.Join(filepath.Dir(canonSkills), base+"-disabled")
			if err := os.MkdirAll(candidate, 0o755); err == nil {
				if ensureDistinctLinkedRoots(canonSkills, candidate) == nil {
					if canonDis, err := canonicalPathOrAbs(candidate); err == nil {
						disabledPath = canonDis
					}
				}
			}
		}
	}

	now := time.Now().UnixMilli()
	rec := ProjectRecord{
		ID:              newSkillUUID(),
		Name:            trimmedName,
		Path:            canonSkills,
		WorkspaceType:   "linked",
		LinkedAgentKey:  slugifySkillDirName(trimmedName),
		LinkedAgentName: trimmedName,
		DisabledPath:    disabledPath,
		SortOrder:       len(m.state.Projects),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	m.state.Projects = append(m.state.Projects, rec)
	if err := m.saveLocked(); err != nil {
		m.state.Projects = m.state.Projects[:len(m.state.Projects)-1]
		return nil, err
	}
	dto := m.projectToDTO(rec, m.state.Skills, m.projectAgentConfigsLocked(), redactPaths)
	return &dto, nil
}

// RemoveProject removes a workspace registration from state without touching disk files.
func (m *SkillsManager) RemoveProject(projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx, _, err := m.findProjectLocked(projectID)
	if err != nil {
		return err
	}
	m.state.Projects = append(m.state.Projects[:idx], m.state.Projects[idx+1:]...)
	return m.saveLocked()
}

// ReorderProjects updates workspace ordering according to ids.
func (m *SkillsManager) ReorderProjects(ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	rank := make(map[string]int, len(ids))
	for i, id := range ids {
		rank[id] = i
	}
	nextOrder := len(ids)
	for i := range m.state.Projects {
		if r, ok := rank[m.state.Projects[i].ID]; ok {
			m.state.Projects[i].SortOrder = r
		} else {
			m.state.Projects[i].SortOrder = nextOrder
			nextOrder++
		}
	}
	sort.SliceStable(m.state.Projects, func(i, j int) bool {
		return m.state.Projects[i].SortOrder < m.state.Projects[j].SortOrder
	})
	return m.saveLocked()
}

// ScanProjects scans rawRoot up to depth 4 for directories containing any agent skills directory.
func (m *SkillsManager) ScanProjects(rawRoot string, redactPaths bool) ([]ScanProjectItem, error) {
	m.mu.Lock()
	homeDir, _ := m.dirs()
	configs := m.projectAgentConfigsLocked()
	existingCanon := map[string]bool{}
	for _, p := range m.state.Projects {
		existingCanon[p.Path] = true
		if c, err := filepath.EvalSymlinks(p.Path); err == nil {
			existingCanon[c] = true
		}
	}
	m.mu.Unlock()

	root := expandPathWithHome(rawRoot, homeDir)
	if root == "" || !isDirFollow(root) {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_DIR", "Directory does not exist")
	}

	found := scanProjectsInDir(root, 4, configs)
	out := make([]ScanProjectItem, 0, len(found))
	for _, p := range found {
		canon, _ := canonicalPathOrAbs(p)
		already := existingCanon[p] || (canon != "" && existingCanon[canon])
		shown := m.redactWithHome(p, redactPaths)
		out = append(out, ScanProjectItem{
			Path:         shown,
			DisplayPath:  shown,
			AlreadyAdded: already,
		})
	}
	return out, nil
}

// GetProjectAgentTargets returns the deduplicated agent targets usable in the given workspace.
func (m *SkillsManager) GetProjectAgentTargets(projectID string) ([]ProjectAgentTargetDTO, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, rec, err := m.findProjectLocked(projectID)
	if err != nil {
		return nil, err
	}
	return m.projectAgentTargetsForRecordLocked(rec), nil
}

// GetProjectSkills scans enabled and disabled skill directories across the workspace and matches them against central skills.
func (m *SkillsManager) GetProjectSkills(projectID string, redactPaths bool) ([]AgentSkill, error) {
	m.mu.Lock()
	_, rec, err := m.findProjectLocked(projectID)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	configs := m.projectAgentConfigsLocked()
	managed := slices.Clone(m.state.Skills)
	m.mu.Unlock()

	skills := readWorkspaceSkills(rec, configs)
	for i := range skills {
		sk := &skills[i]
		matched := findBestProjectCenterMatch(sk, managed)
		if matched != nil {
			sk.InCenter = true
			sk.CenterSkillID = matched.ID
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
	return skills, nil
}

func findProjectSkillVariant(skills []AgentSkill, relPath, groupKey string) *AgentSkill {
	for i := range skills {
		if skills[i].RelativePath == relPath && skills[i].Agent == groupKey {
			return &skills[i]
		}
	}
	for i := range skills {
		if strings.EqualFold(skills[i].RelativePath, relPath) && skills[i].Agent == groupKey {
			return &skills[i]
		}
	}
	return nil
}

// GetProjectSkillDocument returns the markdown documentation for one workspace skill copy.
func (m *SkillsManager) GetProjectSkillDocument(projectID, relPath, agent string, redactContent bool) (SkillDocumentResp, error) {
	if err := ensureSafeSkillRelPath(relPath); err != nil {
		return SkillDocumentResp{}, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}

	m.mu.Lock()
	_, rec, err := m.findProjectLocked(projectID)
	if err != nil {
		m.mu.Unlock()
		return SkillDocumentResp{}, err
	}
	group, ok := m.resolveProjectAgentGroupLocked(rec, agent)
	centralDir := m.centralDir
	m.mu.Unlock()
	if !ok {
		return SkillDocumentResp{}, newSkillsErr(http.StatusNotFound, "UNKNOWN_AGENT", "Unknown workspace agent: "+agent)
	}

	var skillDir string
	enabledCandidate := filepath.Join(group.SkillsRoot, filepath.FromSlash(relPath))
	if isDirFollow(enabledCandidate) {
		if err := ensureDirWithinRoot(enabledCandidate, group.SkillsRoot); err != nil {
			return SkillDocumentResp{}, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
		}
		skillDir = enabledCandidate
	} else if group.DisabledRoot != "" {
		disabledCandidate := filepath.Join(group.DisabledRoot, filepath.FromSlash(relPath))
		if isDirFollow(disabledCandidate) {
			if err := ensureDirWithinRoot(disabledCandidate, group.DisabledRoot); err != nil {
				return SkillDocumentResp{}, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
			}
			skillDir = disabledCandidate
		}
	}
	if skillDir == "" {
		return SkillDocumentResp{}, newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill directory not found")
	}

	allowed := []string{group.SkillsRoot}
	if group.DisabledRoot != "" {
		allowed = append(allowed, group.DisabledRoot)
	}
	if rec.WorkspaceType != "linked" {
		allowed = append(allowed, rec.Path)
	}
	if centralDir != "" {
		allowed = append(allowed, centralDir)
	}
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

// ImportProjectSkillToCenter pushes a project or linked-workspace skill copy into the central library.
func (m *SkillsManager) ImportProjectSkillToCenter(projectID, relPath, agent string) (*CenterSkill, error) {
	if err := ensureSafeSkillRelPath(relPath); err != nil {
		return nil, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	_, rec, err := m.findProjectLocked(projectID)
	if err != nil {
		return nil, err
	}
	group, ok := m.resolveProjectAgentGroupLocked(rec, agent)
	if !ok {
		return nil, newSkillsErr(http.StatusNotFound, "UNKNOWN_AGENT", "Unknown workspace agent: "+agent)
	}

	configs := m.projectAgentConfigsLocked()
	variants := readWorkspaceSkills(rec, configs)
	found := findProjectSkillVariant(variants, relPath, group.Key)
	if found == nil {
		return nil, newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found in workspace")
	}
	if err := ensureDirWithinRoot(found.Path, group.SkillsRoot); err != nil {
		if group.DisabledRoot == "" || ensureDirWithinRoot(found.Path, group.DisabledRoot) != nil {
			return nil, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
		}
	}

	now := time.Now().UnixMilli()
	matched := findBestProjectCenterMatch(found, m.state.Skills)
	if matched != nil {
		var targetSkill *CenterSkill
		for i := range m.state.Skills {
			if m.state.Skills[i].ID == matched.ID {
				targetSkill = &m.state.Skills[i]
				break
			}
		}
		if targetSkill != nil {
			canonSrc, errSrc := filepath.EvalSymlinks(found.Path)
			if errSrc != nil || canonSrc != targetSkill.CentralPath {
				stageDir := targetSkill.CentralPath + ".importing-" + newSkillUUID()
				if err := copyDirRecursive(found.Path, stageDir); err != nil {
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
			targetSkill.Description = desc
			targetSkill.ContentHash = hashDirectory(targetSkill.CentralPath)
			targetSkill.UpdatedAt = now
			if err := m.saveLocked(); err != nil {
				return nil, err
			}
			cp := *targetSkill
			return &cp, nil
		}
	}

	name := inferSkillName(found.Path)
	_, desc := parseSkillMD(found.Path)
	destDir, err := m.allocateCentralDirLocked(name)
	if err != nil {
		return nil, err
	}
	if err := copyDirRecursive(found.Path, destDir); err != nil {
		_ = os.RemoveAll(destDir)
		return nil, err
	}
	newRec := CenterSkill{
		ID:          newSkillUUID(),
		Name:        name,
		Description: desc,
		SourceType:  "local",
		SourceRef:   found.Path,
		CentralPath: destDir,
		ContentHash: hashDirectory(destDir),
		Tags:        []string{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	m.state.Skills = append(m.state.Skills, newRec)
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	cp := newRec
	return &cp, nil
}

// ExportSkillsToProject deploys one or more central skills to selected agent groups inside a workspace.
func (m *SkillsManager) ExportSkillsToProject(projectID string, skillIDs, agents []string) (SyncBatchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, rec, err := m.findProjectLocked(projectID)
	if err != nil {
		return SyncBatchResult{}, err
	}
	if !isDirFollow(rec.Path) {
		return SyncBatchResult{}, newSkillsErr(http.StatusBadRequest, "WORKSPACE_MISSING", "Workspace directory no longer exists: "+rec.Path)
	}

	reqAgents := agents
	if len(reqAgents) == 0 {
		if rec.WorkspaceType == "linked" {
			reqAgents = []string{linkedWorkspaceAgentKey(rec)}
		} else {
			reqAgents = []string{"claude_code"}
		}
	}

	rows := m.projectAgentTargetsForRecordLocked(rec)
	var groups []projectAgentGroup
	seenGroup := map[string]bool{}
	for _, ak := range reqAgents {
		g, ok := m.resolveProjectAgentGroupLocked(rec, ak)
		if !ok {
			return SyncBatchResult{}, newSkillsErr(http.StatusNotFound, "UNKNOWN_AGENT", "Unknown agent: "+ak)
		}
		if rec.WorkspaceType != "linked" {
			for _, row := range rows {
				if row.Key == g.Key {
					if !row.Installed {
						return SyncBatchResult{}, newSkillsErr(http.StatusBadRequest, "AGENT_NOT_INSTALLED", "Agent is not installed: "+ak)
					}
					if !row.Enabled {
						return SyncBatchResult{}, newSkillsErr(http.StatusBadRequest, "AGENT_DISABLED", "Agent is disabled: "+ak)
					}
					break
				}
			}
		}
		if !seenGroup[g.Key] {
			seenGroup[g.Key] = true
			groups = append(groups, g)
		}
	}

	configs := m.projectAgentConfigsLocked()
	variants := readWorkspaceSkills(rec, configs)

	type deployPlanItem struct {
		skillID    string
		skillName  string
		source     string
		agent      string
		targetPath string
		state      string // "create" | "already_deployed" | "conflict"
	}

	var plan []deployPlanItem
	for _, sid := range skillIDs {
		var sk *CenterSkill
		for i := range m.state.Skills {
			if m.state.Skills[i].ID == sid {
				sk = &m.state.Skills[i]
				break
			}
		}
		if sk == nil {
			return SyncBatchResult{}, newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found")
		}
		if !isDirFollow(sk.CentralPath) {
			return SyncBatchResult{}, newSkillsErr(http.StatusNotFound, "CENTRAL_MISSING", "Central skill directory is missing")
		}
		dirName := targetDirName(sk.CentralPath, sk.Name)
		if err := ensureSafeSkillRelPath(dirName); err != nil {
			return SyncBatchResult{}, newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
		}

		for _, g := range groups {
			enabledPath := filepath.Join(g.SkillsRoot, dirName)
			var occupiedPath string
			if _, err := os.Lstat(enabledPath); err == nil {
				occupiedPath = enabledPath
			} else if g.DisabledRoot != "" {
				disabledPath := filepath.Join(g.DisabledRoot, dirName)
				if _, err := os.Lstat(disabledPath); err == nil {
					occupiedPath = disabledPath
				}
			}

			if occupiedPath == "" {
				plan = append(plan, deployPlanItem{
					skillID:    sk.ID,
					skillName:  sk.Name,
					source:     sk.CentralPath,
					agent:      g.Key,
					targetPath: enabledPath,
					state:      "create",
				})
				continue
			}

			variant := findProjectSkillVariant(variants, dirName, g.Key)
			var matched *CenterSkill
			if variant != nil {
				matched = findBestProjectCenterMatch(variant, m.state.Skills)
			}
			if variant != nil && matched != nil && matched.ID == sk.ID {
				plan = append(plan, deployPlanItem{
					skillID:    sk.ID,
					skillName:  sk.Name,
					source:     sk.CentralPath,
					agent:      g.Key,
					targetPath: occupiedPath,
					state:      "already_deployed",
				})
			} else {
				plan = append(plan, deployPlanItem{
					skillID:    sk.ID,
					skillName:  sk.Name,
					source:     sk.CentralPath,
					agent:      g.Key,
					targetPath: occupiedPath,
					state:      "conflict",
				})
			}
		}
	}

	// Refuse batch if two different library skills collide on the same target path.
	var batchCollisions []TargetConflictDTO
	for i, item := range plan {
		if item.state != "create" {
			continue
		}
		for _, prev := range plan[:i] {
			if prev.state == "create" && prev.targetPath == item.targetPath && prev.skillID != item.skillID {
				batchCollisions = append(batchCollisions, TargetConflictDTO{
					Path:   item.targetPath,
					Reason: fmt.Sprintf("skills %q and %q would both deploy to the same path", prev.skillName, item.skillName),
				})
				break
			}
		}
	}
	if len(batchCollisions) > 0 {
		return SyncBatchResult{OK: false, Conflicts: batchCollisions}, &SkillsAPIError{
			Status:    http.StatusConflict,
			Code:      "TARGET_CONFLICT",
			Message:   fmt.Sprintf("Refusing to deploy: %d target path collision(s)", len(batchCollisions)),
			Conflicts: batchCollisions,
		}
	}

	res := SyncBatchResult{OK: true}
	singlePair := len(skillIDs) == 1 && len(groups) == 1
	written := map[string]bool{}
	syncMode := normalizeSyncMode(m.state.SyncMode)

	for _, item := range plan {
		switch item.state {
		case "already_deployed":
			res.Skipped++
		case "conflict":
			c := TargetConflictDTO{
				Path:   item.targetPath,
				Reason: "exists and is not this library skill",
			}
			res.Conflicts = append(res.Conflicts, c)
			if singlePair {
				return res, &SkillsAPIError{
					Status:    http.StatusConflict,
					Code:      "TARGET_CONFLICT",
					Message:   fmt.Sprintf("Skill %q cannot overwrite existing path %s", item.skillName, item.targetPath),
					Conflicts: res.Conflicts,
				}
			}
		case "create":
			if !written[item.targetPath] {
				if err := os.MkdirAll(filepath.Dir(item.targetPath), 0o755); err != nil {
					if singlePair {
						return res, err
					}
					res.Failed++
					res.Failures = append(res.Failures, SyncPairFailure{
						SkillID: item.skillID,
						Agent:   item.agent,
						Error:   err.Error(),
					})
					continue
				}
				if _, err := syncSkill(item.source, item.targetPath, syncMode, ReplacePolicy{Kind: PolicyNoClobber}); err != nil {
					if singlePair {
						return res, err
					}
					if ref, ok := isReplaceRefused(err); ok {
						res.Conflicts = append(res.Conflicts, TargetConflictDTO{
							Path:   ref.Target,
							Reason: ref.Reason,
						})
					} else {
						res.Failed++
						res.Failures = append(res.Failures, SyncPairFailure{
							SkillID: item.skillID,
							Agent:   item.agent,
							Error:   err.Error(),
						})
					}
					continue
				}
				written[item.targetPath] = true
			}
			res.Applied++
		}
	}
	res.OK = res.Failed == 0 && len(res.Conflicts) == 0
	return res, nil
}

// UpdateProjectSkillFromCenter pulls the central library skill back into a project skill slot.
func (m *SkillsManager) UpdateProjectSkillFromCenter(projectID, relPath, agent string, force bool) error {
	if err := ensureSafeSkillRelPath(relPath); err != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	_, rec, err := m.findProjectLocked(projectID)
	if err != nil {
		return err
	}
	group, ok := m.resolveProjectAgentGroupLocked(rec, agent)
	if !ok {
		return newSkillsErr(http.StatusNotFound, "UNKNOWN_AGENT", "Unknown agent: "+agent)
	}

	variants := readWorkspaceSkills(rec, m.projectAgentConfigsLocked())
	found := findProjectSkillVariant(variants, relPath, group.Key)
	if found == nil {
		return newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill not found in workspace")
	}
	matched := findBestProjectCenterMatch(found, m.state.Skills)
	if matched == nil {
		return newSkillsErr(http.StatusNotFound, "CENTER_NOT_FOUND", "No matching skill in central library")
	}
	if !force && classifySyncStatus(found, matched) == "project_newer" {
		return newSkillsErr(http.StatusBadRequest, "LOCAL_NEWER", "Project skill is newer than the Skills Center version")
	}
	if ensureDirWithinRoot(found.Path, group.SkillsRoot) != nil {
		if group.DisabledRoot == "" || ensureDirWithinRoot(found.Path, group.DisabledRoot) != nil {
			return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", "Invalid skill directory path")
		}
	}
	syncMode := normalizeSyncMode(m.state.SyncMode)
	_, err = syncSkill(matched.CentralPath, found.Path, syncMode, ReplacePolicy{Kind: PolicyUserConfirmed})
	return err
}

func removeSymlinkDuplicate(p string) error {
	lstat, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if lstat.Mode()&os.ModeSymlink == 0 {
		return newSkillsErr(http.StatusConflict, "DUPLICATE_NOT_SYMLINK", "Duplicate skill entry is not a symlink — resolve manually")
	}
	return removeTarget(p)
}

func cleanupEmptyDirsUpTo(start, root string) {
	rootCanon, err := filepath.EvalSymlinks(root)
	if err != nil {
		return
	}
	curr := start
	for {
		currCanon, err := filepath.EvalSymlinks(curr)
		if err != nil || !isSubpath(currCanon, rootCanon) {
			return
		}
		if err := os.Remove(curr); err != nil {
			return
		}
		if currCanon == rootCanon {
			return
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			return
		}
		curr = parent
	}
}

// ToggleProjectSkill moves a project/linked skill between skillsRoot and disabledRoot.
func (m *SkillsManager) ToggleProjectSkill(projectID, relPath, agent string, enabled bool) error {
	if err := ensureSafeSkillRelPath(relPath); err != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	_, rec, err := m.findProjectLocked(projectID)
	if err != nil {
		return err
	}
	group, ok := m.resolveProjectAgentGroupLocked(rec, agent)
	if !ok {
		return newSkillsErr(http.StatusNotFound, "UNKNOWN_AGENT", "Unknown agent: "+agent)
	}
	if group.DisabledRoot == "" {
		return newSkillsErr(http.StatusBadRequest, "TOGGLE_UNSUPPORTED", "This workspace does not support disabling skills")
	}

	relFS := filepath.FromSlash(relPath)
	enabledPath := filepath.Join(group.SkillsRoot, relFS)
	disabledPath := filepath.Join(group.DisabledRoot, relFS)

	if enabled {
		if isDirFollow(enabledPath) {
			if err := ensureDirWithinRoot(enabledPath, group.SkillsRoot); err != nil {
				return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
			}
			if _, err := os.Lstat(disabledPath); err == nil {
				if err := ensureDirWithinRoot(disabledPath, group.DisabledRoot); err != nil {
					return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
				}
				if err := removeSymlinkDuplicate(disabledPath); err != nil {
					return err
				}
				cleanupEmptyDirsUpTo(filepath.Dir(disabledPath), group.DisabledRoot)
			}
			return nil
		}
		if !isDirFollow(disabledPath) {
			return newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill directory not found in skills-disabled")
		}
		if err := ensureDirWithinRoot(disabledPath, group.DisabledRoot); err != nil {
			return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
		}
		if err := os.MkdirAll(filepath.Dir(enabledPath), 0o755); err != nil {
			return err
		}
		if _, err := os.Lstat(enabledPath); err == nil {
			return newSkillsErr(http.StatusConflict, "ALREADY_EXISTS", "Skill already exists in skills directory")
		}
		if err := os.Rename(disabledPath, enabledPath); err != nil {
			return err
		}
		cleanupEmptyDirsUpTo(filepath.Dir(disabledPath), group.DisabledRoot)
		return nil
	}

	if isDirFollow(disabledPath) {
		if err := ensureDirWithinRoot(disabledPath, group.DisabledRoot); err != nil {
			return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
		}
		if _, err := os.Lstat(enabledPath); err == nil {
			if err := ensureDirWithinRoot(enabledPath, group.SkillsRoot); err != nil {
				return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
			}
			if err := removeSymlinkDuplicate(enabledPath); err != nil {
				return err
			}
		}
		return nil
	}
	if !isDirFollow(enabledPath) {
		return newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill directory not found")
	}
	if err := ensureDirWithinRoot(enabledPath, group.SkillsRoot); err != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(disabledPath), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(disabledPath); err == nil {
		return newSkillsErr(http.StatusConflict, "ALREADY_EXISTS", "Skill already exists in skills-disabled directory")
	}
	return os.Rename(enabledPath, disabledPath)
}

// DeleteProjectSkill removes a skill directory (or symlink) from a project or linked workspace without touching the central copy.
func (m *SkillsManager) DeleteProjectSkill(projectID, relPath, agent string) error {
	if err := ensureSafeSkillRelPath(relPath); err != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	_, rec, err := m.findProjectLocked(projectID)
	if err != nil {
		return err
	}
	if !isDirFollow(rec.Path) {
		return newSkillsErr(http.StatusBadRequest, "WORKSPACE_MISSING", "Workspace directory no longer exists: "+rec.Path)
	}
	group, ok := m.resolveProjectAgentGroupLocked(rec, agent)
	if !ok {
		return newSkillsErr(http.StatusNotFound, "UNKNOWN_AGENT", "Unknown agent: "+agent)
	}

	relFS := filepath.FromSlash(relPath)
	enabledCandidate := filepath.Join(group.SkillsRoot, relFS)
	var target, root string
	if isDirFollow(enabledCandidate) {
		target, root = enabledCandidate, group.SkillsRoot
	} else if group.DisabledRoot != "" {
		disabledCandidate := filepath.Join(group.DisabledRoot, relFS)
		if isDirFollow(disabledCandidate) {
			target, root = disabledCandidate, group.DisabledRoot
		}
	}
	if target == "" {
		return newSkillsErr(http.StatusNotFound, "SKILL_NOT_FOUND", "Skill directory not found")
	}
	if err := ensureDirWithinRoot(target, root); err != nil {
		return newSkillsErr(http.StatusBadRequest, "INVALID_PATH", err.Error())
	}
	return removeTarget(target)
}

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type sessionFileInfo struct {
	path     string
	modTime  time.Time
	kind     string // "claude", "codex", "auto"
	isCustom bool
}

type ScanDirStatus struct {
	Path        string `json:"path"`
	DisplayPath string `json:"display_path,omitempty"`
	Kind        string `json:"kind"`
	Exists      bool   `json:"exists"`
	Files       int    `json:"files"`
}

type ScanManager struct {
	mu          sync.Mutex
	store       *Store
	configPath  string
	IncludeHome bool     `json:"include_home"`
	CustomDirs  []string `json:"custom_dirs"`
	MaxFiles    int      `json:"max_files"`
}

type scanConfigDisk struct {
	IncludeHome bool     `json:"include_home"`
	CustomDirs  []string `json:"custom_dirs"`
	MaxFiles    int      `json:"max_files,omitempty"`
}

func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" {
		if home, _ := os.UserHomeDir(); home != "" {
			return filepath.Clean(home)
		}
	} else if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, _ := os.UserHomeDir(); home != "" {
			return filepath.Clean(filepath.Join(home, p[2:]))
		}
	}
	return filepath.Clean(p)
}

func NewScanManager(store *Store, dataDir string, initialDirs []string, includeHome bool) *ScanManager {
	m := &ScanManager{
		store:       store,
		IncludeHome: includeHome,
		MaxFiles:    200,
	}
	if dataDir != "" {
		m.configPath = filepath.Join(dataDir, "scan_dirs.json")
	}

	loadedFromDisk := false
	if m.configPath != "" {
		if data, err := os.ReadFile(m.configPath); err == nil {
			var cfg scanConfigDisk
			if json.Unmarshal(data, &cfg) == nil {
				m.IncludeHome = cfg.IncludeHome
				for _, d := range cfg.CustomDirs {
					if d = expandPath(d); d != "" && !slices.Contains(m.CustomDirs, d) {
						m.CustomDirs = append(m.CustomDirs, d)
					}
				}
				if cfg.MaxFiles > 0 {
					m.MaxFiles = cfg.MaxFiles
				}
				loadedFromDisk = true
			}
		}
	}

	changed := false
	for _, d := range initialDirs {
		if d = expandPath(d); d != "" && !slices.Contains(m.CustomDirs, d) {
			m.CustomDirs = append(m.CustomDirs, d)
			changed = true
		}
	}
	if m.configPath != "" && (!loadedFromDisk || changed) {
		_ = m.saveLocked()
	}
	return m
}

func (m *ScanManager) saveLocked() error {
	if m.configPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.configPath), 0o755); err != nil {
		return err
	}
	cfg := scanConfigDisk{
		IncludeHome: m.IncludeHome,
		CustomDirs:  m.CustomDirs,
		MaxFiles:    m.MaxFiles,
	}
	if cfg.CustomDirs == nil {
		cfg.CustomDirs = []string{}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.configPath, append(data, '\n'), 0o644)
}

func (m *ScanManager) Scan() int {
	m.mu.Lock()
	custom := slices.Clone(m.CustomDirs)
	maxFiles := m.MaxFiles
	incHome := m.IncludeHome
	m.mu.Unlock()
	return ScanLocalSessions(m.store, custom, maxFiles, incHome)
}

func (m *ScanManager) Status(redactPaths bool) (defaultDirs []ScanDirStatus, customDirs []ScanDirStatus, includeHome bool) {
	m.mu.Lock()
	incHome := m.IncludeHome
	custom := slices.Clone(m.CustomDirs)
	m.mu.Unlock()

	mkStatus := func(p, kind string, allowJSON bool) ScanDirStatus {
		st := ScanDirStatus{
			Path:        p,
			DisplayPath: p,
			Kind:        kind,
		}
		if redactPaths {
			st.DisplayPath = replaceHomePaths(p)
		}
		info, err := os.Stat(p)
		if err != nil {
			return st
		}
		st.Exists = true
		if !info.IsDir() {
			lp := strings.ToLower(p)
			if strings.HasSuffix(lp, ".jsonl") || (allowJSON && strings.HasSuffix(lp, ".json")) {
				st.Files = 1
			}
			return st
		}
		_ = filepath.WalkDir(p, func(fp string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "subagents" || d.Name() == ".git" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			ln := strings.ToLower(d.Name())
			if strings.HasSuffix(ln, ".jsonl") || (allowJSON && strings.HasSuffix(ln, ".json")) {
				if fi, e := d.Info(); e == nil && fi.Size() > 0 {
					st.Files++
				}
			}
			return nil
		})
		return st
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		defaultDirs = append(defaultDirs,
			mkStatus(filepath.Join(home, ".claude", "projects"), "claude", false),
			mkStatus(filepath.Join(home, ".codex", "sessions"), "codex", false),
			mkStatus(filepath.Join(home, ".joycode", "sessions"), "joycode", false),
		)
	}
	for _, d := range custom {
		customDirs = append(customDirs, mkStatus(d, "custom", true))
	}
	return defaultDirs, customDirs, incHome
}

func (m *ScanManager) AddDirs(paths []string) ([]string, int, error) {
	m.mu.Lock()
	var added []string
	for _, raw := range paths {
		p := expandPath(raw)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			m.mu.Unlock()
			return nil, 0, fmt.Errorf("path not accessible: %s (%w)", raw, err)
		}
		if !slices.Contains(m.CustomDirs, p) {
			m.CustomDirs = append(m.CustomDirs, p)
			added = append(added, p)
		}
	}
	err := m.saveLocked()
	custom := slices.Clone(m.CustomDirs)
	maxFiles := m.MaxFiles
	incHome := m.IncludeHome
	m.mu.Unlock()
	if err != nil {
		return nil, 0, err
	}
	count := ScanLocalSessions(m.store, custom, maxFiles, incHome)
	return added, count, nil
}

func (m *ScanManager) RemoveDir(raw string) (int, error) {
	target := expandPath(raw)
	m.mu.Lock()
	next := make([]string, 0, len(m.CustomDirs))
	for _, d := range m.CustomDirs {
		if d == raw || d == target || replaceHomePaths(d) == raw {
			continue
		}
		next = append(next, d)
	}
	m.CustomDirs = next
	err := m.saveLocked()
	custom := slices.Clone(m.CustomDirs)
	maxFiles := m.MaxFiles
	incHome := m.IncludeHome
	m.mu.Unlock()
	if err != nil {
		return 0, err
	}
	count := ScanLocalSessions(m.store, custom, maxFiles, incHome)
	return count, nil
}

func (m *ScanManager) SetIncludeHome(on bool) (int, error) {
	m.mu.Lock()
	m.IncludeHome = on
	err := m.saveLocked()
	custom := slices.Clone(m.CustomDirs)
	maxFiles := m.MaxFiles
	incHome := m.IncludeHome
	m.mu.Unlock()
	if err != nil {
		return 0, err
	}
	count := ScanLocalSessions(m.store, custom, maxFiles, incHome)
	return count, nil
}

// ScanLocalSessions discovers and parses local Claude Code, Codex, and custom session JSONL/JSON files.
func ScanLocalSessions(store *Store, extraDirs []string, maxFiles int, includeHome bool) int {
	if maxFiles <= 0 {
		maxFiles = 200
	}
	home, _ := os.UserHomeDir()
	var homeCandidates []sessionFileInfo
	var customCandidates []sessionFileInfo

	collectInto := func(dst *[]sessionFileInfo, root, kind string, allowJSON, isCustom bool) {
		if root == "" {
			return
		}
		root = expandPath(root)
		info, err := os.Stat(root)
		if err != nil {
			return
		}
		if !info.IsDir() {
			lp := strings.ToLower(root)
			if strings.HasSuffix(lp, ".jsonl") || (allowJSON && strings.HasSuffix(lp, ".json")) {
				*dst = append(*dst, sessionFileInfo{path: root, modTime: info.ModTime(), kind: kind, isCustom: isCustom})
			}
			return
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "subagents" || d.Name() == ".git" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			ln := strings.ToLower(d.Name())
			if strings.HasPrefix(ln, ".") {
				return nil
			}
			if !strings.HasSuffix(ln, ".jsonl") && !(allowJSON && strings.HasSuffix(ln, ".json")) {
				return nil
			}
			fi, err := d.Info()
			if err != nil || fi.Size() == 0 {
				return nil
			}
			*dst = append(*dst, sessionFileInfo{path: p, modTime: fi.ModTime(), kind: kind, isCustom: isCustom})
			return nil
		})
	}

	if includeHome && home != "" {
		presetDirs := []struct {
			dir  string
			kind string
		}{
			{filepath.Join(home, ".claude", "projects"), "claude"},
			{filepath.Join(home, ".codex", "sessions"), "codex"},
			{filepath.Join(home, ".joycode", "sessions"), "joycode"},
		}
		for _, pd := range presetDirs {
			var dirCands []sessionFileInfo
			collectInto(&dirCands, pd.dir, pd.kind, false, false)
			slices.SortFunc(dirCands, func(a, b sessionFileInfo) int {
				return b.modTime.Compare(a.modTime)
			})
			if len(dirCands) > maxFiles {
				dirCands = dirCands[:maxFiles]
			}
			homeCandidates = append(homeCandidates, dirCands...)
		}
	}
	for _, dir := range extraDirs {
		if dir != "" {
			collectInto(&customCandidates, dir, "auto", true, true)
		}
	}

	slices.SortFunc(homeCandidates, func(a, b sessionFileInfo) int {
		return b.modTime.Compare(a.modTime)
	})
	slices.SortFunc(customCandidates, func(a, b sessionFileInfo) int {
		return b.modTime.Compare(a.modTime)
	})

	seen := map[string]bool{}
	var candidates []sessionFileInfo
	for _, c := range customCandidates {
		if !seen[c.path] {
			seen[c.path] = true
			candidates = append(candidates, c)
		}
	}
	for _, c := range homeCandidates {
		if !seen[c.path] {
			seen[c.path] = true
			candidates = append(candidates, c)
		}
	}

	var all []*RouteRecord
	for _, c := range candidates {
		recs, err := parseSessionFile(c.path, c.kind, SourceLocal)
		if err != nil || len(recs) == 0 {
			continue
		}
		all = append(all, recs...)
	}
	store.ReplaceSource(SourceLocal, all)
	return len(all)
}

func parseSessionFile(path, kind, source string) ([]*RouteRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseSessionReader(f, path, kind, source)
}

func parseSessionReader(r io.Reader, path, kind, source string) ([]*RouteRecord, error) {
	data, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, err
	}
	return parseSessionBytes(data, path, kind, source), nil
}

func parseSessionBytes(data []byte, path, kind, source string) []*RouteRecord {
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		if recs := parseCustomJSONFile(data, path, source); len(recs) > 0 {
			return recs
		}
	}
	if kind == "" || kind == "auto" {
		kind = detectSessionKind(data, path)
	}
	switch kind {
	case "codex":
		return parseCodexSessionWithKind(data, path, source, "codex")
	case "joycode":
		return parseCodexSessionWithKind(data, path, source, "joycode")
	case "claude":
		return parseClaudeSession(data, path, source)
	case "route_jsonl":
		return parseExportedRouteJSONL(data, source)
	default:
		if recs := parseClaudeSession(data, path, source); len(recs) > 0 {
			return recs
		}
		if recs := parseCodexSessionWithKind(data, path, source, ""); len(recs) > 0 {
			return recs
		}
		if recs := parseExportedRouteJSONL(data, source); len(recs) > 0 {
			return recs
		}
		return parseCustomJSONFile(data, path, source)
	}
}

func parseCustomJSONFile(data []byte, path, source string) []*RouteRecord {
	trim := bytes.TrimSpace(data)
	if len(trim) == 0 || trim[0] != '{' || !json.Valid(trim) {
		return nil
	}
	var wrap struct {
		Request  json.RawMessage `json:"request"`
		Response json.RawMessage `json:"response"`
		Body     json.RawMessage `json:"body"`
	}
	reqBytes := trim
	var respBytes []byte
	if json.Unmarshal(trim, &wrap) == nil {
		if len(wrap.Request) > 0 && bytes.TrimSpace(wrap.Request)[0] == '{' {
			reqBytes = wrap.Request
			respBytes = wrap.Response
		} else if len(wrap.Body) > 0 && bytes.TrimSpace(wrap.Body)[0] == '{' {
			reqBytes = wrap.Body
			respBytes = wrap.Response
		}
	}
	proto, detectedModel := detectProtocolAndModel(reqBytes, path)
	p := promptOf(proto, reqBytes)
	if p == nil {
		return nil
	}
	win := modelWindow(detectedModel)
	p.Window = win
	var (
		freshIn      int
		outTok       int
		cr           int
		cw           int
		cacheUnknown = true
	)
	if len(respBytes) > 0 {
		freshIn, outTok, cr, cw, cacheUnknown = extractUsageFromResponse(proto, respBytes)
		if freshIn > 0 || cr > 0 || cw > 0 {
			p.calibrate(freshIn+cr+cw, win)
		}
	}
	ts := time.Now()
	if info, err := os.Stat(path); err == nil {
		ts = info.ModTime()
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	rec := &RouteRecord{
		Source:       source,
		Time:         ts,
		Done:         true,
		Agent:        "custom-json",
		Client:       "Local JSON File",
		Provider:     string(proto),
		Protocol:     string(proto),
		Model:        detectedModel,
		Path:         path,
		Session:      "json-" + base,
		SessionTitle: filepath.Base(path),
		Turn:         p.Turns,
		Input:        freshIn,
		Output:       outTok,
		CacheRead:    cr,
		CacheWrite:   cw,
		Usage: []RouteUsage{{
			In:           freshIn,
			Out:          outTok,
			CacheRead:    cr,
			CacheWrite:   cw,
			CacheUnknown: cacheUnknown,
		}},
		Prompt:   p,
		Status:   200,
		reqBody:  append([]byte(nil), reqBytes...),
		respBody: append([]byte(nil), respBytes...),
	}
	return []*RouteRecord{rec}
}

func detectSessionKind(data []byte, path string) string {
	lp := strings.ToLower(path)
	if strings.Contains(lp, ".joycode") || strings.Contains(lp, "joycode") {
		return "joycode"
	}
	if strings.Contains(lp, ".codex") {
		return "codex"
	}
	if strings.Contains(lp, ".claude") {
		return "claude"
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	sawPayload := false
	for i := 0; i < 20 && sc.Scan(); i++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		if payloadRaw, ok := obj["payload"]; ok {
			sawPayload = true
			var typ string
			_ = json.Unmarshal(obj["type"], &typ)
			if typ == "session_meta" {
				var meta struct {
					Originator string `json:"originator"`
				}
				if json.Unmarshal(payloadRaw, &meta) == nil {
					if strings.Contains(strings.ToLower(meta.Originator), "joycode") {
						return "joycode"
					}
					return "codex"
				}
			}
			continue
		}
		if _, ok := obj["sessionId"]; ok {
			return "claude"
		}
		if _, ok := obj["prompt"]; ok {
			if _, ok2 := obj["model"]; ok2 {
				return "route_jsonl"
			}
		}
	}
	if sawPayload || strings.Contains(lp, "rollout-") {
		return "codex"
	}
	return "auto"
}

func parseExportedRouteJSONL(data []byte, source string) []*RouteRecord {
	var out []*RouteRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec RouteRecord
		if json.Unmarshal(line, &rec) == nil && rec.Prompt != nil {
			rec.ID = 0
			rec.Source = source
			rec.Done = true
			if rec.Time.IsZero() {
				rec.Time = time.Now()
			}
			out = append(out, &rec)
		}
	}
	return out
}

func truncateTitle(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes-1]) + "…"
}

func padTextToTokens(prefix string, targetTokens int) string {
	if targetTokens <= 0 {
		return ""
	}
	return prefix
}

var synthToolNames = []string{"Read", "Edit", "Write", "Bash", "Grep", "Glob", "Agent", "WebFetch", "WebSearch", "TodoWrite"}

func mergeToolNames(extra []string) []string {
	seen := make(map[string]bool, len(extra)+len(synthToolNames))
	out := make([]string, 0, len(extra)+len(synthToolNames))
	for _, n := range extra {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range synthToolNames {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func buildSynthToolsForGap(gapTokens int, extraToolNames []string) []any {
	if gapTokens <= 0 {
		return nil
	}
	names := mergeToolNames(extraToolNames)
	tools := make([]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{
			"name":         name,
			"description":  fmt.Sprintf("Tool definition baseline reconstructed from session token usage for %s (exact schema is not stored in local session JSONL).", name),
			"input_schema": map[string]any{"type": "object"},
		})
	}
	return tools
}

// applyBaselineGap attributes unlogged static system/tool baseline tokens in O(1) without re-serializing JSON.
func applyBaselineGap(p *Prompt, totalIn, win int, hasSys, hasTools bool, extraToolNames []string) {
	if p == nil {
		return
	}
	gap := totalIn - p.Tokens
	if gap > 200 {
		var addSys, addTools int
		if !hasSys && !hasTools {
			addSys = gap * 35 / 100
			addTools = gap - addSys
		} else if !hasTools {
			addTools = gap
		} else if !hasSys {
			addSys = gap
		} else {
			// Both exist, proportionally attribute missing static overhead.
			addSys = gap * 35 / 100
			addTools = gap - addSys
		}

		mergePart := func(kind string, addTok int, items []PromptItem) {
			if addTok <= 0 {
				return
			}
			for i := range p.Parts {
				if p.Parts[i].Kind == kind {
					p.Parts[i].Tokens += addTok
					p.Parts[i].Items = append(p.Parts[i].Items, items...)
					return
				}
			}
			p.Parts = append(p.Parts, PromptPart{
				Kind:   kind,
				Tokens: addTok,
				Items:  items,
			})
		}

		if addSys > 0 {
			mergePart(PartSystem, addSys, []PromptItem{{Name: "prompt", Tokens: addSys}})
		}
		if addTools > 0 {
			names := mergeToolNames(extraToolNames)
			each := addTools / len(names)
			if each < 40 {
				each = 40
			}
			var items []PromptItem
			acc := 0
			for _, name := range names {
				if acc >= addTools {
					break
				}
				rem := addTools - acc
				t := each
				if t > rem {
					t = rem
				}
				tag := ""
				if strings.HasPrefix(name, "mcp__") {
					tag = "mcp"
				}
				items = append(items, PromptItem{Name: name, Tag: tag, Tokens: t})
				acc += t
			}
			mergePart(PartTools, addTools, items)
		}
		p.Tokens += addSys + addTools

		orderIdx := func(k string) int {
			if idx := slices.Index(partOrder, k); idx >= 0 {
				return idx
			}
			return len(partOrder)
		}
		slices.SortFunc(p.Parts, func(a, b PromptPart) int {
			return orderIdx(a.Kind) - orderIdx(b.Kind)
		})
	}
	p.calibrate(totalIn, win)
}

// --- Claude Code Session Parser ---

type claudeLine struct {
	Type        string    `json:"type"`
	SessionID   string    `json:"sessionId"`
	Cwd         string    `json:"cwd"`
	Timestamp   time.Time `json:"timestamp"`
	IsSidechain bool      `json:"isSidechain"`
	Message     struct {
		ID      string          `json:"id"`
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type claudeMsg struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

func normalizeContentBlocks(raw json.RawMessage) []json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			b, _ := json.Marshal(map[string]any{"type": "text", "text": s})
			return []json.RawMessage{b}
		}
		return nil
	}
	if raw[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			return arr
		}
	}
	return nil
}

func extractUserTitleFromBlocks(blocks []json.RawMessage) string {
	for _, blk := range blocks {
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(blk, &b) == nil && b.Type == "text" {
			if w := userWords(b.Text); w != "" {
				return truncateTitle(w, 56)
			}
		}
	}
	return ""
}

func decodeRawBlocks(blocks []json.RawMessage) []rawBlock {
	bs := make([]rawBlock, 0, len(blocks))
	for _, raw := range blocks {
		var x rawBlock
		if json.Unmarshal(raw, &x) == nil {
			bs = append(bs, x)
		}
	}
	return bs
}

func parseClaudeSession(data []byte, path, source string) []*RouteRecord {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var (
		sessionID     string
		cwd           string
		sessionTitle  string
		msgs          []claudeMsg
		out           []*RouteRecord
		turnNum       int
		b             = newPromptBuilder()
		usedTools     []string
		seenTools     = map[string]bool{}
		checkedMem    bool
		memBlocksText []string
	)

	recordUsedTools := func(bs []rawBlock) {
		for _, x := range bs {
			if (x.Type == "tool_use" || x.Type == "server_tool_use" || x.Type == "mcp_tool_use") && x.Name != "" {
				if !seenTools[x.Name] {
					seenTools[x.Name] = true
					usedTools = append(usedTools, x.Name)
				}
			}
		}
	}

	maybeLoadProjectMemory := func(dir string) {
		if checkedMem || dir == "" {
			return
		}
		checkedMem = true
		for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
			fp := filepath.Join(dir, name)
			st, err := os.Stat(fp)
			if err != nil || st.IsDir() || st.Size() == 0 || st.Size() > 256*1024 {
				continue
			}
			raw, err := os.ReadFile(fp)
			if err != nil || len(raw) == 0 {
				continue
			}
			txt := fmt.Sprintf("Contents of %s (project instructions):\n%s", fp, string(raw))
			memBlocksText = append(memBlocksText, txt)
			b.systemText(txt, "prompt")
		}
	}

	type pendingAsst struct {
		id     string
		model  string
		ts     time.Time
		blocks []json.RawMessage
		in     int
		out    int
		cr     int
		cw     int
	}
	var cur *pendingAsst

	buildClaudeSynth := func(prevMsgs []claudeMsg, model string, totalIn int, toolsUsed, mems []string) []byte {
		noBase := map[string]any{
			"model":    model,
			"messages": prevMsgs,
		}
		baseBytes, _ := json.Marshal(noBase)
		basePrompt := promptOf(ProtoAnthropic, baseBytes)
		estMsgs := 0
		if basePrompt != nil {
			estMsgs = basePrompt.Tokens
		}
		gap := totalIn - estMsgs
		var sysBlocks []any
		for _, m := range mems {
			sysBlocks = append(sysBlocks, map[string]any{
				"type": "text",
				"text": m,
			})
		}
		var toolDefs []any
		if gap > 200 {
			sysGap := gap * 35 / 100
			toolGap := gap - sysGap
			sysBlocks = append(sysBlocks, map[string]any{
				"type": "text",
				"text": padTextToTokens("Claude Code system prompt baseline (reconstructed from session token count; raw system prompt is not stored in local ~/.claude/projects/*.jsonl).", sysGap),
			})
			toolDefs = buildSynthToolsForGap(toolGap, toolsUsed)
		}
		fullObj := map[string]any{
			"model":    model,
			"messages": prevMsgs,
		}
		if len(sysBlocks) > 0 {
			fullObj["system"] = sysBlocks
		}
		if len(toolDefs) > 0 {
			fullObj["tools"] = toolDefs
		}
		reqBytes, _ := json.Marshal(fullObj)
		return reqBytes
	}

	flushPending := func() {
		if cur == nil {
			return
		}
		p := cur
		cur = nil

		totalIn := p.in + p.cr + p.cw
		if totalIn > 0 && len(msgs) > 0 && p.model != "" && p.model != "<synthetic>" {
			turnNum++
			snapMsgs := slices.Clone(msgs)
			snapReplyBlocks := slices.Clone(p.blocks)
			snapModel := p.model
			snapTotalIn := totalIn
			snapUsedTools := slices.Clone(usedTools)
			snapMems := slices.Clone(memBlocksText)

			prompt := b.prompt()
			applyBaselineGap(prompt, snapTotalIn, modelWindow(snapModel), false, false, snapUsedTools)

			sid := sessionID
			if sid == "" && path != "" {
				sid = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			title := sessionTitle
			if title == "" && cwd != "" {
				title = filepath.Base(cwd)
			}
			ts := p.ts
			if ts.IsZero() {
				ts = time.Now()
			}
			rec := &RouteRecord{
				Source:       source,
				Time:         ts,
				Done:         true,
				Agent:        "claude-code",
				Client:       "Claude Code Local Session",
				Provider:     "anthropic",
				Protocol:     string(ProtoAnthropic),
				Model:        snapModel,
				Path:         path,
				Session:      sid,
				SessionTitle: title,
				Turn:         turnNum,
				Input:        p.in,
				Output:       p.out,
				CacheRead:    p.cr,
				CacheWrite:   p.cw,
				Usage: []RouteUsage{{
					In:         p.in,
					Out:        p.out,
					CacheRead:  p.cr,
					CacheWrite: p.cw,
				}},
				Prompt: prompt,
				Status: 200,
			}
			rec.bodyLoader = func() ([]byte, []byte, error) {
				reqBytes := buildClaudeSynth(snapMsgs, snapModel, snapTotalIn, snapUsedTools, snapMems)
				respBytes, _ := json.Marshal(map[string]any{"content": snapReplyBlocks})
				return reqBytes, respBytes, nil
			}
			out = append(out, rec)
		}

		if len(p.blocks) > 0 {
			for _, x := range decodeRawBlocks(p.blocks) {
				b.anthropicBlock("assistant", x)
			}
			msgs = append(msgs, claudeMsg{Role: "assistant", Content: p.blocks})
		}
	}

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev claudeLine
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.IsSidechain {
			continue
		}
		if ev.SessionID != "" && sessionID == "" {
			sessionID = ev.SessionID
		}
		if ev.Cwd != "" && cwd == "" {
			cwd = ev.Cwd
			maybeLoadProjectMemory(cwd)
		}

		switch ev.Type {
		case "user":
			flushPending()
			blocks := normalizeContentBlocks(ev.Message.Content)
			if len(blocks) == 0 {
				continue
			}
			if sessionTitle == "" {
				if t := extractUserTitleFromBlocks(blocks); t != "" {
					sessionTitle = t
				}
			}
			bs := decodeRawBlocks(blocks)
			results := slices.ContainsFunc(bs, func(x rawBlock) bool { return x.Type == "tool_result" })
			if !results && slices.ContainsFunc(bs, func(x rawBlock) bool { return x.Type == "text" && userSays(x.Text) }) {
				b.newTurn()
			}
			for _, x := range bs {
				b.anthropicBlock("user", x)
			}
			msgs = append(msgs, claudeMsg{Role: "user", Content: blocks})

		case "assistant":
			blocks := normalizeContentBlocks(ev.Message.Content)
			mid := ev.Message.ID
			if cur != nil && (mid == "" || cur.id != mid) {
				flushPending()
			}
			if cur == nil {
				cur = &pendingAsst{id: mid, model: ev.Message.Model, ts: ev.Timestamp}
			}
			if ev.Message.Model != "" {
				cur.model = ev.Message.Model
			}
			if !ev.Timestamp.IsZero() {
				cur.ts = ev.Timestamp
			}
			cur.blocks = append(cur.blocks, blocks...)
			recordUsedTools(decodeRawBlocks(blocks))
			if u := ev.Message.Usage; u != nil {
				cur.in = u.InputTokens
				cur.out = u.OutputTokens
				cur.cr = u.CacheReadInputTokens
				cur.cw = u.CacheCreationInputTokens
			}
		}
	}
	flushPending()
	return out
}

// --- Codex / JoyCode Session Parser ---

func parseCodexSession(data []byte, path, source string) []*RouteRecord {
	return parseCodexSessionWithKind(data, path, source, "")
}

func parseCodexSessionWithKind(data []byte, path, source, defaultKind string) []*RouteRecord {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var (
		sessionID     string
		cwd           string
		model         string
		originator    string
		baseInst      string
		metaWindow    int
		sessionTitle  string
		checkedTitle  bool
		toolsList     []json.RawMessage
		usedTools     []string
		seenTools     = map[string]bool{}
		inputItems    []json.RawMessage
		replyItems    []json.RawMessage
		out           []*RouteRecord
		lastTotalSeen int
		turnNum       int
		b             = newPromptBuilder()
	)

	recordUsedTool := func(name string) {
		if name != "" && !seenTools[name] {
			seenTools[name] = true
			usedTools = append(usedTools, name)
		}
	}

	buildCodexSynth := func(inst string, tools []json.RawMessage, items []json.RawMessage, mdl string, totalIn int, toolsUsed []string) []byte {
		noGap := map[string]any{
			"model":        mdl,
			"instructions": inst,
			"tools":        tools,
			"input":        items,
		}
		baseBytes, _ := json.Marshal(noGap)
		p0 := promptOf(ProtoResponses, baseBytes)
		est := 0
		if p0 != nil {
			est = p0.Tokens
		}
		finalTools := slices.Clone(tools)
		finalInst := inst
		gap := totalIn - est
		if gap > 200 {
			existingTools := map[string]bool{}
			for _, rawTool := range finalTools {
				var d struct {
					Name     string `json:"name"`
					Function *struct {
						Name string `json:"name"`
					} `json:"function"`
				}
				if json.Unmarshal(rawTool, &d) == nil {
					if d.Name != "" {
						existingTools[d.Name] = true
					} else if d.Function != nil && d.Function.Name != "" {
						existingTools[d.Function.Name] = true
					}
				}
			}
			tGap := gap * 65 / 100
			if len(finalTools) == 0 {
				finalInst = strings.TrimSpace(finalInst + "\n\n" + padTextToTokens("Codex harness instructions baseline (reconstructed from token_count).", gap-tGap))
			} else if finalInst == "" {
				finalInst = padTextToTokens("Codex base instructions baseline (reconstructed from token_count).", gap-tGap)
			}
			for _, td := range buildSynthToolsForGap(tGap, toolsUsed) {
				if m, ok := td.(map[string]any); ok {
					if name, _ := m["name"].(string); existingTools[name] {
						continue
					}
				}
				if raw, err := json.Marshal(td); err == nil {
					finalTools = append(finalTools, raw)
				}
			}
		}
		fullObj := map[string]any{
			"model":        mdl,
			"instructions": finalInst,
			"tools":        finalTools,
			"input":        items,
		}
		reqBytes, _ := json.Marshal(fullObj)
		return reqBytes
	}

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var env struct {
			Type      string          `json:"type"`
			Timestamp time.Time       `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &env) != nil {
			continue
		}

		switch env.Type {
		case "session_meta":
			var meta struct {
				SessionID        string            `json:"session_id"`
				ID               string            `json:"id"`
				Cwd              string            `json:"cwd"`
				Originator       string            `json:"originator"`
				BaseInstructions any               `json:"base_instructions"`
				DynamicTools     []json.RawMessage `json:"dynamic_tools"`
				ContextWindow    any               `json:"context_window"`
			}
			if json.Unmarshal(env.Payload, &meta) == nil {
				if meta.SessionID != "" {
					sessionID = meta.SessionID
				} else if meta.ID != "" {
					sessionID = meta.ID
				}
				if meta.Cwd != "" {
					cwd = meta.Cwd
				}
				if meta.Originator != "" {
					originator = meta.Originator
				}
				if cw, ok := meta.ContextWindow.(float64); ok && int(cw) > 0 {
					metaWindow = int(cw)
				}
				for _, dt := range meta.DynamicTools {
					if len(bytes.TrimSpace(dt)) > 0 {
						b.responsesTool(dt)
						toolsList = append(toolsList, dt)
					}
				}
				switch v := meta.BaseInstructions.(type) {
				case string:
					baseInst = v
				case map[string]any:
					if txt, ok := v["text"].(string); ok {
						baseInst = txt
					}
				}
				if baseInst != "" {
					b.systemText(baseInst, "prompt")
				}
			}

		case "turn_context":
			var tc struct {
				Model string `json:"model"`
				Cwd   string `json:"cwd"`
			}
			if json.Unmarshal(env.Payload, &tc) == nil {
				if tc.Model != "" {
					model = tc.Model
				}
				if tc.Cwd != "" && cwd == "" {
					cwd = tc.Cwd
				}
			}

		case "response_item":
			var item struct {
				Type    string            `json:"type"`
				Role    string            `json:"role"`
				Name    string            `json:"name"`
				Content json.RawMessage   `json:"content"`
				Tools   []json.RawMessage `json:"tools"`
			}
			if json.Unmarshal(env.Payload, &item) != nil {
				continue
			}
			b.responsesItem(env.Payload)
			if item.Type == "additional_tools" {
				toolsList = append(toolsList, item.Tools...)
				continue
			}
			if item.Type == "message" && item.Role == "user" && sessionTitle == "" {
				for _, rp := range responsesContent(item.Content) {
					if w := userWords(rp.text); w != "" {
						sessionTitle = truncateTitle(w, 56)
						break
					}
				}
			}
			if item.Type == "function_call" || item.Type == "custom_tool_call" || item.Type == "local_shell_call" || item.Type == "tool_search_call" {
				recordUsedTool(item.Name)
			}
			if item.Type == "message" && item.Role == "assistant" {
				replyItems = append(replyItems, env.Payload)
			} else if item.Type == "reasoning" || item.Type == "function_call" || item.Type == "custom_tool_call" || item.Type == "local_shell_call" {
				replyItems = append(replyItems, env.Payload)
			}
			inputItems = append(inputItems, env.Payload)

		case "event_msg":
			var ev struct {
				Type string `json:"type"`
				Info *struct {
					ModelContextWindow int `json:"model_context_window"`
					TotalTokenUsage    *struct {
						TotalTokens int `json:"total_tokens"`
					} `json:"total_token_usage"`
					LastTokenUsage *struct {
						InputTokens           int `json:"input_tokens"`
						CachedInputTokens     int `json:"cached_input_tokens"`
						CacheWriteInputTokens int `json:"cache_write_input_tokens"`
						OutputTokens          int `json:"output_tokens"`
					} `json:"last_token_usage"`
				} `json:"info"`
			}
			if json.Unmarshal(env.Payload, &ev) != nil || ev.Type != "token_count" || ev.Info == nil || ev.Info.LastTokenUsage == nil {
				continue
			}
			if ev.Info.TotalTokenUsage != nil {
				if ev.Info.TotalTokenUsage.TotalTokens == lastTotalSeen {
					continue
				}
				lastTotalSeen = ev.Info.TotalTokenUsage.TotalTokens
			}
			lu := ev.Info.LastTokenUsage
			if lu.InputTokens <= 0 {
				continue
			}
			turnNum++
			win := ev.Info.ModelContextWindow
			if win <= 0 {
				win = metaWindow
			}
			mdl := model
			if mdl == "" {
				mdl = "gpt-5-codex"
			}
			if win <= 0 {
				win = modelWindow(mdl)
			}

			snapInst := baseInst
			snapTools := slices.Clone(toolsList)
			snapUsedTools := slices.Clone(usedTools)
			snapItems := slices.Clone(inputItems)
			snapReply := slices.Clone(replyItems)
			replyItems = nil

			prompt := b.prompt()
			applyBaselineGap(prompt, lu.InputTokens, win, snapInst != "", len(snapTools) > 0, snapUsedTools)

			fresh := lu.InputTokens - lu.CachedInputTokens - lu.CacheWriteInputTokens
			if fresh < 0 {
				fresh = 0
			}
			sid := sessionID
			if sid == "" && path != "" {
				sid = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			if sessionTitle == "" && !checkedTitle && path != "" {
				checkedTitle = true
				titlePath := strings.TrimSuffix(path, filepath.Ext(path)) + ".title"
				if rawTitle, err := os.ReadFile(titlePath); err == nil {
					if t := truncateTitle(string(rawTitle), 56); t != "" {
						sessionTitle = t
					}
				}
			}
			title := sessionTitle
			if title == "" && cwd != "" {
				title = filepath.Base(cwd)
			}
			ts := env.Timestamp
			if ts.IsZero() {
				ts = time.Now()
			}
			isJoycode := defaultKind == "joycode" ||
				strings.Contains(strings.ToLower(path), ".joycode") ||
				strings.Contains(strings.ToLower(originator), "joycode")
			agentID := "codex"
			clientName := "Codex Local Session"
			if isJoycode {
				agentID = "joycode"
				clientName = "JoyCode Local Session"
			}
			rec := &RouteRecord{
				Source:       source,
				Time:         ts,
				Done:         true,
				Agent:        agentID,
				Client:       clientName,
				Provider:     "openai",
				Protocol:     string(ProtoResponses),
				Model:        mdl,
				Path:         path,
				Session:      sid,
				SessionTitle: title,
				Turn:         turnNum,
				Input:        fresh,
				Output:       lu.OutputTokens,
				CacheRead:    lu.CachedInputTokens,
				CacheWrite:   lu.CacheWriteInputTokens,
				Usage: []RouteUsage{{
					In:         fresh,
					Out:        lu.OutputTokens,
					CacheRead:  lu.CachedInputTokens,
					CacheWrite: lu.CacheWriteInputTokens,
				}},
				Prompt: prompt,
				Status: 200,
			}
			snapTotalIn := lu.InputTokens
			rec.bodyLoader = func() ([]byte, []byte, error) {
				reqBytes := buildCodexSynth(snapInst, snapTools, snapItems, mdl, snapTotalIn, snapUsedTools)
				respBytes, _ := json.Marshal(map[string]any{"output": snapReply})
				return reqBytes, respBytes, nil
			}
			out = append(out, rec)
		}
	}
	return out
}

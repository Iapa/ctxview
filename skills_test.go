package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeSkillFiles(t *testing.T, dir, content string, extra map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	for rel, body := range extra {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir extra %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write extra %s: %v", p, err)
		}
	}
}

func doSkillsReq(t *testing.T, handler http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode request body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func decodeMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode json response (%d %s): %v", rr.Code, rr.Body.String(), err)
	}
	return out
}

func TestBuiltinAdaptersInventoryAndConfigFallback(t *testing.T) {
	adapters := DefaultToolAdapters()
	if len(adapters) != 56 {
		t.Fatalf("expected 56 built-in adapters, got %d", len(adapters))
	}
	codingCount, lobsterCount := 0, 0
	seen := map[string]bool{}
	var hermes *ToolAdapter
	var opencode *ToolAdapter
	for i := range adapters {
		a := adapters[i]
		if seen[a.Key] {
			t.Fatalf("duplicate adapter key: %s", a.Key)
		}
		seen[a.Key] = true
		switch a.Category {
		case CategoryCoding:
			codingCount++
		case CategoryLobster:
			lobsterCount++
		default:
			t.Fatalf("unexpected category %q on %s", a.Category, a.Key)
		}
		if a.Key == "hermes" {
			cp := a
			hermes = &cp
		}
		if a.Key == "opencode" {
			cp := a
			opencode = &cp
		}
	}
	if codingCount != 49 || lobsterCount != 7 {
		t.Fatalf("unexpected category split: coding=%d lobster=%d", codingCount, lobsterCount)
	}
	if hermes == nil || !hermes.RecursiveScan {
		t.Fatalf("expected hermes adapter with RecursiveScan=true")
	}
	if opencode == nil {
		t.Fatalf("expected opencode adapter")
	}

	home := t.TempDir()
	cfgDir := filepath.Join(home, "Library", "Application Support")
	xdgOpenCode := filepath.Join(cfgDir, "opencode", "skills")
	if err := os.MkdirAll(xdgOpenCode, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := opencode.SkillsDir(home, cfgDir); got != xdgOpenCode {
		t.Fatalf("expected .config/ fallback %s, got %s", xdgOpenCode, got)
	}
	if !opencode.IsInstalled(home, cfgDir) {
		t.Fatalf("expected opencode IsInstalled via configDir fallback")
	}
}

func TestScannerFrontmatterHashSymlinkAndRecursion(t *testing.T) {
	raw := "---\nname: \"quoted-skill\"\ndescription: |\n  Line one\n  Line two\n---\n# Hello\n"
	name, desc := parseFrontmatter(raw)
	if name != "quoted-skill" {
		t.Fatalf("unexpected frontmatter name: %q", name)
	}
	if !strings.Contains(desc, "Line one") || !strings.Contains(desc, "Line two") {
		t.Fatalf("expected multiline description, got %q", desc)
	}

	root := t.TempDir()
	realSkill := filepath.Join(root, "real-skill")
	writeSkillFiles(t, realSkill, "---\nname: real-skill\ndescription: real desc\n---\nBody\n", map[string]string{
		"scripts/run.sh":   "#!/bin/sh\necho hi\n",
		".DS_Store":        "ignored",
		".git/HEAD":        "ignored",
		"__pycache__/a.py": "ignored",
		"tmp.pyc":          "ignored",
	})

	h1 := hashDirectory(realSkill)
	if h1 == "" {
		t.Fatal("expected non-empty hashDirectory result")
	}
	// Ignored files shouldn't alter hash
	if err := os.WriteFile(filepath.Join(realSkill, ".DS_Store"), []byte("changed-ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h2 := hashDirectory(realSkill); h2 != h1 {
		t.Fatalf("ignored file changed directory hash: %s != %s", h2, h1)
	}

	// Symlinked skill directory should produce identical content hash
	symSkill := filepath.Join(root, "sym-skill")
	if err := os.Symlink(realSkill, symSkill); err != nil {
		t.Fatal(err)
	}
	if hSym := hashDirectory(symSkill); hSym != h1 {
		t.Fatalf("symlinked skill hash mismatch: %s != %s", hSym, h1)
	}

	// Executable permission bit change should change hash on Unix
	if runtime.GOOS != "windows" {
		sh := filepath.Join(realSkill, "scripts", "run.sh")
		if err := os.Chmod(sh, 0o755); err != nil {
			t.Fatal(err)
		}
		if hExec := hashDirectory(realSkill); hExec == h1 {
			t.Fatalf("expected chmod +x to change directory hash")
		}
	}

	// Recursive scanning with cycle & skipped hidden/nested-skills guards
	recRoot := filepath.Join(root, "hermes-skills")
	writeSkillFiles(t, filepath.Join(recRoot, "group", "alpha"), "---\nname: alpha\n---\nAlpha\n", nil)
	writeSkillFiles(t, filepath.Join(recRoot, ".hidden", "secret"), "---\nname: secret\n---\nSecret\n", nil)
	writeSkillFiles(t, filepath.Join(recRoot, "bundle", "skills", "inner"), "---\nname: inner\n---\nInner\n", nil)
	if err := os.Symlink(recRoot, filepath.Join(recRoot, "group", "loop")); err != nil {
		t.Fatal(err)
	}

	found := readAgentLocalSkills(recRoot, "hermes", "Hermes", true)
	if len(found) != 1 || found[0].RelativePath != "group/alpha" {
		t.Fatalf("expected only [group/alpha], got %+v", found)
	}
}

func TestSyncOwnershipNoClobberAndSharedDirSurvivor(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()
	mgr := NewSkillsManager(dataDir, "")
	mgr.homeDirFn = func() (string, error) { return home, nil }
	mgr.configDirFn = func() (string, error) { return filepath.Join(home, ".config"), nil }

	// Seed Claude Code and Cursor, both installed; cursor has an unmanaged conflict dir
	claudeSkills := filepath.Join(home, ".claude", "skills")
	cursorSkills := filepath.Join(home, ".cursor", "skills")
	codexSkills := filepath.Join(home, ".codex", "skills")
	sharedPath := filepath.Join(home, ".shared-agent-skills")

	writeSkillFiles(t, filepath.Join(claudeSkills, "audit"), "---\nname: audit\ndescription: v1\n---\nV1\n", nil)
	writeSkillFiles(t, filepath.Join(cursorSkills, "audit"), "---\nname: audit\ndescription: foreign\n---\nForeign\n", nil)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}

	imported, err := mgr.ImportAgentLocalSkillToCenter("claude_code", "audit")
	if err != nil {
		t.Fatalf("import audit: %v", err)
	}

	// Single-pair sync into cursor must refuse due to foreign dir (PolicyNoClobber)
	_, err = mgr.SyncSkills([]string{imported.ID}, []string{"cursor"})
	if err == nil {
		t.Fatal("expected TARGET_CONFLICT syncing over unmanaged cursor/audit")
	}
	var apiErr *SkillsAPIError
	if ok := err != nil; ok {
		apiErr, _ = err.(*SkillsAPIError)
	}
	if apiErr == nil || apiErr.Status != http.StatusConflict || apiErr.Code != "TARGET_CONFLICT" {
		t.Fatalf("expected 409 TARGET_CONFLICT, got %v", err)
	}

	// Sync into codex succeeds
	res, err := mgr.SyncSkills([]string{imported.ID}, []string{"codex"})
	if err != nil || !res.OK || res.Applied != 1 {
		t.Fatalf("expected codex sync success, got res=%+v err=%v", res, err)
	}
	if _, err := os.Lstat(filepath.Join(codexSkills, "audit")); err != nil {
		t.Fatalf("expected deployed codex skill: %v", err)
	}

	// Point two custom tools at the exact same directory to verify shared-path survivor check
	if err := mgr.UpdateSettings(SkillsSettingsReq{Action: "add_custom_tool", Key: "custom-a", DisplayName: "Custom A", SkillsDir: sharedPath}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateSettings(SkillsSettingsReq{Action: "add_custom_tool", Key: "custom-b", DisplayName: "Custom B", SkillsDir: sharedPath}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SyncSkills([]string{imported.ID}, []string{"custom-a", "custom-b"}); err != nil {
		t.Fatal(err)
	}
	sharedTarget := filepath.Join(sharedPath, "audit")
	if _, err := os.Lstat(sharedTarget); err != nil {
		t.Fatalf("expected shared target present: %v", err)
	}
	// Unsync only custom-a: shared filesystem entry must survive because custom-b still references it!
	if _, err := mgr.UnsyncSkills([]string{imported.ID}, []string{"custom-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sharedTarget); err != nil {
		t.Fatalf("expected shared target to survive while custom-b remains synced: %v", err)
	}
	// Now unsync custom-b: filesystem target is cleaned up
	if _, err := mgr.UnsyncSkills([]string{imported.ID}, []string{"custom-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sharedTarget); !os.IsNotExist(err) {
		t.Fatalf("expected shared target removed after last owner unsynced, got err=%v", err)
	}
}

func TestClassifySyncStatusAllFiveStatesAndMatch(t *testing.T) {
	dir := t.TempDir()
	centerCopy := filepath.Join(dir, "center", "alpha")
	localCopy := filepath.Join(dir, "local", "alpha")
	writeSkillFiles(t, centerCopy, "---\nname: alpha\n---\nOne\n", nil)
	writeSkillFiles(t, localCopy, "---\nname: alpha\n---\nOne\n", nil)

	cHash := hashDirectory(centerCopy)
	lHash := hashDirectory(localCopy)
	now := time.Now().UnixMilli()

	center := &CenterSkill{
		ID:          "s1",
		Name:        "alpha",
		CentralPath: centerCopy,
		ContentHash: cHash,
		UpdatedAt:   now,
	}
	sk := &AgentSkill{
		Name:           "alpha",
		RelativePath:   "alpha",
		Path:           localCopy,
		ContentHash:    lHash,
		LastModifiedMs: now,
	}

	if got := classifySyncStatus(sk, nil); got != "project_only" {
		t.Fatalf("expected project_only, got %s", got)
	}
	if got := classifySyncStatus(sk, center); got != "in_sync" {
		t.Fatalf("expected in_sync, got %s", got)
	}

	// Simulate diverged content with center newer vs project newer vs closely timed diverged
	sk.ContentHash = "different-hash"
	sk.LastModifiedMs = now - 60000
	center.UpdatedAt = now
	if got := classifySyncStatus(sk, center); got != "center_newer" {
		t.Fatalf("expected center_newer, got %s", got)
	}

	sk.LastModifiedMs = now + 60000
	if got := classifySyncStatus(sk, center); got != "project_newer" {
		t.Fatalf("expected project_newer, got %s", got)
	}

	// Tie timestamp -> diverged
	sk.LastModifiedMs = now
	center.UpdatedAt = now
	if got := classifySyncStatus(sk, center); got != "diverged" {
		t.Fatalf("expected diverged on equal mtime, got %s", got)
	}

	// Unrelated skill sharing only display name must NOT false-match without path or hash evidence
	unrelated := &AgentSkill{
		Name:         "alpha",
		RelativePath: "alpha",
		Path:         filepath.Join(dir, "somewhere-else", "alpha"),
		ContentHash:  "brand-new-hash",
	}
	if m := findVerifiedCenterMatch(unrelated, []CenterSkill{*center}, nil); m != nil {
		t.Fatalf("expected no match for unrelated same-name skill, got %+v", m)
	}
}

func TestSkillsHTTPAPIWorkflowAndRedaction(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()

	// Seed agent directories under isolated HOME
	claudeSkills := filepath.Join(home, ".claude", "skills")
	cursorSkills := filepath.Join(home, ".cursor", "skills")
	hermesSkills := filepath.Join(home, ".hermes", "skills")
	openclawSkills := filepath.Join(home, ".openclaw", "skills")
	for _, d := range []string{claudeSkills, cursorSkills, hermesSkills, openclawSkills} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Seed local skills + secret token inside SKILL.md to test doc redaction
	writeSkillFiles(t, filepath.Join(claudeSkills, "sec-audit"),
		"---\nname: sec-audit\ndescription: Security audit helper\n---\n# Security Audit\n\nToken sk-proj-secret1234567890abcdef and path "+filepath.Join(home, "work")+"\n",
		nil,
	)
	writeSkillFiles(t, filepath.Join(hermesSkills, "category", "nested-skill"),
		"---\nname: nested-skill\ndescription: Deep hermes skill\n---\n# Nested\n",
		nil,
	)
	writeSkillFiles(t, filepath.Join(claudeSkills, "throwaway"),
		"---\nname: throwaway\n---\nTemp\n",
		nil,
	)
	// Foreign blocking skill in cursor for 409 test
	writeSkillFiles(t, filepath.Join(cursorSkills, "sec-audit"),
		"---\nname: sec-audit\n---\nForeign\n",
		nil,
	)

	skillsMgr := NewSkillsManager(dataDir, "")
	skillsMgr.homeDirFn = func() (string, error) { return home, nil }
	skillsMgr.configDirFn = func() (string, error) { return filepath.Join(home, ".config"), nil }

	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(ServerConfig{
		Store:       store,
		SkillsMgr:   skillsMgr,
		IncludeHome: false,
	})

	// 1. Static embedded skills web assets
	for _, path := range []string{"/skills.js", "/skills.css"} {
		rr := doSkillsReq(t, handler, "GET", path, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d", path, rr.Code)
		}
	}

	// 2. Agents overview redaction default vs ?redact=0
	rrRed := doSkillsReq(t, handler, "GET", "/api/skills/agents", nil)
	if rrRed.Code != http.StatusOK {
		t.Fatalf("GET /api/skills/agents failed: %d %s", rrRed.Code, rrRed.Body.String())
	}
	if strings.Contains(rrRed.Body.String(), home) {
		t.Fatalf("expected default redacted response not to leak temp home %s", home)
	}
	rrRaw := doSkillsReq(t, handler, "GET", "/api/skills/agents?redact=0", nil)
	if !strings.Contains(rrRaw.Body.String(), home) {
		t.Fatalf("expected ?redact=0 response to contain raw path %s", home)
	}

	// 3. Get claude_code skills (initially project_only)
	rrList := doSkillsReq(t, handler, "GET", "/api/skills/agents/claude_code/skills", nil)
	if rrList.Code != http.StatusOK {
		t.Fatalf("GET claude_code skills failed: %d", rrList.Code)
	}
	listData := decodeMap(t, rrList)
	rawSkills := listData["skills"].([]any)
	if len(rawSkills) != 2 {
		t.Fatalf("expected 2 skills in claude_code, got %d", len(rawSkills))
	}

	// 4. Document redaction + path traversal guard
	rrDocRed := doSkillsReq(t, handler, "GET", "/api/skills/agents/claude_code/document?rel_path=sec-audit", nil)
	if rrDocRed.Code != http.StatusOK {
		t.Fatalf("GET document failed: %d %s", rrDocRed.Code, rrDocRed.Body.String())
	}
	docRed := decodeMap(t, rrDocRed)
	if strings.Contains(docRed["content"].(string), "sk-proj-secret1234567890abcdef") {
		t.Fatalf("expected redacted document to mask secret key")
	}
	rrDocRaw := doSkillsReq(t, handler, "GET", "/api/skills/agents/claude_code/document?rel_path=sec-audit&redact=0", nil)
	docRaw := decodeMap(t, rrDocRaw)
	if !strings.Contains(docRaw["content"].(string), "sk-proj-secret1234567890abcdef") {
		t.Fatalf("expected ?redact=0 document to preserve raw token")
	}
	rrTrav := doSkillsReq(t, handler, "GET", "/api/skills/agents/claude_code/document?rel_path=../../etc", nil)
	if rrTrav.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for path traversal, got %d", rrTrav.Code)
	}

	// 5. Import sec-audit into center + adopt
	rrImp := doSkillsReq(t, handler, "POST", "/api/skills/agents/claude_code/import", map[string]any{"rel_path": "sec-audit"})
	if rrImp.Code != http.StatusOK {
		t.Fatalf("import failed: %d %s", rrImp.Code, rrImp.Body.String())
	}
	impCenter := decodeMap(t, rrImp)
	skillID := impCenter["id"].(string)

	// Recursive Hermes skill import & doc fetch
	rrHermesImp := doSkillsReq(t, handler, "POST", "/api/skills/agents/hermes/import", map[string]any{"rel_path": "category/nested-skill"})
	if rrHermesImp.Code != http.StatusOK {
		t.Fatalf("hermes recursive import failed: %d %s", rrHermesImp.Code, rrHermesImp.Body.String())
	}
	rrHermesDoc := doSkillsReq(t, handler, "GET", "/api/skills/agents/hermes/document?rel_path=category/nested-skill", nil)
	if rrHermesDoc.Code != http.StatusOK {
		t.Fatalf("hermes recursive document failed: %d", rrHermesDoc.Code)
	}

	// 6. Update center copy, verify center_newer detection, pull back to in_sync
	centralDocPath := filepath.Join(dataDir, "skills", "sec-audit", "SKILL.md")
	if err := os.WriteFile(centralDocPath, []byte("---\nname: sec-audit\ndescription: v2\n---\n# V2 Updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make central skill UpdatedAt strictly newer so symlinked check or copied check triggers; also test pull endpoint
	skillsMgr.mu.Lock()
	for i := range skillsMgr.state.Skills {
		if skillsMgr.state.Skills[i].ID == skillID {
			skillsMgr.state.Skills[i].ContentHash = hashDirectory(filepath.Join(dataDir, "skills", "sec-audit"))
			skillsMgr.state.Skills[i].UpdatedAt = time.Now().Add(time.Minute).UnixMilli()
		}
	}
	skillsMgr.mu.Unlock()
	if rrPull := doSkillsReq(t, handler, "POST", "/api/skills/agents/claude_code/pull", map[string]any{"rel_path": "sec-audit"}); rrPull.Code != http.StatusOK {
		t.Fatalf("pull failed: %d %s", rrPull.Code, rrPull.Body.String())
	}

	// 7. Sync sec-audit: 409 conflict on cursor, 200 ok on openclaw, then unsync
	rrConflict := doSkillsReq(t, handler, "POST", "/api/skills/sync", map[string]any{
		"skill_ids": []string{skillID},
		"agents":    []string{"cursor"},
	})
	if rrConflict.Code != http.StatusConflict {
		t.Fatalf("expected 409 TARGET_CONFLICT, got %d %s", rrConflict.Code, rrConflict.Body.String())
	}
	if rrSyncOC := doSkillsReq(t, handler, "POST", "/api/skills/sync", map[string]any{
		"skill_ids": []string{skillID},
		"agents":    []string{"openclaw"},
	}); rrSyncOC.Code != http.StatusOK {
		t.Fatalf("sync openclaw failed: %d %s", rrSyncOC.Code, rrSyncOC.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(openclawSkills, "sec-audit")); err != nil {
		t.Fatalf("expected sec-audit deployed in openclaw: %v", err)
	}

	if rrUnsync := doSkillsReq(t, handler, "POST", "/api/skills/unsync", map[string]any{
		"skill_ids": []string{skillID},
		"agents":    []string{"openclaw"},
	}); rrUnsync.Code != http.StatusOK {
		t.Fatalf("unsync openclaw failed: %d %s", rrUnsync.Code, rrUnsync.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(openclawSkills, "sec-audit")); !os.IsNotExist(err) {
		t.Fatalf("expected openclaw/sec-audit removed after unsync")
	}

	// 8. Tags, Presets, Center import-path, Delete local, Settings
	if rrTags := doSkillsReq(t, handler, "POST", "/api/skills/center/"+skillID+"/tags", map[string]any{"tags": []string{"security", "audit", "security"}}); rrTags.Code != http.StatusOK {
		t.Fatalf("tags update failed: %d %s", rrTags.Code, rrTags.Body.String())
	}
	rrPreset := doSkillsReq(t, handler, "POST", "/api/skills/presets", map[string]any{
		"name":      "Sec Pack",
		"skill_ids": []string{skillID},
	})
	if rrPreset.Code != http.StatusOK {
		t.Fatalf("save preset failed: %d %s", rrPreset.Code, rrPreset.Body.String())
	}
	presetID := decodeMap(t, rrPreset)["id"].(string)

	rrPresetsList := doSkillsReq(t, handler, "GET", "/api/skills/presets", nil)
	if len(decodeMap(t, rrPresetsList)["presets"].([]any)) != 1 {
		t.Fatalf("expected 1 preset")
	}
	if rrPresetDel := doSkillsReq(t, handler, "DELETE", "/api/skills/presets/"+presetID, nil); rrPresetDel.Code != http.StatusOK {
		t.Fatalf("delete preset failed: %d", rrPresetDel.Code)
	}

	// Import arbitrary standalone folder via center/import-path
	standalone := filepath.Join(home, "external-skill")
	writeSkillFiles(t, standalone, "---\nname: external-tool\ndescription: standalone\n---\nBody\n", nil)
	rrExt := doSkillsReq(t, handler, "POST", "/api/skills/center/import-path", map[string]any{"path": standalone})
	if rrExt.Code != http.StatusOK {
		t.Fatalf("center/import-path failed: %d %s", rrExt.Code, rrExt.Body.String())
	}
	extID := decodeMap(t, rrExt)["id"].(string)

	// Delete unmanaged throwaway local skill, refuse deleting managed sec-audit local skill directly
	if rrDelManaged := doSkillsReq(t, handler, "POST", "/api/skills/agents/claude_code/delete-local", map[string]any{"rel_path": "sec-audit"}); rrDelManaged.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 deleting managed local skill directly, got %d", rrDelManaged.Code)
	}
	if rrDelLocal := doSkillsReq(t, handler, "POST", "/api/skills/agents/claude_code/delete-local", map[string]any{"rel_path": "throwaway"}); rrDelLocal.Code != http.StatusOK {
		t.Fatalf("delete-local failed: %d %s", rrDelLocal.Code, rrDelLocal.Body.String())
	}

	// Settings: switch sync mode to copy, add custom tool, override path, reset, remove custom
	for _, actionBody := range []map[string]any{
		{"action": "set_sync_mode", "sync_mode": "copy"},
		{"action": "add_custom_tool", "key": "my-custom", "display_name": "My Custom", "skills_dir": filepath.Join(home, ".my-custom", "skills"), "category": "coding"},
		{"action": "set_tool_path", "key": "cursor", "skills_dir": filepath.Join(home, ".cursor-alt", "skills")},
		{"action": "reset_tool_path", "key": "cursor"},
		{"action": "remove_custom_tool", "key": "my-custom"},
	} {
		if rrSet := doSkillsReq(t, handler, "POST", "/api/skills/settings", actionBody); rrSet.Code != http.StatusOK {
			t.Fatalf("settings action %v failed: %d %s", actionBody["action"], rrSet.Code, rrSet.Body.String())
		}
	}

	// Delete center skill + verify persistence reload
	if rrDelCenter := doSkillsReq(t, handler, "DELETE", "/api/skills/center/"+extID, nil); rrDelCenter.Code != http.StatusOK {
		t.Fatalf("delete center skill failed: %d %s", rrDelCenter.Code, rrDelCenter.Body.String())
	}

	reloaded := NewSkillsManager(dataDir, "")
	if reloaded.state.SyncMode != SyncModeCopy {
		t.Fatalf("expected persisted SyncMode=copy, got %s", reloaded.state.SyncMode)
	}
	if len(reloaded.ListCenterSkills(false)) != 2 {
		t.Fatalf("expected 2 remaining central skills after reload, got %d", len(reloaded.ListCenterSkills(false)))
	}
}

func TestAdapterProjectRelativeDirs(t *testing.T) {
	byKey := map[string]ToolAdapter{}
	for _, a := range DefaultToolAdapters() {
		byKey[a.Key] = a
	}

	expectedOverrides := map[string]string{
		"omp_agent":   ".omp/skills",
		"opencode":    ".opencode/skills",
		"antigravity": ".agents/skills",
		"kimi":        ".kimi-code/skills",
		"pi":          ".pi/skills",
		"trae_cn":     ".trae/skills",
		"gitlab_duo":  ".agents/skills",
	}
	for key, want := range expectedOverrides {
		a, ok := byKey[key]
		if !ok {
			t.Fatalf("missing adapter %s", key)
		}
		if got := a.ProjectSkillsRelDir(); got != want {
			t.Fatalf("%s.ProjectSkillsRelDir() = %q, want %q", key, got, want)
		}
	}
	if got := byKey["claude_code"].ProjectSkillsRelDir(); got != ".claude/skills" {
		t.Fatalf("expected claude_code fallback .claude/skills, got %q", got)
	}
}

func TestProjectScannerAndDirectoryScan(t *testing.T) {
	root := t.TempDir()
	projA := filepath.Join(root, "ws", "alpha-repo")
	projB := filepath.Join(root, "ws", "beta-repo")
	ignored := filepath.Join(root, "ws", "node_modules", "pkg")

	writeSkillFiles(t, filepath.Join(projA, ".claude", "skills", "active-skill"), "---\nname: active-skill\n---\nActive\n", nil)
	writeSkillFiles(t, filepath.Join(projA, ".claude", "skills-disabled", "paused", "deep"), "---\nname: deep-paused\n---\nPaused\n", nil)
	// Sub-project inside projA must not be emitted separately because scan stops on matched project
	writeSkillFiles(t, filepath.Join(projA, "nested-sub", ".cursor", "skills", "sub"), "---\nname: sub\n---\n", nil)
	writeSkillFiles(t, filepath.Join(projB, ".cursor", "skills", "lint"), "---\nname: lint\n---\n", nil)
	writeSkillFiles(t, filepath.Join(ignored, ".claude", "skills", "skip"), "---\nname: skip\n---\n", nil)

	mgr := NewSkillsManager(t.TempDir(), "")
	mgr.mu.Lock()
	configs := mgr.projectAgentConfigsLocked()
	mgr.mu.Unlock()

	rec := ProjectRecord{ID: "p1", Name: "alpha", Path: projA, WorkspaceType: "project"}
	skills := readWorkspaceSkills(rec, configs)
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills (1 enabled + 1 disabled) in alpha, got %+v", skills)
	}
	var sawActive, sawPaused bool
	for _, s := range skills {
		if s.RelativePath == "active-skill" && s.Enabled {
			sawActive = true
		}
		if s.RelativePath == "paused/deep" && !s.Enabled {
			sawPaused = true
		}
	}
	if !sawActive || !sawPaused {
		t.Fatalf("expected enabled active-skill and disabled paused/deep, got %+v", skills)
	}

	discovered := scanProjectsInDir(root, 4, configs)
	if len(discovered) != 2 {
		t.Fatalf("expected 2 scanned projects (skipping node_modules and nested-sub), got %v", discovered)
	}
}

func TestProjectWorkspaceHTTPWorkflow(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()

	// Detect claude_code, cursor, cline (shares .agents/skills), antigravity (shares .agents/skills)
	for _, rel := range []string{
		".claude/skills",
		".cursor/skills",
		".agents/skills",
		".gemini/antigravity",
	} {
		if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	scanRoot := filepath.Join(home, "repos")
	projOne := filepath.Join(scanRoot, "app-one")
	projTwo := filepath.Join(scanRoot, "app-two")
	if err := os.MkdirAll(projOne, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillFiles(t, filepath.Join(projOne, ".claude", "skills", "group", "deep-skill"),
		"---\nname: deep-skill\ndescription: Nested project skill\n---\n# Deep\nToken sk-proj-secret1234567890abcdef\n",
		nil,
	)
	writeSkillFiles(t, filepath.Join(projTwo, ".cursor", "skills", "other"), "---\nname: other\n---\nOther\n", nil)

	// Prepare foreign unmanaged blocking dir in .agents/skills of projOne for 409 conflict test later
	foreignBlockDir := filepath.Join(projOne, ".agents", "skills", "central-only")
	if err := os.MkdirAll(foreignBlockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreignBlockDir, "custom.txt"), []byte("unmanaged content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed a central skill for export testing
	centralSrc := filepath.Join(home, "central-src")
	writeSkillFiles(t, centralSrc, "---\nname: central-only\ndescription: From center\n---\n# Central\nVersion 1\n", nil)

	linkedDir := filepath.Join(home, "ext-linked-skills")
	writeSkillFiles(t, filepath.Join(linkedDir, "ext-skill"), "---\nname: ext-skill\n---\n# Linked\n", nil)

	skillsMgr := NewSkillsManager(dataDir, "")
	skillsMgr.homeDirFn = func() (string, error) { return home, nil }
	skillsMgr.configDirFn = func() (string, error) { return filepath.Join(home, ".config"), nil }
	skillsMgr.state.SyncMode = SyncModeCopy // copy mode so project_newer & pull force tests mutate real project files cleanly
	_ = skillsMgr.saveLocked()

	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(ServerConfig{
		Store:       store,
		SkillsMgr:   skillsMgr,
		IncludeHome: false,
	})

	// 1. Add project (with redaction default + ?redact=0) & verify .claude/skills-disabled initialized
	rrAdd := doSkillsReq(t, handler, "POST", "/api/skills/projects", map[string]any{"path": projOne})
	if rrAdd.Code != http.StatusOK {
		t.Fatalf("POST /api/skills/projects failed: %d %s", rrAdd.Code, rrAdd.Body.String())
	}
	if strings.Contains(rrAdd.Body.String(), home) {
		t.Fatalf("expected default redacted project response not to leak home dir %s", home)
	}
	projOneID := decodeMap(t, rrAdd)["id"].(string)
	if !isDirFollow(filepath.Join(projOne, ".claude", "skills-disabled")) {
		t.Fatalf("expected .claude/skills-disabled to be initialized")
	}

	// Duplicate registration via symlink must fail with 409 DUPLICATE_PROJECT
	symlinkProj := filepath.Join(home, "app-one-link")
	if err := os.Symlink(projOne, symlinkProj); err != nil {
		t.Fatal(err)
	}
	if rrDup := doSkillsReq(t, handler, "POST", "/api/skills/projects", map[string]any{"path": symlinkProj}); rrDup.Code != http.StatusConflict {
		t.Fatalf("expected 409 duplicate symlink project, got %d %s", rrDup.Code, rrDup.Body.String())
	}

	// 2. Scan directory should return projOne (already_added: true) & projTwo (already_added: false)
	rrScan := doSkillsReq(t, handler, "POST", "/api/skills/projects/scan", map[string]any{"root": scanRoot})
	if rrScan.Code != http.StatusOK {
		t.Fatalf("scan projects failed: %d %s", rrScan.Code, rrScan.Body.String())
	}
	scannedItems := decodeMap(t, rrScan)["projects"].([]any)
	if len(scannedItems) != 2 {
		t.Fatalf("expected 2 scanned items, got %d", len(scannedItems))
	}

	// 3. Linked workspace overlapping root must fail, valid linked workspace auto-creates -disabled
	if rrOverlap := doSkillsReq(t, handler, "POST", "/api/skills/projects/linked", map[string]any{
		"name":          "Bad",
		"path":          linkedDir,
		"disabled_path": linkedDir,
	}); rrOverlap.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 OVERLAPPING_ROOTS, got %d %s", rrOverlap.Code, rrOverlap.Body.String())
	}
	rrLinked := doSkillsReq(t, handler, "POST", "/api/skills/projects/linked", map[string]any{
		"name": "Team Shared",
		"path": linkedDir,
	})
	if rrLinked.Code != http.StatusOK {
		t.Fatalf("add linked workspace failed: %d %s", rrLinked.Code, rrLinked.Body.String())
	}
	linkedID := decodeMap(t, rrLinked)["id"].(string)
	if !isDirFollow(linkedDir + "-disabled") {
		t.Fatalf("expected companion %s-disabled to be created", linkedDir)
	}

	// 4. Reorder projects & list raw vs redacted
	if rrReorder := doSkillsReq(t, handler, "POST", "/api/skills/projects/reorder", map[string]any{"ids": []string{linkedID, projOneID}}); rrReorder.Code != http.StatusOK {
		t.Fatalf("reorder failed: %d", rrReorder.Code)
	}
	rrProjList := doSkillsReq(t, handler, "GET", "/api/skills/projects?redact=0", nil)
	projItems := decodeMap(t, rrProjList)["projects"].([]any)
	if projItems[0].(map[string]any)["id"].(string) != linkedID {
		t.Fatalf("expected reordered first project to be linkedID")
	}

	// 5. Targets deduplicate .agents/skills into one row
	rrTargets := doSkillsReq(t, handler, "GET", "/api/skills/projects/"+projOneID+"/targets", nil)
	if rrTargets.Code != http.StatusOK {
		t.Fatalf("targets failed: %d %s", rrTargets.Code, rrTargets.Body.String())
	}
	targets := decodeMap(t, rrTargets)["targets"].([]any)
	sharedAgentsRows := 0
	for _, item := range targets {
		m := item.(map[string]any)
		if m["relative_skills_dir"] == ".agents/skills" {
			sharedAgentsRows++
		}
	}
	if sharedAgentsRows != 1 {
		t.Fatalf("expected exactly 1 deduplicated target row for .agents/skills, got %d", sharedAgentsRows)
	}

	// 6. Document read with redaction + path traversal rejection
	rrDocRed := doSkillsReq(t, handler, "GET", "/api/skills/projects/"+projOneID+"/document?rel_path=group/deep-skill&agent=claude_code", nil)
	if rrDocRed.Code != http.StatusOK {
		t.Fatalf("project document read failed: %d %s", rrDocRed.Code, rrDocRed.Body.String())
	}
	if strings.Contains(decodeMap(t, rrDocRed)["content"].(string), "sk-proj-secret1234567890abcdef") {
		t.Fatalf("expected project doc redaction to mask API key")
	}
	rrDocRaw := doSkillsReq(t, handler, "GET", "/api/skills/projects/"+projOneID+"/document?rel_path=group/deep-skill&agent=claude_code&redact=0", nil)
	if !strings.Contains(decodeMap(t, rrDocRaw)["content"].(string), "sk-proj-secret1234567890abcdef") {
		t.Fatalf("expected ?redact=0 project doc to preserve raw token")
	}
	if rrTrav := doSkillsReq(t, handler, "GET", "/api/skills/projects/"+projOneID+"/document?rel_path=../../etc&agent=claude_code", nil); rrTrav.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for path traversal, got %d", rrTrav.Code)
	}

	// 7. Import project skill into center & verify central skill imported
	rrImp := doSkillsReq(t, handler, "POST", "/api/skills/projects/"+projOneID+"/import", map[string]any{
		"rel_path": "group/deep-skill",
		"agent":    "claude_code",
	})
	if rrImp.Code != http.StatusOK {
		t.Fatalf("project import failed: %d %s", rrImp.Code, rrImp.Body.String())
	}
	deepSkillID := decodeMap(t, rrImp)["id"].(string)

	// 8. Export central-only skill -> 409 TARGET_CONFLICT on antigravity (.agents/skills conflict), 200 OK on cursor & claude_code
	rrCent := doSkillsReq(t, handler, "POST", "/api/skills/center/import-path", map[string]any{"path": centralSrc})
	centralOnlyID := decodeMap(t, rrCent)["id"].(string)

	rrExpConflict := doSkillsReq(t, handler, "POST", "/api/skills/projects/"+projOneID+"/export", map[string]any{
		"skill_ids": []string{centralOnlyID},
		"agents":    []string{"antigravity"},
	})
	if rrExpConflict.Code != http.StatusConflict {
		t.Fatalf("expected 409 TARGET_CONFLICT exporting onto occupied .agents/skills, got %d %s", rrExpConflict.Code, rrExpConflict.Body.String())
	}

	rrExpOK := doSkillsReq(t, handler, "POST", "/api/skills/projects/"+projOneID+"/export", map[string]any{
		"skill_ids": []string{centralOnlyID},
		"agents":    []string{"cursor", "claude_code"},
	})
	if rrExpOK.Code != http.StatusOK || decodeMap(t, rrExpOK)["applied"].(float64) != 2 {
		t.Fatalf("expected export applied=2, got %d %s", rrExpOK.Code, rrExpOK.Body.String())
	}

	// 9. Toggle nested group/deep-skill disabled then enabled + verify empty parent dir cleanup
	if rrOff := doSkillsReq(t, handler, "POST", "/api/skills/projects/"+projOneID+"/toggle", map[string]any{
		"rel_path": "group/deep-skill",
		"agent":    "claude_code",
		"enabled":  false,
	}); rrOff.Code != http.StatusOK {
		t.Fatalf("toggle off failed: %d %s", rrOff.Code, rrOff.Body.String())
	}
	disabledDeep := filepath.Join(projOne, ".claude", "skills-disabled", "group", "deep-skill")
	if !isDirFollow(disabledDeep) {
		t.Fatalf("expected skill moved to %s", disabledDeep)
	}
	// Disabled document should still be readable
	if rrDisDoc := doSkillsReq(t, handler, "GET", "/api/skills/projects/"+projOneID+"/document?rel_path=group/deep-skill&agent=claude_code", nil); rrDisDoc.Code != http.StatusOK {
		t.Fatalf("expected disabled skill document readable, got %d", rrDisDoc.Code)
	}
	if rrOn := doSkillsReq(t, handler, "POST", "/api/skills/projects/"+projOneID+"/toggle", map[string]any{
		"rel_path": "group/deep-skill",
		"agent":    "claude_code",
		"enabled":  true,
	}); rrOn.Code != http.StatusOK {
		t.Fatalf("toggle on failed: %d %s", rrOn.Code, rrOn.Body.String())
	}
	if isDirFollow(filepath.Join(projOne, ".claude", "skills-disabled", "group")) {
		t.Fatalf("expected empty parent directory in skills-disabled to be cleaned up")
	}

	// 10. Modify cursor copy of central-only to make it project_newer: pull without force fails, force succeeds
	cursorCopyDoc := filepath.Join(projOne, ".cursor", "skills", "central-only", "SKILL.md")
	future := time.Now().Add(2 * time.Minute)
	if err := os.WriteFile(cursorCopyDoc, []byte("---\nname: central-only\n---\n# Edited locally\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(cursorCopyDoc, future, future)

	if rrPullNoForce := doSkillsReq(t, handler, "POST", "/api/skills/projects/"+projOneID+"/pull", map[string]any{
		"rel_path": "central-only",
		"agent":    "cursor",
		"force":    false,
	}); rrPullNoForce.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 LOCAL_NEWER without force, got %d %s", rrPullNoForce.Code, rrPullNoForce.Body.String())
	}
	if rrPullForce := doSkillsReq(t, handler, "POST", "/api/skills/projects/"+projOneID+"/pull", map[string]any{
		"rel_path": "central-only",
		"agent":    "cursor",
		"force":    true,
	}); rrPullForce.Code != http.StatusOK {
		t.Fatalf("expected force pull 200, got %d %s", rrPullForce.Code, rrPullForce.Body.String())
	}

	// 11. Delete project skill copy while preserving central skill + remove project while preserving disk files
	if rrDelSk := doSkillsReq(t, handler, "POST", "/api/skills/projects/"+projOneID+"/delete-skill", map[string]any{
		"rel_path": "central-only",
		"agent":    "cursor",
	}); rrDelSk.Code != http.StatusOK {
		t.Fatalf("delete-skill failed: %d %s", rrDelSk.Code, rrDelSk.Body.String())
	}
	if isDirFollow(filepath.Join(projOne, ".cursor", "skills", "central-only")) {
		t.Fatalf("expected cursor project skill copy deleted")
	}
	if _, err := skillsMgr.GetCenterSkillDocument(deepSkillID, false); err != nil {
		t.Fatalf("expected central deepSkill preserved: %v", err)
	}

	if rrDelProj := doSkillsReq(t, handler, "DELETE", "/api/skills/projects/"+projOneID, nil); rrDelProj.Code != http.StatusOK {
		t.Fatalf("delete project registration failed: %d %s", rrDelProj.Code, rrDelProj.Body.String())
	}
	if !isDirFollow(projOne) {
		t.Fatalf("expected disk folder %s preserved after RemoveProject", projOne)
	}

	reloadedProjMgr := NewSkillsManager(dataDir, "")
	if len(reloadedProjMgr.ListProjects(false)) != 1 || reloadedProjMgr.ListProjects(false)[0].ID != linkedID {
		t.Fatalf("expected 1 persisted linked workspace after reload, got %+v", reloadedProjMgr.ListProjects(false))
	}
}


package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func doJSONReq(t *testing.T, h http.Handler, method, path string, body any) (int, string, map[string]any) {
	t.Helper()
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s body: %v", path, err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw := rec.Body.String()
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, raw, parsed
}

func assertNotContains(t *testing.T, label, haystack string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if needle != "" && strings.Contains(haystack, needle) {
			t.Fatalf("%s leaked %q in payload:\n%s", label, needle, haystack)
		}
	}
}

func TestRedactionAPIDefaultAndToggle(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	projDir := filepath.Join(fakeHome, "mywork")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const (
		secretKey  = "sk-ant-api03-SECRET1234567890ABCDEF"
		claudeKey  = "sk-ant-api03-MEM9876543210ZYXWVUT"
		phone      = "13800138000"
		email      = "alice@acme-corp.io"
		fieldValue = "super-secret-field-value-987654"
	)
	secretPath := filepath.Join(projDir, "config.env")

	claudeMD := "# Project Notes\nOwner: " + email + "\nBackup phone: " + phone + "\nToken: " + claudeKey + "\n"
	if err := os.WriteFile(filepath.Join(projDir, "CLAUDE.md"), []byte(claudeMD), 0o644); err != nil {
		t.Fatal(err)
	}

	sessDir := t.TempDir()
	ts1 := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	ts2 := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339Nano)

	userEv := map[string]any{
		"type":      "user",
		"sessionId": "sess-claude-redact-1",
		"cwd":       projDir,
		"timestamp": ts1,
		"message": map[string]any{
			"role":    "user",
			"content": "Debug api_key=" + secretKey + " call " + phone + " at " + secretPath + " (" + email + ")",
		},
	}
	assistEv := map[string]any{
		"type":      "assistant",
		"sessionId": "sess-claude-redact-1",
		"cwd":       projDir,
		"timestamp": ts2,
		"message": map[string]any{
			"role":  "assistant",
			"model": "claude-sonnet-4-5",
			"usage": map[string]any{
				"input_tokens":               5000,
				"cache_read_input_tokens":    3000,
				"cache_creation_input_tokens": 200,
				"output_tokens":              120,
			},
			"content": []any{
				map[string]any{
					"type": "tool_use",
					"id":   "tu_1",
					"name": "Read",
					"input": map[string]any{
						"file_path": secretPath,
						"api_key":   fieldValue,
					},
				},
				map[string]any{
					"type": "text",
					"text": "Checked " + secretPath + " found " + secretKey + " phone " + phone + " email " + email,
				},
			},
		},
	}

	var buf bytes.Buffer
	for _, ev := range []any{userEv, assistEv} {
		line, _ := json.Marshal(ev)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(sessDir, "sess-claude-redact-1.jsonl"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	scanMgr := NewScanManager(store, dataDir, []string{sessDir}, false)
	if n := scanMgr.Scan(); n == 0 {
		t.Fatalf("expected scanned local records > 0, got %d", n)
	}

	h := NewHandler(ServerConfig{Store: store, ScanMgr: scanMgr, IncludeHome: false})

	code, ctxRaw, _ := doJSONReq(t, h, "GET", "/api/context?days=7&source=local", nil)
	if code != 200 {
		t.Fatalf("/api/context status=%d: %s", code, ctxRaw)
	}
	assertNotContains(t, "/api/context", ctxRaw, secretKey, claudeKey, phone, email, fakeHome)
	if !strings.Contains(ctxRaw, "~/mywork") {
		t.Fatalf("/api/context expected redacted home path ~/mywork in:\n%s", ctxRaw)
	}

	code, routesRaw, routesObj := doJSONReq(t, h, "GET", "/api/routes?source=local", nil)
	if code != 200 {
		t.Fatalf("/api/routes status=%d: %s", code, routesRaw)
	}
	assertNotContains(t, "/api/routes", routesRaw, secretKey, claudeKey, phone, email, fakeHome)

	routesList, _ := routesObj["routes"].([]any)
	if len(routesList) == 0 {
		t.Fatalf("expected at least 1 route in /api/routes")
	}
	firstRoute := routesList[0].(map[string]any)
	routeID := int64(firstRoute["id"].(float64))

	code, routeRaw, routeObj := doJSONReq(t, h, "GET", "/api/route?id="+itoa(routeID), nil)
	if code != 200 {
		t.Fatalf("/api/route status=%d: %s", code, routeRaw)
	}
	assertNotContains(t, "/api/route", routeRaw, secretKey, claudeKey, phone, email, fakeHome)

	// Toggle off via ?redact=0 must recover unredacted data without store corruption.
	code, unredRouteRaw, _ := doJSONReq(t, h, "GET", "/api/route?id="+itoa(routeID)+"&redact=0", nil)
	if code != 200 {
		t.Fatalf("/api/route?redact=0 status=%d", code)
	}
	if !strings.Contains(unredRouteRaw, secretKey) || !strings.Contains(unredRouteRaw, fakeHome) {
		t.Fatalf("/api/route?redact=0 expected original secret/path back:\n%s", unredRouteRaw)
	}

	code, sessRaw, _ := doJSONReq(t, h, "GET", "/api/context/session?agent=claude&id=sess-claude-redact-1&source=local", nil)
	if code != 200 {
		t.Fatalf("/api/context/session status=%d: %s", code, sessRaw)
	}
	assertNotContains(t, "/api/context/session", sessRaw, secretKey, claudeKey, phone, email, fakeHome)

	// Verify all parts via /api/context/text (redacted, aligned tokens, no placeholder spam).
	promptObj, _ := routeObj["prompt"].(map[string]any)
	partsSlice, _ := promptObj["parts"].([]any)
	for _, pRaw := range partsSlice {
		part := pRaw.(map[string]any)
		kind := part["kind"].(string)
		partTokens := int(part["tokens"].(float64))
		code, txtRaw, _ := doJSONReq(t, h, "GET", "/api/context/text?id="+itoa(routeID)+"&kind="+kind, nil)
		if code != 200 {
			t.Fatalf("/api/context/text kind=%s status=%d: %s", kind, code, txtRaw)
		}
		assertNotContains(t, "/api/context/text("+kind+")", txtRaw,
			secretKey, claudeKey, phone, email, fakeHome, fieldValue, "[session static context placeholder]")
		var pt PromptText
		if err := json.Unmarshal([]byte(txtRaw), &pt); err != nil {
			t.Fatalf("unmarshal PromptText kind=%s: %v", kind, err)
		}
		if partTokens > 0 && len(pt.Items) > 0 {
			sum := 0
			for idx, it := range pt.Items {
				sum += it.Tokens
				iCode, itemRaw, _ := doJSONReq(t, h, "GET", "/api/context/text?id="+itoa(routeID)+"&kind="+kind+"&item="+itoa(int64(idx)), nil)
				if iCode != 200 {
					t.Fatalf("/api/context/text kind=%s item=%d status=%d: %s", kind, idx, iCode, itemRaw)
				}
				assertNotContains(t, "/api/context/text("+kind+",item="+itoa(int64(idx))+")", itemRaw,
					secretKey, claudeKey, phone, email, fakeHome, fieldValue, "[session static context placeholder]")
			}
			if sum != partTokens {
				t.Fatalf("kind=%s item token sum=%d != calibrated part.Tokens=%d", kind, sum, partTokens)
			}
		}
	}

	// Verify redacted chat item=0 actually contains placeholders/tilde paths and not raw secrets,
	// while ?redact=0 exposes the original unredacted text.
	code, redChatItem, _ := doJSONReq(t, h, "GET", "/api/context/text?id="+itoa(routeID)+"&kind=chat&item=0", nil)
	if code != 200 {
		t.Fatalf("/api/context/text kind=chat&item=0 status=%d", code)
	}
	assertNotContains(t, "/api/context/text(chat,item=0)", redChatItem, secretKey, phone, email, fakeHome, fieldValue)
	if !strings.Contains(redChatItem, "~/mywork") || !strings.Contains(redChatItem, "API_KEY") {
		t.Fatalf("expected redacted chat item to contain ~/mywork and placeholder, got:\n%s", redChatItem)
	}

	code, unredChatRaw, _ := doJSONReq(t, h, "GET", "/api/context/text?id="+itoa(routeID)+"&kind=chat&item=0&redact=0", nil)
	if code != 200 {
		t.Fatalf("/api/context/text?redact=0 status=%d", code)
	}
	if !strings.Contains(unredChatRaw, secretKey) || !strings.Contains(unredChatRaw, phone) {
		t.Fatalf("/api/context/text?redact=0 expected original secrets back:\n%s", unredChatRaw)
	}

	// Verify /api/context/reply redacts secrets, PII, and home paths by default, and toggles off with ?redact=0.
	code, replyRaw, _ := doJSONReq(t, h, "GET", "/api/context/reply?id="+itoa(routeID), nil)
	if code != 200 {
		t.Fatalf("/api/context/reply status=%d: %s", code, replyRaw)
	}
	assertNotContains(t, "/api/context/reply", replyRaw, secretKey, phone, email, fakeHome)

	code, unredReplyRaw, _ := doJSONReq(t, h, "GET", "/api/context/reply?id="+itoa(routeID)+"&redact=0", nil)
	if code != 200 {
		t.Fatalf("/api/context/reply?redact=0 status=%d", code)
	}
	if !strings.Contains(unredReplyRaw, secretKey) {
		t.Fatalf("/api/context/reply?redact=0 expected original reply text:\n%s", unredReplyRaw)
	}
}

func TestScanDirsLifecycle(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	if err := os.MkdirAll(filepath.Join(fakeHome, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	joyDayDir := filepath.Join(fakeHome, ".joycode", "sessions", "2026", "11", "14")
	if err := os.MkdirAll(joyDayDir, 0o755); err != nil {
		t.Fatal(err)
	}

	joyTS := time.Now().UTC().Format(time.RFC3339Nano)
	joyBase := filepath.Join(joyDayDir, "rollout-2026-11-14T03-10-49-test-joycode")
	var joyBuf bytes.Buffer
	for _, ev := range []any{
		map[string]any{
			"type":      "session_meta",
			"timestamp": joyTS,
			"payload": map[string]any{
				"session_id":        "joy-sess-1",
				"cwd":               filepath.Join(fakeHome, "joyproj"),
				"originator":        "joycode-desktop",
				"model_provider":    "openai",
				"context_window":    map[string]any{"window_id": "win-1"},
				"base_instructions": map[string]any{"text": "JoyCode base prompt"},
				"dynamic_tools": []any{
					map[string]any{"type": "function", "name": "joycode_browser", "description": "Browse web", "inputSchema": map[string]any{"type": "object"}},
				},
			},
		},
		map[string]any{
			"type":      "turn_context",
			"timestamp": joyTS,
			"payload":   map[string]any{"model": "GPT-6.1 Sol"},
		},
		map[string]any{
			"type":      "response_item",
			"timestamp": joyTS,
			"payload": map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "<environment_context><cwd>~/joyproj</cwd></environment_context>"}},
			},
		},
		map[string]any{
			"type":      "response_item",
			"timestamp": joyTS,
			"payload":   map[string]any{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": `{"cmd":"pwd"}`},
		},
		map[string]any{
			"type":      "event_msg",
			"timestamp": joyTS,
			"payload": map[string]any{
				"type": "token_count",
				"info": map[string]any{
					"model_context_window": 950000,
					"total_token_usage":    map[string]any{"total_tokens": 12150},
					"last_token_usage": map[string]any{
						"input_tokens":            12000,
						"cached_input_tokens":     0,
						"cache_write_input_tokens": 9000,
						"output_tokens":           150,
					},
				},
			},
		},
	} {
		b, _ := json.Marshal(ev)
		joyBuf.Write(b)
		joyBuf.WriteByte('\n')
	}
	if err := os.WriteFile(joyBase+".jsonl", joyBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(joyBase+".title", []byte("JoyCode Custom Title\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	customDir := filepath.Join(fakeHome, "custom-logs")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1) A .jsonl Claude session file
	now := time.Now().UTC().Format(time.RFC3339Nano)
	jsonlLine1, _ := json.Marshal(map[string]any{
		"type":      "user",
		"sessionId": "custom-jsonl-1",
		"timestamp": now,
		"message":   map[string]any{"role": "user", "content": "Hello jsonl"},
	})
	jsonlLine2, _ := json.Marshal(map[string]any{
		"type":      "assistant",
		"sessionId": "custom-jsonl-1",
		"timestamp": now,
		"message": map[string]any{
			"role":  "assistant",
			"model": "claude-sonnet-4-5",
			"usage": map[string]any{"input_tokens": 1200, "output_tokens": 80},
			"content": []any{
				map[string]any{"type": "text", "text": "Hi jsonl"},
			},
		},
	})
	if err := os.WriteFile(filepath.Join(customDir, "sess-1.jsonl"), append(append(jsonlLine1, '\n'), append(jsonlLine2, '\n')...), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2) A .json single-request session file
	jsonPayload, _ := json.Marshal(map[string]any{
		"model":  "claude-sonnet-4-5",
		"system": "Be brief",
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello standalone json"},
		},
	})
	if err := os.WriteFile(filepath.Join(customDir, "sess-2.json"), jsonPayload, 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	scanMgr := NewScanManager(store, dataDir, nil, false)
	h := NewHandler(ServerConfig{Store: store, ScanMgr: scanMgr, IncludeHome: false})

	// Initial GET /api/scan-dirs: include_home=false, 3 preset default dirs (claude, codex, joycode), redacted paths hide fakeHome.
	code, raw, obj := doJSONReq(t, h, "GET", "/api/scan-dirs", nil)
	if code != 200 {
		t.Fatalf("GET /api/scan-dirs status=%d: %s", code, raw)
	}
	assertNotContains(t, "GET /api/scan-dirs redacted", raw, fakeHome)
	if obj["include_home"] != false {
		t.Fatalf("expected include_home=false, got %v", obj["include_home"])
	}
	defDirs, _ := obj["default_dirs"].([]any)
	if len(defDirs) != 3 {
		t.Fatalf("expected 3 default_dirs (claude, codex, joycode), got %d (%s)", len(defDirs), raw)
	}
	var kinds []string
	for _, d := range defDirs {
		m := d.(map[string]any)
		kinds = append(kinds, m["kind"].(string))
	}
	if strings.Join(kinds, ",") != "claude,codex,joycode" {
		t.Fatalf("unexpected default_dirs kinds: %v (%s)", kinds, raw)
	}

	// GET /api/scan-dirs?redact=0 exposes full fakeHome.
	code, unredRaw, _ := doJSONReq(t, h, "GET", "/api/scan-dirs?redact=0", nil)
	if code != 200 || !strings.Contains(unredRaw, fakeHome) {
		t.Fatalf("GET /api/scan-dirs?redact=0 status=%d expected %s in:\n%s", code, fakeHome, unredRaw)
	}

	// POST /api/scan-dirs add customDir using ~ shorthand.
	code, addRaw, addObj := doJSONReq(t, h, "POST", "/api/scan-dirs", map[string]any{
		"action": "add",
		"paths":  []string{"~/custom-logs"},
	})
	if code != 200 {
		t.Fatalf("POST /api/scan-dirs add status=%d: %s", code, addRaw)
	}
	assertNotContains(t, "POST /api/scan-dirs add", addRaw, fakeHome)
	custDirs := addObj["custom_dirs"].([]any)
	if len(custDirs) != 1 {
		t.Fatalf("expected 1 custom dir, got %d (%s)", len(custDirs), addRaw)
	}
	cd0 := custDirs[0].(map[string]any)
	if int(cd0["files"].(float64)) != 2 {
		t.Fatalf("expected 2 files (.jsonl + .json) in custom dir, got %v", cd0["files"])
	}
	if int(addObj["local"].(float64)) < 2 {
		t.Fatalf("expected >= 2 scanned local records from .jsonl + .json, got %v", addObj["local"])
	}

	// Verify persistence file scan_dirs.json reloads on restart.
	cfgBytes, err := os.ReadFile(filepath.Join(dataDir, "scan_dirs.json"))
	if err != nil {
		t.Fatalf("read scan_dirs.json: %v", err)
	}
	if !strings.Contains(string(cfgBytes), customDir) {
		t.Fatalf("scan_dirs.json missing %s:\n%s", customDir, string(cfgBytes))
	}
	store2, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	scanMgr2 := NewScanManager(store2, dataDir, nil, false)
	_, cust2, incHome2 := scanMgr2.Status(false)
	if incHome2 || len(cust2) != 1 || cust2[0].Path != customDir {
		t.Fatalf("reloaded ScanManager mismatch: includeHome=%v custom=%+v", incHome2, cust2)
	}

	// POST /api/scan-dirs remove using redacted path returned in response (~\/custom-logs).
	code, rmRaw, rmObj := doJSONReq(t, h, "POST", "/api/scan-dirs", map[string]any{
		"action": "remove",
		"path":   cd0["path"],
	})
	if code != 200 {
		t.Fatalf("POST /api/scan-dirs remove status=%d: %s", code, rmRaw)
	}
	if len(rmObj["custom_dirs"].([]any)) != 0 {
		t.Fatalf("expected 0 custom_dirs after remove, got %s", rmRaw)
	}
	counts := rmObj["counts"].(map[string]any)
	if localCnt, _ := counts["local"].(float64); localCnt != 0 {
		t.Fatalf("expected local count 0 after removing only custom dir, got %v", localCnt)
	}

	// POST /api/scan-dirs toggle_home true must pick up ~/.joycode/sessions and emit joycode agent panel.
	code, thRaw, thObj := doJSONReq(t, h, "POST", "/api/scan-dirs", map[string]any{
		"action":       "toggle_home",
		"include_home": true,
	})
	if code != 200 || thObj["include_home"] != true {
		t.Fatalf("toggle_home=true failed: code=%d body=%s", code, thRaw)
	}
	if int(thObj["local"].(float64)) < 1 {
		t.Fatalf("expected joycode home scan to yield >= 1 record, got %s", thRaw)
	}

	code, jCtxRaw, jCtxObj := doJSONReq(t, h, "GET", "/api/context?days=7&source=local", nil)
	if code != 200 {
		t.Fatalf("GET /api/context for joycode failed: status=%d %s", code, jCtxRaw)
	}
	agentsList, _ := jCtxObj["agents"].([]any)
	foundJoyAgent := false
	for _, aRaw := range agentsList {
		am := aRaw.(map[string]any)
		if am["agent"] == "joycode" {
			foundJoyAgent = true
			break
		}
	}
	if !foundJoyAgent {
		t.Fatalf("expected joycode agent panel in /api/context, got:\n%s", jCtxRaw)
	}
	sessList, _ := jCtxObj["sessions"].([]any)
	if len(sessList) == 0 || sessList[0].(map[string]any)["title"] != "JoyCode Custom Title" {
		t.Fatalf("expected JoyCode Custom Title session in /api/context, got:\n%s", jCtxRaw)
	}
	joyRouteID := int64(sessList[0].(map[string]any)["latestId"].(float64))
	code, joyToolsRaw, _ := doJSONReq(t, h, "GET", "/api/context/text?id="+itoa(joyRouteID)+"&kind=tools", nil)
	if code != 200 || !strings.Contains(joyToolsRaw, "joycode_browser") || !strings.Contains(joyToolsRaw, "exec_command") {
		t.Fatalf("expected dynamic_tools and used tools in joycode /api/context/text?kind=tools, got status=%d body=%s", code, joyToolsRaw)
	}
}

func TestEmbeddedVendorAssetsServed(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(ServerConfig{Store: store})
	for _, path := range []string{
		"/",
		"/app.css",
		"/context.css",
		"/i18n.js",
		"/context.js",
		"/ctxtext.js",
		"/host.js",
		"/viewer.css",
		"/icons/claude-color.svg",
		"/icons/openai.svg",
		"/icons/jcode.svg",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s returned status %d (body=%q)", path, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("GET %s returned empty body", path)
		}
	}
}

func TestResolveListenAddr(t *testing.T) {
	noEnv := func(string) string { return "" }
	cases := []struct {
		name     string
		addrFlag string
		addrSet  bool
		hostFlag string
		portFlag int
		env      map[string]string
		want     string
	}{
		{name: "default", addrFlag: ":8787", want: ":8787"},
		{name: "port flag", addrFlag: ":8787", portFlag: 9000, want: ":9000"},
		{name: "host and port flags", addrFlag: ":8787", hostFlag: "127.0.0.1", portFlag: 9000, want: "127.0.0.1:9000"},
		{name: "addr bare port", addrFlag: "9000", addrSet: true, want: ":9000"},
		{name: "addr host and port", addrFlag: "0.0.0.0:8080", addrSet: true, want: "0.0.0.0:8080"},
		{name: "env CTXVIEW_PORT", addrFlag: ":8787", env: map[string]string{"CTXVIEW_PORT": "7777"}, want: ":7777"},
		{name: "env PORT fallback", addrFlag: ":8787", env: map[string]string{"PORT": "7778"}, want: ":7778"},
		{name: "env CTXVIEW_ADDR", addrFlag: ":8787", env: map[string]string{"CTXVIEW_ADDR": "127.0.0.1:6666"}, want: "127.0.0.1:6666"},
		{name: "port flag overrides CTXVIEW_PORT", addrFlag: ":8787", portFlag: 9001, env: map[string]string{"CTXVIEW_PORT": "7777"}, want: ":9001"},
		{name: "explicit addr overrides env", addrFlag: ":8787", addrSet: true, env: map[string]string{"CTXVIEW_PORT": "7777"}, want: ":8787"},
	}
	for _, tc := range cases {
		getenv := noEnv
		if tc.env != nil {
			getenv = func(k string) string { return tc.env[k] }
		}
		got := resolveListenAddr(tc.addrFlag, tc.addrSet, tc.hostFlag, tc.portFlag, getenv)
		if got != tc.want {
			t.Errorf("%s: resolveListenAddr(...) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

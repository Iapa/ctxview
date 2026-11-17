package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectProtocolAndPromptBreakdown(t *testing.T) {
	anthroBody := []byte(`{
		"model": "claude-sonnet-4-5",
		"system": "You are helpful.\nContents of /repo/CLAUDE.md (project instructions):\nFollow tests.",
		"tools": [{"name": "Read", "input_schema": {"type": "object"}}],
		"messages": [
			{"role": "user", "content": "Read main.go"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "call_1", "name": "Read", "input": {"file_path": "/repo/main.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_1", "content": "package main\nfunc main() {}"}]}
		]
	}`)
	proto, mdl := detectProtocolAndModel(anthroBody, "")
	if proto != ProtoAnthropic || mdl != "claude-sonnet-4-5" {
		t.Fatalf("got (%s, %s), want (anthropic, claude-sonnet-4-5)", proto, mdl)
	}
	p := promptOf(proto, anthroBody)
	if p == nil || p.Tokens <= 0 || p.Turns != 1 {
		t.Fatalf("unexpected prompt: %+v", p)
	}
	hasKind := map[string]bool{}
	for _, part := range p.Parts {
		hasKind[part.Kind] = true
	}
	for _, want := range []string{PartSystem, PartTools, PartMemory, PartFiles, PartChat} {
		if !hasKind[want] {
			t.Errorf("missing expected part %q in %+v", want, p.Parts)
		}
	}

	p.calibrate(12000, 200000)
	if !p.Counted || p.Tokens != 12000 || p.Window != 200000 {
		t.Fatalf("calibration failed: %+v", p)
	}

	pt, ok := promptTextOf(ProtoAnthropic, anthroBody, 1, PartFiles, true, p.Tokens)
	if !ok || len(pt.Items) != 1 || pt.Items[0].Name != "/repo/main.go" {
		t.Fatalf("unexpected promptTextOf(files): ok=%v pt=%+v", ok, pt)
	}
}

func TestReverseProxyGatewaySSEAndPersistence(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message_start\n"))
			_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":500,"cache_read_input_tokens":8000,"cache_creation_input_tokens":200}}}` + "\n\n"))
			_, _ = w.Write([]byte("event: content_block_delta\n"))
			_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello from upstream"}}` + "\n\n"))
			_, _ = w.Write([]byte("event: message_delta\n"))
			_, _ = w.Write([]byte(`data: {"type":"message_delta","usage":{"output_tokens":42}}` + "\n\n"))
			_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"Codex answer"}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Codex answer"}]}],"usage":{"input_tokens":10000,"output_tokens":55,"input_tokens_details":{"cached_tokens":9200}}}}` + "\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dataDir := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(ServerConfig{
		Store: store,
		Upstream: &UpstreamConfig{
			Anthropic: upstream.URL,
			OpenAI:    upstream.URL,
		},
	}))
	defer srv.Close()

	// 1. Anthropic proxy request
	reqBody := `{"model":"claude-sonnet-4-5","system":"System instructions","tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"Hi"}]}`
	req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("User-Agent", "claude-cli/2.1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sse1, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("proxy /v1/messages status=%d", resp.StatusCode)
	}
	if !strings.Contains(string(sse1), "Hello from upstream") {
		t.Fatalf("expected streamed SSE body to contain upstream output, got %q", string(sse1))
	}

	// 2. OpenAI Responses proxy request
	respReqBody := `{"model":"gpt-5-codex","instructions":"Codex instructions","input":[{"type":"message","role":"user","content":"Check status"}]}`
	req2, _ := http.NewRequest("POST", srv.URL+"/v1/responses", strings.NewReader(respReqBody))
	req2.Header.Set("User-Agent", "codex-cli/0.165")
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	sse2, err := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != 200 {
		t.Fatalf("proxy /v1/responses status=%d", resp2.StatusCode)
	}
	if !strings.Contains(string(sse2), "Codex answer") {
		t.Fatalf("expected streamed SSE body to contain upstream output, got %q", string(sse2))
	}

	recs := store.List(SourceProxy)
	if len(recs) != 2 {
		t.Fatalf("expected 2 proxy records, got %d", len(recs))
	}

	anthroRec := recs[0]
	if anthroRec.Prompt == nil || !anthroRec.Prompt.Counted || anthroRec.Prompt.Tokens != 8700 || anthroRec.CacheRead != 8000 || anthroRec.Input != 500 {
		t.Fatalf("unexpected anthro calibrated record: tokens=%d counted=%v cr=%d in=%d",
			anthroRec.Prompt.Tokens, anthroRec.Prompt.Counted, anthroRec.CacheRead, anthroRec.Input)
	}

	respRec := recs[1]
	if respRec.Prompt == nil || !respRec.Prompt.Counted || respRec.Prompt.Tokens != 10000 || respRec.CacheRead != 9200 || respRec.Input != 800 {
		t.Fatalf("unexpected responses calibrated record: tokens=%d counted=%v cr=%d in=%d",
			respRec.Prompt.Tokens, respRec.Prompt.Counted, respRec.CacheRead, respRec.Input)
	}

	// Verify /api/context/text and /api/context/reply work for the captured proxy request
	textResp, err := http.Get(srv.URL + "/api/context/text?id=1&kind=system&item=0")
	if err != nil {
		t.Fatal(err)
	}
	defer textResp.Body.Close()
	var pt PromptText
	if err := json.NewDecoder(textResp.Body).Decode(&pt); err != nil {
		t.Fatal(err)
	}
	if len(pt.Items) == 0 || len(pt.Items[0].Pieces) == 0 || !strings.Contains(pt.Items[0].Pieces[0].Text, "System instructions") {
		t.Fatalf("unexpected prompt text pieces: %+v", pt)
	}

	replyResp, err := http.Get(srv.URL + "/api/context/reply?id=1")
	if err != nil {
		t.Fatal(err)
	}
	defer replyResp.Body.Close()
	var pr PromptReply
	if err := json.NewDecoder(replyResp.Body).Decode(&pr); err != nil {
		t.Fatal(err)
	}
	if len(pr.Pieces) == 0 || pr.Pieces[0].Text != "Hello from upstream" {
		t.Fatalf("unexpected prompt reply: %+v", pr)
	}

	// Verify reload from disk
	store2, err := NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(store2.List(SourceProxy)) != 2 {
		t.Fatalf("expected 2 persisted records on reload, got %d", len(store2.List(SourceProxy)))
	}
}

func TestLocalSessionJSONLParsingAndUpload(t *testing.T) {
	sessDir := t.TempDir()

	// Write a realistic Claude Code session JSONL with split assistant message.id blocks.
	claudeFile := filepath.Join(sessDir, "claude-session.jsonl")
	claudeJSONL := strings.Join([]string{
		`{"type":"user","sessionId":"sess-claude-1","cwd":"/workspace/demo","timestamp":"2026-11-12T10:00:00Z","message":{"role":"user","content":"Inspect server.go"}}`,
		`{"type":"assistant","sessionId":"sess-claude-1","cwd":"/workspace/demo","timestamp":"2026-11-12T10:00:02Z","message":{"id":"m1","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"thinking","thinking":"Checking server"}],"usage":{"input_tokens":12000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":20}}}`,
		`{"type":"assistant","sessionId":"sess-claude-1","cwd":"/workspace/demo","timestamp":"2026-11-12T10:00:03Z","message":{"id":"m1","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"tu1","name":"Read","input":{"file_path":"/workspace/demo/server.go"}}],"usage":{"input_tokens":12000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":45}}}`,
		`{"type":"user","sessionId":"sess-claude-1","cwd":"/workspace/demo","timestamp":"2026-11-12T10:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"package main\n// server code here"}]}}`,
		`{"type":"assistant","sessionId":"sess-claude-1","cwd":"/workspace/demo","timestamp":"2026-11-12T10:00:06Z","message":{"id":"m2","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"I inspected server.go."}],"usage":{"input_tokens":400,"cache_read_input_tokens":12100,"cache_creation_input_tokens":0,"output_tokens":30}}}`,
	}, "\n")
	if err := os.WriteFile(claudeFile, []byte(claudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a realistic Codex session JSONL.
	codexFile := filepath.Join(sessDir, "rollout-codex-1.jsonl")
	codexJSONL := strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-11-12T11:00:00Z","payload":{"session_id":"sess-codex-1","cwd":"/workspace/demo","base_instructions":"Be concise","context_window":272000}}`,
		`{"type":"turn_context","timestamp":"2026-11-12T11:00:00Z","payload":{"model":"gpt-5-codex"}}`,
		`{"type":"response_item","timestamp":"2026-11-12T11:00:01Z","payload":{"type":"additional_tools","tools":[{"type":"function","name":"exec_command"}]}}`,
		`{"type":"response_item","timestamp":"2026-11-12T11:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Run tests"}]}}`,
		`{"type":"event_msg","timestamp":"2026-11-12T11:00:03Z","payload":{"type":"token_count","info":{"model_context_window":272000,"total_token_usage":{"total_tokens":18100},"last_token_usage":{"input_tokens":18000,"cached_input_tokens":14000,"output_tokens":100}}}}`,
	}, "\n")
	if err := os.WriteFile(codexFile, []byte(codexJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n := ScanLocalSessions(store, []string{sessDir}, 20, false)
	if n != 3 {
		t.Fatalf("expected 3 scanned local records (2 claude + 1 codex), got %d", n)
	}

	srv := httptest.NewServer(NewHandler(ServerConfig{
		Store:       store,
		SessionDirs: []string{sessDir},
	}))
	defer srv.Close()

	// Check /api/context
	ctxResp, err := http.Get(srv.URL + "/api/context?days=30&source=local")
	if err != nil {
		t.Fatal(err)
	}
	defer ctxResp.Body.Close()
	var rep ctxReport
	if err := json.NewDecoder(ctxResp.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Agents) != 2 || len(rep.Sessions) != 2 {
		t.Fatalf("expected 2 agents and 2 sessions, got agents=%d sessions=%d", len(rep.Agents), len(rep.Sessions))
	}

	// Check lazy text extraction for Claude turn 2 (contains /workspace/demo/server.go)
	var claudeTurn2 *RouteRecord
	for _, r := range store.List(SourceLocal) {
		if r.Agent == "claude-code" && r.Turn == 2 {
			claudeTurn2 = r
		}
	}
	if claudeTurn2 == nil || claudeTurn2.Prompt.Tokens != 12500 {
		t.Fatalf("unexpected claude turn 2: %+v", claudeTurn2)
	}
	txtRes, err := http.Get(srv.URL + "/api/context/text?id=" + itoa(claudeTurn2.ID) + "&kind=files&item=0")
	if err != nil {
		t.Fatal(err)
	}
	defer txtRes.Body.Close()
	var pt PromptText
	if err := json.NewDecoder(txtRes.Body).Decode(&pt); err != nil {
		t.Fatal(err)
	}
	if len(pt.Items) == 0 || pt.Items[0].Name != "/workspace/demo/server.go" {
		t.Fatalf("expected server.go in lazy files inspection, got %+v", pt)
	}

	// Check POST /api/upload
	upPayload := map[string]any{
		"raw":        `{"model":"gpt-5.4","messages":[{"role":"system","content":"System prompt"},{"role":"user","content":"Hello world"}]}`,
		"window":     400000,
		"input":      500,
		"cache_read": 4500,
		"title":      "Manual Paste Test",
	}
	upBytes, _ := json.Marshal(upPayload)
	upResp, err := http.Post(srv.URL+"/api/upload", "application/json", bytes.NewReader(upBytes))
	if err != nil {
		t.Fatal(err)
	}
	defer upResp.Body.Close()
	if upResp.StatusCode != 200 {
		t.Fatalf("upload failed status=%d", upResp.StatusCode)
	}
	var upOut struct {
		OK    bool         `json:"ok"`
		Count int          `json:"count"`
		Route *RouteRecord `json:"route"`
	}
	if err := json.NewDecoder(upResp.Body).Decode(&upOut); err != nil {
		t.Fatal(err)
	}
	if !upOut.OK || upOut.Count != 1 || upOut.Route.Prompt.Tokens != 5000 || upOut.Route.Prompt.Window != 400000 {
		t.Fatalf("unexpected upload response: %+v", upOut)
	}

	// Verify all static assets return HTTP 200
	for _, path := range []string{"/", "/host.js", "/viewer.css", "/app.css", "/context.css", "/i18n.js", "/context.js", "/ctxtext.js"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s error: %v", path, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 200 {
			t.Errorf("GET %s status=%d", path, res.StatusCode)
		}
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}


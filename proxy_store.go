package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	SourceProxy  = "proxy"
	SourceLocal  = "local"
	SourceUpload = "upload"
	SourceDemo   = "demo"
)

// RouteUsage mirrors the JSON shape expected by internal/gui/assets/context.js.
// Total input prompt tokens = In + CacheRead + CacheWrite.
type RouteUsage struct {
	In           int  `json:"in"`
	Out          int  `json:"out,omitempty"`
	CacheRead    int  `json:"cache_read,omitempty"`
	CacheWrite   int  `json:"cache_write,omitempty"`
	CacheUnknown bool `json:"cache_unknown,omitempty"`
}

// RouteRecord stores one intercepted, local-session, uploaded, or demo request turn.
type RouteRecord struct {
	ID           int64        `json:"id"`
	Source       string       `json:"source"`
	Time         time.Time    `json:"time"`
	Millis       int64        `json:"millis,omitempty"`
	Done         bool         `json:"done"`
	Agent        string       `json:"agent"`
	Client       string       `json:"client,omitempty"`
	Provider     string       `json:"provider,omitempty"`
	Protocol     string       `json:"protocol"`
	Model        string       `json:"model"`
	Path         string       `json:"path,omitempty"`
	Session      string       `json:"session,omitempty"`
	SessionTitle string       `json:"session_title,omitempty"`
	Turn         int          `json:"turn,omitempty"`
	Input        int          `json:"input"`
	Output       int          `json:"output"`
	CacheRead    int          `json:"cache_read"`
	CacheWrite   int          `json:"cache_write"`
	Usage        []RouteUsage `json:"usage,omitempty"`
	Prompt       *Prompt      `json:"prompt"`
	Status       int          `json:"status"`
	Error        string       `json:"error,omitempty"`

	reqBody    []byte
	respBody   []byte
	bodyLoader func() ([]byte, []byte, error)
}

type Store struct {
	mu      sync.RWMutex
	dataDir string
	nextID  atomic.Int64
	records []*RouteRecord
	byID    map[int64]*RouteRecord
}

func NewStore(dataDir string) (*Store, error) {
	s := &Store{
		dataDir: dataDir,
		byID:    map[int64]*RouteRecord{},
	}
	s.nextID.Store(1)
	if dataDir != "" {
		if err := os.MkdirAll(filepath.Join(dataDir, "bodies"), 0o755); err != nil {
			return nil, err
		}
		if err := s.loadPersisted(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) loadPersisted() error {
	path := filepath.Join(s.dataDir, "records.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var maxID int64
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec RouteRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec.ID > maxID {
			maxID = rec.ID
		}
		id := rec.ID
		rec.bodyLoader = func() ([]byte, []byte, error) {
			req, errReq := os.ReadFile(filepath.Join(s.dataDir, "bodies", fmt.Sprintf("%d.req.json", id)))
			resp, _ := os.ReadFile(filepath.Join(s.dataDir, "bodies", fmt.Sprintf("%d.resp.bin", id)))
			return req, resp, errReq
		}
		s.records = append(s.records, &rec)
		s.byID[rec.ID] = &rec
	}
	if maxID >= s.nextID.Load() {
		s.nextID.Store(maxID + 1)
	}
	return sc.Err()
}

func (s *Store) Add(rec *RouteRecord, reqBody, respBody []byte, persist bool) *RouteRecord {
	if rec.ID == 0 {
		rec.ID = s.nextID.Add(1) - 1
	} else {
		for {
			cur := s.nextID.Load()
			if rec.ID < cur || s.nextID.CompareAndSwap(cur, rec.ID+1) {
				break
			}
		}
	}
	if len(reqBody) > 0 {
		rec.reqBody = append([]byte(nil), reqBody...)
	}
	if len(respBody) > 0 {
		rec.respBody = append([]byte(nil), respBody...)
	}

	s.mu.Lock()
	s.records = append(s.records, rec)
	s.byID[rec.ID] = rec
	s.mu.Unlock()

	if persist && s.dataDir != "" {
		s.persistRecord(rec, reqBody, respBody)
	}
	return rec
}

func (s *Store) persistRecord(rec *RouteRecord, reqBody, respBody []byte) {
	if len(reqBody) > 0 {
		_ = os.WriteFile(filepath.Join(s.dataDir, "bodies", fmt.Sprintf("%d.req.json", rec.ID)), reqBody, 0o644)
	}
	if len(respBody) > 0 {
		_ = os.WriteFile(filepath.Join(s.dataDir, "bodies", fmt.Sprintf("%d.resp.bin", rec.ID)), respBody, 0o644)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.dataDir, "records.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// ReplaceSource replaces all records of a given source (used when rescanning local sessions).
func (s *Store) ReplaceSource(source string, incoming []*RouteRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := make([]*RouteRecord, 0, len(s.records)+len(incoming))
	for _, r := range s.records {
		if r.Source == source {
			delete(s.byID, r.ID)
			continue
		}
		kept = append(kept, r)
	}
	for _, r := range incoming {
		if r.ID == 0 {
			r.ID = s.nextID.Add(1) - 1
		}
		r.Source = source
		kept = append(kept, r)
		s.byID[r.ID] = r
	}
	s.records = kept
}

func (s *Store) Get(id int64) (*RouteRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byID[id]
	return r, ok
}

func (s *Store) Bodies(id int64) ([]byte, []byte, bool) {
	r, ok := s.Get(id)
	if !ok {
		return nil, nil, false
	}
	if len(r.reqBody) > 0 || len(r.respBody) > 0 {
		return r.reqBody, r.respBody, true
	}
	if r.bodyLoader != nil {
		req, resp, err := r.bodyLoader()
		if err == nil && (len(req) > 0 || len(resp) > 0) {
			return req, resp, true
		}
	}
	return nil, nil, false
}

func (s *Store) List(source string) []*RouteRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*RouteRecord, 0, len(s.records))
	for _, r := range s.records {
		if source != "" && source != "all" && r.Source != source {
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *RouteRecord) int {
		if !a.Time.Equal(b.Time) {
			return a.Time.Compare(b.Time)
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return out
}

func (s *Store) Counts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]int{
		"all":        0,
		SourceLocal:  0,
		SourceProxy:  0,
		SourceUpload: 0,
		SourceDemo:   0,
	}
	for _, r := range s.records {
		counts["all"]++
		counts[r.Source]++
	}
	return counts
}

// UpstreamConfig holds default upstream targets per protocol.
type UpstreamConfig struct {
	Anthropic string
	OpenAI    string
	Gemini    string
	Client    *http.Client
}

func (cfg *UpstreamConfig) resolveTarget(r *http.Request, proto Protocol) (string, error) {
	if raw, ok := strings.CutPrefix(r.URL.Path, "/proxy/"); ok && raw != "" {
		target := raw
		if r.URL.RawQuery != "" {
			if strings.Contains(target, "?") {
				target += "&" + r.URL.RawQuery
			} else {
				target += "?" + r.URL.RawQuery
			}
		}
		if _, err := url.ParseRequestURI(target); err == nil {
			return target, nil
		}
	}
	if h := strings.TrimSpace(r.Header.Get("X-Target-URL")); h != "" {
		return joinUpstream(h, r.URL.Path, r.URL.RawQuery)
	}
	var base string
	switch proto {
	case ProtoAnthropic:
		base = cfg.Anthropic
	case ProtoGemini:
		base = cfg.Gemini
	default:
		base = cfg.OpenAI
	}
	if base == "" {
		return "", fmt.Errorf("no upstream configured for protocol %s", proto)
	}
	return joinUpstream(base, r.URL.Path, r.URL.RawQuery)
}

func joinUpstream(base, reqPath, rawQuery string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(reqPath, "/v1/") {
		reqPath = strings.TrimPrefix(reqPath, "/v1")
	}
	u.Path = basePath + reqPath
	if rawQuery != "" {
		if u.RawQuery != "" {
			u.RawQuery += "&" + rawQuery
		} else {
			u.RawQuery = rawQuery
		}
	}
	return u.String(), nil
}

func detectAgentFromRequest(r *http.Request) (agent, client string) {
	if a := strings.TrimSpace(r.Header.Get("X-Agent")); a != "" {
		return a, a
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	switch {
	case strings.Contains(ua, "claude"):
		return "claude-code", r.Header.Get("User-Agent")
	case strings.Contains(ua, "joycode") || strings.Contains(ua, "jcode"):
		return "joycode", r.Header.Get("User-Agent")
	case strings.Contains(ua, "codex"):
		return "codex", r.Header.Get("User-Agent")
	case strings.Contains(ua, "opencode"):
		return "opencode", r.Header.Get("User-Agent")
	case strings.Contains(ua, "cursor"):
		return "cursor", r.Header.Get("User-Agent")
	case strings.Contains(ua, "gemini"):
		return "gemini", r.Header.Get("User-Agent")
	case ua != "":
		return "proxy-client", r.Header.Get("User-Agent")
	default:
		return "proxy-client", "HTTP Client"
	}
}

func inferSessionFromRequest(r *http.Request, body []byte, agent string) string {
	for _, h := range []string{"X-Session-ID", "X-Conversation-ID", "Conversation_id", "Session_id"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	var meta struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
		PromptCacheKey string `json:"prompt_cache_key"`
		Messages       []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &meta) == nil {
		if meta.PromptCacheKey != "" {
			return meta.PromptCacheKey
		}
		if meta.Metadata.UserID != "" {
			if _, after, ok := strings.Cut(meta.Metadata.UserID, "_session_"); ok && after != "" {
				return after
			}
		}
		for _, m := range meta.Messages {
			if m.Role == "user" {
				h := sha1.Sum(append([]byte(agent+":"), m.Content...))
				return "conv-" + hex.EncodeToString(h[:6])
			}
		}
		if len(meta.Input) > 0 {
			items, ok := inputList(meta.Input)
			if ok && len(items) > 0 {
				h := sha1.Sum(append([]byte(agent+":"), items[0]...))
				return "conv-" + hex.EncodeToString(h[:6])
			}
			h := sha1.Sum(append([]byte(agent+":"), meta.Input...))
			return "conv-" + hex.EncodeToString(h[:6])
		}
	}
	return "proxy-" + time.Now().Format("20060102-150405")
}

// extractUsageFromResponse extracts input/output/cache tokens from JSON or SSE response bodies.
// Returns (freshInput, output, cacheRead, cacheWrite, cacheUnknown).
func extractUsageFromResponse(proto Protocol, respBytes []byte) (freshIn, outTok, cacheRead, cacheWrite int, cacheUnknown bool) {
	trim := bytes.TrimSpace(respBytes)
	if len(trim) == 0 {
		return 0, 0, 0, 0, true
	}

	applyUsage := func(in, out, cr, cw int, totalIncludesCache bool) {
		if out > outTok {
			outTok = out
		}
		if cr > cacheRead {
			cacheRead = cr
		}
		if cw > cacheWrite {
			cacheWrite = cw
		}
		if in > 0 || cr > 0 || cw > 0 {
			if totalIncludesCache {
				f := in - cr - cw
				if f < 0 {
					f = 0
				}
				freshIn = f
			} else {
				freshIn = in
			}
			cacheUnknown = false
		}
	}

	parseObj := func(raw []byte) {
		var u struct {
			Usage *struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				PromptTokens             int `json:"prompt_tokens"`
				CompletionTokens         int `json:"completion_tokens"`
				PromptCacheHitTokens     int `json:"prompt_cache_hit_tokens"`
				InputTokensDetails       *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
				PromptTokensDetails *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
			Message *struct {
				Usage *struct {
					InputTokens              int `json:"input_tokens"`
					OutputTokens             int `json:"output_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Response *struct {
				Usage *struct {
					InputTokens        int `json:"input_tokens"`
					OutputTokens       int `json:"output_tokens"`
					InputTokensDetails *struct {
						CachedTokens int `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
			} `json:"response"`
			UsageMetadata *struct {
				PromptTokenCount        int `json:"promptTokenCount"`
				CandidatesTokenCount    int `json:"candidatesTokenCount"`
				CachedContentTokenCount int `json:"cachedContentTokenCount"`
			} `json:"usageMetadata"`
		}
		if json.Unmarshal(raw, &u) != nil {
			return
		}
		if u.Usage != nil {
			x := u.Usage
			if x.InputTokens > 0 || x.CacheReadInputTokens > 0 || x.CacheCreationInputTokens > 0 {
				cr := x.CacheReadInputTokens
				if x.InputTokensDetails != nil && x.InputTokensDetails.CachedTokens > cr {
					cr = x.InputTokensDetails.CachedTokens
				}
				includesCache := proto == ProtoResponses || x.InputTokensDetails != nil
				applyUsage(x.InputTokens, x.OutputTokens, cr, x.CacheCreationInputTokens, includesCache)
			}
			if x.PromptTokens > 0 || x.CompletionTokens > 0 {
				cr := x.PromptCacheHitTokens
				if x.PromptTokensDetails != nil && x.PromptTokensDetails.CachedTokens > cr {
					cr = x.PromptTokensDetails.CachedTokens
				}
				applyUsage(x.PromptTokens, x.CompletionTokens, cr, 0, true)
			}
		}
		if u.Message != nil && u.Message.Usage != nil {
			x := u.Message.Usage
			applyUsage(x.InputTokens, x.OutputTokens, x.CacheReadInputTokens, x.CacheCreationInputTokens, false)
		}
		if u.Response != nil && u.Response.Usage != nil {
			x := u.Response.Usage
			cr := 0
			if x.InputTokensDetails != nil {
				cr = x.InputTokensDetails.CachedTokens
			}
			applyUsage(x.InputTokens, x.OutputTokens, cr, 0, true)
		}
		if u.UsageMetadata != nil {
			x := u.UsageMetadata
			applyUsage(x.PromptTokenCount, x.CandidatesTokenCount, x.CachedContentTokenCount, 0, true)
		}
	}

	cacheUnknown = true
	if json.Valid(trim) {
		parseObj(trim)
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(trim))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data != "" && data != "[DONE]" && json.Valid([]byte(data)) {
			parseObj([]byte(data))
		}
	}
	return
}

func modelWindow(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "[1m]") || strings.HasSuffix(m, "-1m") || strings.Contains(m, "-1m-") || strings.Contains(m, ":1m"):
		return 1000000
	case strings.Contains(m, "gpt-4.1"):
		return 1047576
	case strings.Contains(m, "gemini"):
		return 1000000
	case strings.Contains(m, "gpt-5") || strings.Contains(m, "gpt-6") || strings.Contains(m, "codex"):
		return 272000
	case strings.Contains(m, "claude") || strings.Contains(m, "kimi") ||
		strings.Contains(m, "o1") || strings.Contains(m, "o3") || strings.Contains(m, "o4"):
		return 200000
	case strings.Contains(m, "qwen"):
		return 131072
	case strings.Contains(m, "deepseek") || strings.Contains(m, "glm"):
		return 128000
	default:
		return 200000
	}
}

func (s *Store) handleProxy(cfg *UpstreamConfig) http.HandlerFunc {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		reqBody, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			http.Error(w, "failed to read request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()

		proto, model := detectProtocolAndModel(reqBody, r.URL.Path)
		targetURL, err := cfg.resolveTarget(r, proto)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		agent, ua := detectAgentFromRequest(r)
		sessionID := inferSessionFromRequest(r, reqBody, agent)
		prompt := promptOf(proto, reqBody)
		win := modelWindow(model)
		if prompt != nil && win > 0 {
			prompt.Window = win
		}

		upReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(reqBody))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, vals := range r.Header {
			lk := strings.ToLower(k)
			if lk == "host" || lk == "content-length" || lk == "accept-encoding" || lk == "x-target-url" {
				continue
			}
			for _, v := range vals {
				upReq.Header.Add(k, v)
			}
		}
		upReq.Header.Set("Accept-Encoding", "identity")

		resp, err := client.Do(upReq)
		if err != nil {
			rec := &RouteRecord{
				Source:   SourceProxy,
				Time:     started,
				Millis:   time.Since(started).Milliseconds(),
				Done:     true,
				Agent:    agent,
				Client:   ua,
				Provider: string(proto),
				Protocol: string(proto),
				Model:    model,
				Path:     r.URL.Path,
				Session:  sessionID,
				Prompt:   prompt,
				Status:   http.StatusBadGateway,
				Error:    err.Error(),
			}
			s.Add(rec, reqBody, nil, true)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for k, vals := range resp.Header {
			if strings.EqualFold(k, "Content-Length") {
				continue
			}
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)

		var rawBuf bytes.Buffer
		flusher, canFlush := w.(http.Flusher)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				rawBuf.Write(chunk)
				_, _ = w.Write(chunk)
				if canFlush {
					flusher.Flush()
				}
			}
			if readErr != nil {
				break
			}
		}

		capturedResp := rawBuf.Bytes()
		if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
			if gz, err := gzip.NewReader(bytes.NewReader(capturedResp)); err == nil {
				if plain, err := io.ReadAll(gz); err == nil {
					capturedResp = plain
				}
				_ = gz.Close()
			}
		}

		freshIn, outTok, cr, cw, cacheUnknown := extractUsageFromResponse(proto, capturedResp)
		countedTotal := freshIn + cr + cw
		if prompt != nil && countedTotal > 0 {
			prompt.calibrate(countedTotal, win)
		}

		rec := &RouteRecord{
			Source:     SourceProxy,
			Time:       started,
			Millis:     time.Since(started).Milliseconds(),
			Done:       true,
			Agent:      agent,
			Client:     ua,
			Provider:   string(proto),
			Protocol:   string(proto),
			Model:      model,
			Path:       r.URL.Path,
			Session:    sessionID,
			Input:      freshIn,
			Output:     outTok,
			CacheRead:  cr,
			CacheWrite: cw,
			Usage: []RouteUsage{{
				In:           freshIn,
				Out:          outTok,
				CacheRead:    cr,
				CacheWrite:   cw,
				CacheUnknown: cacheUnknown,
			}},
			Prompt: prompt,
			Status: resp.StatusCode,
		}
		s.Add(rec, reqBody, capturedResp, true)
	}
}

// seedDemoRecords adds a multi-turn demo session reflecting the screenshot for immediate visualization.
func seedDemoRecords(store *Store) {
	now := time.Now().Add(-15 * time.Minute)
	model := "workbuddy-ai/hy4free"
	sessionKey := "demo-hy4free-session"
	sessionTitle := "Context Window Inspection Demo (hy4free)"

	systemText := `<system>
You are an expert AI coding assistant running inside the user's workspace.
Follow all repository coding standards, verify edits with tests, and never hallucinate tool output.
` + strings.Repeat("Detailed workspace guidelines, safety constraints, and execution rules.\n", 260) + `</system>`

	agentsMD := "Contents of /workspace/project/AGENTS.md (project instructions):\n\n# Notes for coding agents\nRead docs/subsystems/README.md and LESSONS.md before modifying gateway or provider packages."

	toolsList := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "run_command", "description": "Execute a shell command in the workspace.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}, "cwd": map[string]any{"type": "string"}, "pad": strings.Repeat("cmd_spec ", 60)}}}},
		{"type": "function", "function": map[string]any{"name": "run_tests", "description": "Run targeted Go unit and integration tests.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"pkg": map[string]any{"type": "string"}, "pad": strings.Repeat("test_spec ", 45)}}}},
		{"type": "function", "function": map[string]any{"name": "search_graph", "description": "Query codebase knowledge graph symbols and call paths.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string"}, "pad": strings.Repeat("graph_spec ", 38)}}}},
		{"type": "function", "function": map[string]any{"name": "edit_file", "description": "Apply a contiguous replacement edit to a file.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "pad": strings.Repeat("edit_spec ", 32)}}}},
	}
	for i := 1; i <= 17; i++ {
		toolsList = append(toolsList, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        fmt.Sprintf("workspace_tool_%02d", i),
				"description": fmt.Sprintf("Auxiliary workspace inspection and helper tool #%d.", i),
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{"arg": map[string]any{"type": "string"}, "pad": strings.Repeat("schema ", 20)}},
			},
		})
	}

	messages := []map[string]any{
		{"role": "system", "content": systemText + "\n\n" + agentsMD},
		{"role": "user", "content": "第 1 轮：请检查当前项目的上下文窗口可视化逻辑，定位 context.js 和 prompt.go。"},
		{"role": "assistant", "content": "我先检索项目结构并查看相关模块。", "tool_calls": []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "search_graph", "arguments": `{"pattern":"promptOf|ctxCard"}`}}}},
		{"role": "tool", "tool_call_id": "c1", "content": strings.Repeat("Found internal/gateway/prompt.go:promptOf, internal/gui/assets/context.js:ctxCard.\n", 80)},
		{"role": "user", "content": "第 2 轮：分析一下 Anthropic 和 OpenAI Responses 协议在 Token 校准上的区别。"},
		{"role": "assistant", "content": "Anthropic 的 input_tokens 不包含 cache_read/cache_write；OpenAI 的 input_tokens 已经包含 cached_tokens。\n" + strings.Repeat("详细分析两者的字段映射和占比缩放逻辑。", 40)},
		{"role": "user", "content": "第 3 轮：请运行测试并列出完整的工具返回输出与上下文结构。" + strings.Repeat(" 说明各部分的比例、缓存命中率计算方式以及历史轮次的累计占用。", 180)},
		{"role": "assistant", "content": "正在执行构建和测试，并收集完整输出。", "tool_calls": []any{
			map[string]any{"id": "c2", "type": "function", "function": map[string]any{"name": "run_command", "arguments": `{"command":"go test ./..."}`}},
			map[string]any{"id": "c3", "type": "function", "function": map[string]any{"name": "run_tests", "arguments": `{"pkg":"./internal/gateway"}`}},
		}},
		{"role": "tool", "tool_call_id": "c2", "content": strings.Repeat("PASS ok example.com/project/internal/gateway 1.24s\n", 180)},
		{"role": "tool", "tool_call_id": "c3", "content": strings.Repeat("PASS ok example.com/project/internal/gui 0.89s\n", 150)},
		{"role": "user", "content": "第 4 轮：确认 treemap 平方分割算法如何避免超窄条。"},
		{"role": "assistant", "content": "tilesOf 通过 TILE_FLOOR=700 和 TILE_SIDE=16 对极小块做保底提升。"},
		{"role": "user", "content": "第 5 轮：检查 session sparkline 点击后的 openRoute 行为。"},
		{"role": "assistant", "content": "点击 sparkline 数据点会触发 onPoint(pt) -> window.openRoute(pt.id, pt.time)。"},
		{"role": "user", "content": "第 6 轮：确认本地 Session `.jsonl` 的增量重构如何计算系统提示基线。"},
		{"role": "assistant", "content": "用 vendor 返回的总 input tokens 减去消息体估算 token，即可还原 static system/tools 基线。"},
		{"role": "user", "content": "第 7 轮：检查代理网关对 SSE 流式响应中 usage 事件的捕获。"},
		{"role": "assistant", "content": "Anthropic 读取 message_start 和 message_delta；OpenAI Responses 读取 response.completed。"},
		{"role": "user", "content": "第 8 轮：生成最终汇总并确认所有 960K 窗口下的缓存命中率展示。"},
	}

	// Build 8 incremental turns along the conversation.
	userIndices := []int{}
	for i, m := range messages {
		if m["role"] == "user" {
			userIndices = append(userIndices, i)
		}
	}

	turnTargets := []struct {
		total int
		cache int
	}{
		{42100, 38000},
		{46800, 42100},
		{58900, 46400},
		{74200, 58500},
		{77500, 73800},
		{80100, 77000},
		{83200, 79800},
		{85800, 85276},
	}

	for ti, uidx := range userIndices {
		end := uidx + 1
		if ti+1 < len(userIndices) {
			end = userIndices[ti+1] - 1
		}
		if end <= uidx {
			end = uidx + 1
		}
		sliceMsgs := messages[:uidx+1]
		reqObj := map[string]any{
			"model":    model,
			"tools":    toolsList,
			"messages": sliceMsgs,
		}
		reqBytes, _ := json.Marshal(reqObj)
		replyText := "已完成第 " + fmt.Sprintf("%d", ti+1) + " 轮分析：所有字段与前端 Context Window 视图一致。"
		if end > uidx+1 && messages[uidx+1]["role"] == "assistant" {
			if s, ok := messages[uidx+1]["content"].(string); ok {
				replyText = s
			}
		}
		respBytes, _ := json.Marshal(map[string]any{
			"choices": []any{
				map[string]any{"message": map[string]any{"role": "assistant", "content": replyText}},
			},
		})

		tgt := turnTargets[ti]
		p := promptOf(ProtoChat, reqBytes)
		if ti == len(userIndices)-1 {
			// Match the screenshot's exact proportions cleanly in the final turn.
			p = &Prompt{
				Window:  960000,
				Tokens:  85800,
				Counted: true,
				Turns:   8,
				Parts: []PromptPart{
					{Kind: PartSystem, Tokens: 15300, Items: []PromptItem{{Name: "prompt", Tokens: 15300}}},
					{Kind: PartTools, Tokens: 23200, Items: []PromptItem{
						{Name: "run_command", Tokens: 3600},
						{Name: "run_tests", Tokens: 2600},
						{Name: "search_graph", Tokens: 2200},
						{Name: "edit_file", Tokens: 1900},
						{Name: "other", Tag: "more", N: 17, Tokens: 12900},
					}},
					{Kind: PartMemory, Tokens: 167, Items: []PromptItem{{Name: "/workspace/project/AGENTS.md", Tag: "project", Tokens: 167}}},
					{Kind: PartResults, Tokens: 24800, Items: []PromptItem{
						{Name: "run_command", Tag: "result", N: 2, Tokens: 14600},
						{Name: "run_tests", Tag: "result", N: 1, Tokens: 6400},
						{Name: "search_graph", Tag: "result", N: 1, Tokens: 3800},
					}},
					{Kind: PartChat, Tokens: 22400, Items: []PromptItem{
						{Name: "3", Tag: "turn", Tokens: 12100},
						{Name: "8", Tag: "turn", Tokens: 3300},
						{Name: "2", Tag: "turn", Tokens: 2400},
						{Name: "1", Tag: "turn", Tokens: 1900},
						{Name: "7", Tag: "turn", Tokens: 900},
						{Name: "6", Tag: "turn", Tokens: 700},
						{Name: "5", Tag: "turn", Tokens: 600},
						{Name: "4", Tag: "turn", Tokens: 500},
					}},
				},
			}
			// Keep the full messages for turn 8 inspectability
			fullReq, _ := json.Marshal(map[string]any{"model": model, "tools": toolsList, "messages": messages})
			reqBytes = fullReq
		} else if p != nil {
			p.calibrate(tgt.total, 960000)
		}

		fresh := tgt.total - tgt.cache
		rec := &RouteRecord{
			Source:       SourceDemo,
			Time:         now.Add(time.Duration(ti) * 90 * time.Second),
			Millis:       1250 + int64(ti*140),
			Done:         true,
			Agent:        "OpenAI",
			Client:       "Demo Session",
			Provider:     "openai",
			Protocol:     string(ProtoChat),
			Model:        model,
			Session:      sessionKey,
			SessionTitle: sessionTitle,
			Turn:         ti + 1,
			Input:        fresh,
			Output:       420,
			CacheRead:    tgt.cache,
			Usage:        []RouteUsage{{In: fresh, Out: 420, CacheRead: tgt.cache}},
			Prompt:       p,
			Status:       200,
		}
		store.Add(rec, reqBytes, respBytes, false)
	}
}


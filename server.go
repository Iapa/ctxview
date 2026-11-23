package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

const defaultWindow = 200000

type ctxPoint struct {
	ID           int64     `json:"id"`
	Time         time.Time `json:"time"`
	Tokens       int       `json:"tokens"`
	Window       int       `json:"window,omitempty"`
	Cache        int       `json:"cache,omitempty"`
	CacheUnknown bool      `json:"cache_unknown,omitempty"`
}

type ctxSession struct {
	Agent    string     `json:"agent"`
	Key      string     `json:"key"`
	Title    string     `json:"title,omitempty"`
	Model    string     `json:"model"`
	Source   string     `json:"source,omitempty"`
	First    time.Time  `json:"first"`
	Last     time.Time  `json:"last"`
	Requests int        `json:"requests"`
	Peak     int        `json:"peak"`
	Window   int        `json:"window"`
	Compacts int        `json:"compacts,omitempty"`
	Latest   *Prompt    `json:"latest"`
	LatestID int64      `json:"latestId"`
	Points   []ctxPoint `json:"points"`
}

type ctxScore struct {
	Key    string  `json:"key"`
	Points float64 `json:"points"`
	Most   float64 `json:"most"`
}

type ctxTag struct {
	Key   string  `json:"key"`
	Tone  string  `json:"tone"`
	Value float64 `json:"value,omitempty"`
}

type ctxAgent struct {
	Agent      string             `json:"agent"`
	Requests   int                `json:"requests"`
	Sessions   int                `json:"sessions"`
	Errors     int                `json:"errors"`
	Calls      int                `json:"calls"`
	Median     int                `json:"median"`
	P90        int                `json:"p90"`
	Baseline   int                `json:"baseline"`
	Window     int                `json:"window"`
	Cache      float64            `json:"cache"`
	Growth     int                `json:"growth"`
	PeakFill   float64            `json:"peakFill"`
	Compacts   int                `json:"compacts"`
	Shares     map[string]float64 `json:"shares"`
	MCP        int                `json:"mcp"`
	Score      int                `json:"score"`
	Scores     []ctxScore         `json:"scores"`
	Tags       []ctxTag           `json:"tags"`
	Models     []string           `json:"models"`
	LatestID   int64              `json:"latestId"`
	LatestTime time.Time          `json:"latestTime"`
	Clients    []string           `json:"clients,omitempty"`
}

type ctxReport struct {
	Days         int            `json:"days"`
	Agents       []ctxAgent     `json:"agents"`
	Sessions     []ctxSession   `json:"sessions"`
	SourceCounts map[string]int `json:"source_counts"`
	LatestRoute  *RouteRecord   `json:"latest_route,omitempty"`
}

func medianInt(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}

func quantileInt(xs []int, q float64) int {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[min(len(s)-1, int(float64(len(s))*q))]
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

func partTokens(p *Prompt, kinds ...string) int {
	if p == nil {
		return 0
	}
	n := 0
	for _, x := range p.Parts {
		if slices.Contains(kinds, x.Kind) {
			n += x.Tokens
		}
	}
	return n
}

func mcpTokens(p *Prompt) int {
	if p == nil {
		return 0
	}
	n := 0
	for _, x := range p.Parts {
		if x.Kind != PartTools {
			continue
		}
		for _, it := range x.Items {
			if it.Tag == "mcp" {
				n += it.Tokens
			}
		}
	}
	return n
}

func compacted(prev, next int) bool { return prev >= 20000 && next < prev*6/10 }

func scoreAgent(a *ctxAgent, counted bool) {
	w := float64(max(a.Window, 1))
	cache := 0.5
	if counted {
		cache = clamp01(a.Cache / 0.9)
	}
	lean := clamp01(1 - (float64(a.Baseline)/w-0.05)/0.25)
	pace := clamp01(1 - (float64(a.Growth)/w-0.01)/0.05)
	errRate := float64(a.Errors) / float64(max(1, a.Calls))
	reliable := clamp01(1 - errRate/0.2)
	a.Scores = []ctxScore{
		{Key: "cache", Points: math.Round(cache * 35), Most: 35},
		{Key: "lean", Points: math.Round(lean * 25), Most: 25},
		{Key: "pace", Points: math.Round(pace * 20), Most: 20},
		{Key: "reliable", Points: math.Round(reliable * 20), Most: 20},
	}
	score := 0.0
	for _, s := range a.Scores {
		score += s.Points
	}
	a.Score = int(score)

	tag := func(key, tone string, v float64) { a.Tags = append(a.Tags, ctxTag{Key: key, Tone: tone, Value: v}) }
	a.Tags = []ctxTag{}
	switch {
	case counted && a.Cache >= 0.8:
		tag("cache-friendly", "good", a.Cache)
	case counted && a.Cache < 0.4 && a.Requests >= 5:
		tag("cache-misses", "warn", a.Cache)
	}
	switch base := float64(a.Baseline) / w; {
	case base <= 0.06:
		tag("lean", "good", float64(a.Baseline))
	case base >= 0.2:
		tag("heavy-start", "warn", float64(a.Baseline))
	}
	if a.MCP >= 5000 && float64(a.MCP) >= 0.3*float64(a.Baseline) {
		tag("mcp-heavy", "info", float64(a.MCP))
	}
	if s := a.Shares[PartResults]; s >= 0.3 {
		tag("tool-heavy", "info", s)
	}
	if s := a.Shares[PartFiles]; s >= 0.25 {
		tag("file-reader", "info", s)
	}
	if s := a.Shares[PartMemory]; s >= 0.1 {
		tag("memory-heavy", "info", s)
	}
	if g := float64(a.Growth) / w; g >= 0.03 {
		tag("fills-fast", "warn", float64(a.Growth))
	}
	if a.PeakFill >= 0.85 {
		tag("near-full", "warn", a.PeakFill)
	}
	if a.Compacts > 0 {
		tag("compacts", "info", float64(a.Compacts))
	}
	switch {
	case a.Calls >= 20 && errRate < 0.01:
		tag("steady", "good", errRate)
	case a.Calls >= 5 && errRate >= 0.1:
		tag("flaky", "warn", errRate)
	}
}

func buildContextReport(routes []*RouteRecord, days int, onlySession string) ctxReport {
	out := ctxReport{
		Days:     days,
		Agents:   []ctxAgent{},
		Sessions: []ctxSession{},
	}
	var cutoff time.Time
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}

	filtered := make([]*RouteRecord, 0, len(routes))
	for _, r := range routes {
		if !cutoff.IsZero() && r.Source != SourceDemo && r.Source != SourceUpload && r.Time.Before(cutoff) {
			continue
		}
		filtered = append(filtered, r)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Time.Before(filtered[j].Time) })

	type agentAcc struct {
		a                       ctxAgent
		prompts, bases, growths []int
		mcps                    []int
		cached, counted         int
		parts                   map[string]int
		models                  map[string]int
		sessions                map[string]bool
		fills                   []float64
	}
	agents := map[string]*agentAcc{}
	sess := map[string]*ctxSession{}
	var order []string

	for _, r := range filtered {
		if !r.Done {
			continue
		}
		if r.Prompt != nil && r.Prompt.Tokens > 0 {
			if out.LatestRoute == nil || !r.Time.Before(out.LatestRoute.Time) {
				out.LatestRoute = r
			}
		}
		agentName := r.Agent
		if agentName == "" {
			agentName = "agent"
		}
		acc := agents[agentName]
		if acc == nil {
			acc = &agentAcc{
				a:        ctxAgent{Agent: agentName},
				parts:    map[string]int{},
				models:   map[string]int{},
				sessions: map[string]bool{},
			}
			agents[agentName] = acc
		}
		acc.a.Calls++
		if r.Status >= 400 || r.Error != "" {
			acc.a.Errors++
			continue
		}
		if r.Prompt == nil || r.Prompt.Tokens <= 0 {
			continue
		}

		p := r.Prompt
		win := p.Window
		if win <= 0 {
			win = defaultWindow
		}
		acc.a.Requests++
		acc.prompts = append(acc.prompts, p.Tokens)
		acc.bases = append(acc.bases, partTokens(p, PartSystem, PartTools, PartSkills, PartMemory))
		acc.mcps = append(acc.mcps, mcpTokens(p))
		for _, x := range p.Parts {
			acc.parts[x.Kind] += x.Tokens
		}
		var cacheRead int
		cacheKnown := false
		if len(r.Usage) > 0 {
			u := r.Usage[len(r.Usage)-1]
			cacheRead = u.CacheRead
			cacheKnown = !u.CacheUnknown
		}
		if p.Counted && cacheKnown {
			acc.counted += p.Tokens
			acc.cached += min(cacheRead, p.Tokens)
		}
		if r.Model != "" {
			acc.models[r.Model]++
		}
		if r.Time.After(acc.a.LatestTime) {
			acc.a.LatestTime, acc.a.LatestID = r.Time, r.ID
		}
		acc.a.Window = max(acc.a.Window, win)

		skey := r.Session
		if skey == "" {
			skey = "route-" + strconv.FormatInt(r.ID, 10)
		}
		acc.sessions[skey] = true
		sk := agentName + "\x00" + skey
		s := sess[sk]
		if s == nil {
			s = &ctxSession{
				Agent:  agentName,
				Key:    skey,
				Title:  r.SessionTitle,
				Source: r.Source,
				First:  r.Time,
				Points: []ctxPoint{},
			}
			sess[sk] = s
			order = append(order, sk)
		}
		if s.Title == "" && r.SessionTitle != "" {
			s.Title = r.SessionTitle
		}
		if n := len(s.Points); n > 0 {
			prev := s.Points[n-1].Tokens
			if compacted(prev, p.Tokens) {
				s.Compacts++
				acc.a.Compacts++
			} else if d := p.Tokens - prev; d > 0 {
				acc.growths = append(acc.growths, d)
			}
		}
		s.Points = append(s.Points, ctxPoint{
			ID:           r.ID,
			Time:         r.Time,
			Tokens:       p.Tokens,
			Window:       p.Window,
			Cache:        cacheRead,
			CacheUnknown: !cacheKnown,
		})
		s.Requests++
		s.Last, s.Latest, s.LatestID = r.Time, p, r.ID
		if r.Model != "" {
			s.Model = r.Model
		}
		s.Peak = max(s.Peak, p.Tokens)
		s.Window = max(s.Window, win)
	}

	slices.SortFunc(order, func(a, b string) int { return sess[b].Last.Compare(sess[a].Last) })
	for _, sk := range order {
		s := sess[sk]
		if onlySession != "" && sk != onlySession {
			continue
		}
		if acc := agents[s.Agent]; acc != nil && s.Requests > 1 {
			acc.fills = append(acc.fills, float64(s.Peak)/float64(max(s.Window, 1)))
		}
		out.Sessions = append(out.Sessions, *s)
	}

	for _, acc := range agents {
		a := &acc.a
		if a.Requests == 0 {
			continue
		}
		a.Sessions = len(acc.sessions)
		a.Median, a.P90 = medianInt(acc.prompts), quantileInt(acc.prompts, 0.9)
		a.Baseline, a.Growth, a.MCP = medianInt(acc.bases), medianInt(acc.growths), medianInt(acc.mcps)
		if acc.counted > 0 {
			a.Cache = float64(acc.cached) / float64(acc.counted)
		}
		for _, f := range acc.fills {
			a.PeakFill = math.Max(a.PeakFill, f)
		}
		total := 0
		for _, n := range acc.parts {
			total += n
		}
		a.Shares = map[string]float64{}
		for k, n := range acc.parts {
			a.Shares[k] = float64(n) / float64(max(1, total))
		}
		for m := range acc.models {
			a.Models = append(a.Models, m)
		}
		slices.SortFunc(a.Models, func(x, y string) int { return acc.models[y] - acc.models[x] })
		if len(a.Models) > 3 {
			a.Models = a.Models[:3]
		}
		scoreAgent(a, acc.counted > 0)
		out.Agents = append(out.Agents, *a)
	}

	slices.SortFunc(out.Agents, func(x, y ctxAgent) int {
		if x.Requests != y.Requests {
			return y.Requests - x.Requests
		}
		return y.LatestTime.Compare(x.LatestTime)
	})
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func resolveAssetsDir(explicit string) string {
	if explicit == "" {
		return ""
	}
	if st, err := os.Stat(filepath.Join(explicit, "context.js")); err == nil && !st.IsDir() {
		return explicit
	}
	return ""
}

type ServerConfig struct {
	Store       *Store
	Upstream    *UpstreamConfig
	AssetsDir   string
	ScanMgr     *ScanManager
	SkillsMgr   *SkillsManager
	SessionDirs []string
	IncludeHome bool
}

func wantsRedact(r *http.Request) bool {
	v := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("redact")))
	return v != "0" && v != "false" && v != "no"
}

func redactSession(s *ctxSession) {
	if s == nil {
		return
	}
	s.Title = redactText(s.Title)
	s.Latest = redactPrompt(s.Latest)
}

func redactContextReport(report *ctxReport) {
	if report == nil {
		return
	}
	for i := range report.Sessions {
		redactSession(&report.Sessions[i])
	}
	report.LatestRoute = redactRouteRecord(report.LatestRoute)
}

func redactHomePrefix(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	cleanHome := filepath.Clean(home)
	cleanP := filepath.Clean(p)
	if cleanP == cleanHome {
		return "~"
	}
	sep := string(os.PathSeparator)
	if strings.HasPrefix(cleanP, cleanHome+sep) {
		return "~/" + filepath.ToSlash(cleanP[len(cleanHome)+1:])
	}
	return p
}

func distributeEntryTokens(items []PromptEntry, indices []int, budget int) {
	if len(indices) == 0 || budget <= 0 {
		return
	}
	weightSum := 0
	for _, idx := range indices {
		if items[idx].Tokens > 0 {
			weightSum += items[idx].Tokens
		}
	}
	assigned := 0
	for k, idx := range indices {
		var share int
		if k == len(indices)-1 {
			share = budget - assigned
		} else if weightSum > 0 && items[idx].Tokens > 0 {
			share = int(int64(budget) * int64(items[idx].Tokens) / int64(weightSum))
		} else {
			share = budget / len(indices)
		}
		if share < 0 {
			share = 0
		}
		items[idx].Tokens = share
		assigned += share
	}
}

func alignPromptTextTokens(pt *PromptText, calibrated *Prompt, redacted bool) {
	if pt == nil || calibrated == nil || len(pt.Items) == 0 {
		return
	}
	var part *PromptPart
	for i := range calibrated.Parts {
		if calibrated.Parts[i].Kind == pt.Kind {
			part = &calibrated.Parts[i]
			break
		}
	}
	if part == nil || part.Tokens <= 0 {
		return
	}

	used := make([]bool, len(part.Items))
	matchedSum := 0
	var unmatched []int

	for i := range pt.Items {
		name := pt.Items[i].Name
		matchIdx := -1
		for j, pItem := range part.Items {
			if used[j] || pItem.Name == "" || pItem.Name == "remainder" {
				continue
			}
			if pItem.Name == name || (redacted && redactText(pItem.Name) == name) {
				matchIdx = j
				break
			}
		}
		if matchIdx >= 0 {
			used[matchIdx] = true
			pt.Items[i].Tokens = part.Items[matchIdx].Tokens
			matchedSum += pt.Items[i].Tokens
		} else {
			unmatched = append(unmatched, i)
		}
	}

	rem := part.Tokens - matchedSum
	if len(unmatched) > 0 {
		if rem > 0 {
			distributeEntryTokens(pt.Items, unmatched, rem)
		} else if matchedSum == 0 {
			distributeEntryTokens(pt.Items, unmatched, part.Tokens)
		}
		return
	}
	if rem > 0 {
		all := make([]int, len(pt.Items))
		for i := range pt.Items {
			all[i] = i
		}
		distributeEntryTokens(pt.Items, all, part.Tokens)
	}
}

func NewHandler(cfg ServerConfig) http.Handler {
	mux := http.NewServeMux()
	store := cfg.Store
	assetsDir := resolveAssetsDir(cfg.AssetsDir)
	scanMgr := cfg.ScanMgr
	if scanMgr == nil {
		scanMgr = NewScanManager(store, "", cfg.SessionDirs, cfg.IncludeHome)
	}
	skillsMgr := cfg.SkillsMgr
	if skillsMgr == nil {
		dataDir := ""
		if store != nil {
			dataDir = store.dataDir
		}
		skillsMgr = NewSkillsManager(dataDir, "")
	}

	// --- API routes ---

	mux.HandleFunc("GET /api/context", func(w http.ResponseWriter, r *http.Request) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days <= 0 {
			days = 7
		}
		source := r.URL.Query().Get("source")
		if source == "" {
			source = "all"
		}
		routes := store.List(source)
		report := buildContextReport(routes, days, "")
		report.SourceCounts = store.Counts()
		if wantsRedact(r) {
			redactContextReport(&report)
		}
		writeJSON(w, http.StatusOK, report)
	})

	mux.HandleFunc("GET /api/context/session", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		agent, id := q.Get("agent"), q.Get("id")
		if agent == "" || id == "" {
			http.Error(w, "agent and id are needed", http.StatusBadRequest)
			return
		}
		source := q.Get("source")
		if source == "" {
			source = "all"
		}
		report := buildContextReport(store.List(source), 0, agent+"\x00"+id)
		var sess *ctxSession
		if len(report.Sessions) > 0 {
			if wantsRedact(r) {
				redactSession(&report.Sessions[0])
			}
			sess = &report.Sessions[0]
		}
		writeJSON(w, http.StatusOK, map[string]any{"session": sess})
	})

	mux.HandleFunc("GET /api/context/text", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, err := strconv.ParseInt(q.Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid route id", http.StatusBadRequest)
			return
		}
		kind := q.Get("kind")
		item := -1
		if v := q.Get("item"); v != "" {
			if item, err = strconv.Atoi(v); err != nil || item < 0 {
				http.Error(w, "invalid item", http.StatusBadRequest)
				return
			}
		}
		rec, ok := store.Get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"archive": "missing"})
			return
		}
		reqBody, _, hasBody := store.Bodies(id)
		if !hasBody || len(reqBody) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"archive": "off"})
			return
		}
		redacted := wantsRedact(r)
		if redacted {
			reqBody = redactJSONBytes(reqBody)
		}
		tokens := 0
		if rec.Prompt != nil {
			tokens = rec.Prompt.Tokens
		}
		pt, ok := promptTextOf(Protocol(rec.Protocol), reqBody, id, kind, item >= 0, tokens)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"archive": "unread"})
			return
		}
		alignPromptTextTokens(&pt, rec.Prompt, redacted)
		if item >= 0 {
			if item >= len(pt.Items) {
				http.Error(w, "invalid item", http.StatusBadRequest)
				return
			}
			for i := range pt.Items {
				if i != item {
					pt.Items[i].Pieces = nil
				}
			}
			if pt.Items[item].Pieces == nil {
				pt.Items[item].Pieces = []PromptPiece{}
			}
		}
		if redacted {
			pt = redactPromptText(pt)
			if item >= 0 && pt.Items[item].Pieces == nil {
				pt.Items[item].Pieces = []PromptPiece{}
			}
		}
		writeJSON(w, http.StatusOK, pt)
	})

	mux.HandleFunc("GET /api/context/reply", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, err := strconv.ParseInt(q.Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid route id", http.StatusBadRequest)
			return
		}
		rec, ok := store.Get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"archive": "missing"})
			return
		}
		_, respBody, hasBody := store.Bodies(id)
		if !hasBody || len(respBody) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"archive": "off"})
			return
		}
		redacted := wantsRedact(r)
		if redacted {
			respBody = redactJSONBytes(respBody)
		}
		pieces, ok := replyPieces(Protocol(rec.Protocol), respBody)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"archive": "unread"})
			return
		}
		pr := PromptReply{
			ID:     id,
			Pieces: pieces,
			From:   "memory",
		}
		if redacted {
			pr = redactPromptReply(pr)
		}
		writeJSON(w, http.StatusOK, pr)
	})

	mux.HandleFunc("GET /api/route", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid route id", http.StatusBadRequest)
			return
		}
		rec, ok := store.Get(id)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if wantsRedact(r) {
			rec = redactRouteRecord(rec)
		}
		writeJSON(w, http.StatusOK, rec)
	})

	mux.HandleFunc("GET /api/routes", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		source := q.Get("source")
		if source == "" {
			source = "all"
		}
		session := q.Get("session")
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		redacted := wantsRedact(r)
		list := store.List(source)
		var out []*RouteRecord
		for i := len(list) - 1; i >= 0; i-- {
			rec := list[i]
			if session != "" && rec.Session != session {
				continue
			}
			if redacted {
				rec = redactRouteRecord(rec)
			}
			out = append(out, rec)
			if len(out) >= limit {
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"routes": out,
			"counts": store.Counts(),
		})
	})

	scanStatusPayload := func(redacted bool, local int, added []string) map[string]any {
		defDirs, custDirs, includeHome := scanMgr.Status(redacted)
		if defDirs == nil {
			defDirs = []ScanDirStatus{}
		}
		if custDirs == nil {
			custDirs = []ScanDirStatus{}
		}
		if redacted {
			for i := range defDirs {
				defDirs[i].Path = redactHomePrefix(defDirs[i].Path)
			}
			for i := range custDirs {
				custDirs[i].Path = redactHomePrefix(custDirs[i].Path)
			}
			for i := range added {
				added[i] = redactHomePrefix(added[i])
			}
		}
		payload := map[string]any{
			"ok":           true,
			"include_home": includeHome,
			"default_dirs": defDirs,
			"custom_dirs":  custDirs,
			"counts":       store.Counts(),
		}
		if local >= 0 {
			payload["local"] = local
		}
		if added != nil {
			payload["added"] = added
		}
		return payload
	}

	mux.HandleFunc("GET /api/scan-dirs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, scanStatusPayload(wantsRedact(r), -1, nil))
	})

	mux.HandleFunc("POST /api/scan-dirs", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action      string   `json:"action"`
			Paths       []string `json:"paths"`
			Path        string   `json:"path"`
			IncludeHome *bool    `json:"include_home"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
				return
			}
		}
		var (
			local int
			added []string
		)
		switch req.Action {
		case "add":
			var paths []string
			paths = append(paths, req.Paths...)
			if p := strings.TrimSpace(req.Path); p != "" {
				paths = append(paths, p)
			}
			added, local, err = scanMgr.AddDirs(paths)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if added == nil {
				added = []string{}
			}
		case "remove":
			local, err = scanMgr.RemoveDir(req.Path)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		case "toggle_home":
			_, _, curHome := scanMgr.Status(false)
			nextHome := !curHome
			if req.IncludeHome != nil {
				nextHome = *req.IncludeHome
			}
			local, err = scanMgr.SetIncludeHome(nextHome)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		case "rescan", "":
			local = scanMgr.Scan()
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + req.Action})
			return
		}
		writeJSON(w, http.StatusOK, scanStatusPayload(wantsRedact(r), local, added))
	})

	mux.HandleFunc("POST /api/rescan", func(w http.ResponseWriter, r *http.Request) {
		n := scanMgr.Scan()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"local":  n,
			"counts": store.Counts(),
		})
	})

	mux.HandleFunc("POST /api/upload", handleUpload(store))

	mux.HandleFunc("GET /api/tune", func(w http.ResponseWriter, r *http.Request) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days <= 0 {
			days = 7
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"days":   days,
			"agents": []any{},
		})
	})

	// --- Global Agent Skills Workspace Endpoints ---
	registerSkillsRoutes(mux, skillsMgr)

	// --- Reverse Proxy Endpoints (Scheme 1) ---

	if cfg.Upstream != nil {
		proxyHandler := cfg.Upstream.handler(store)
		mux.HandleFunc("/v1/messages", proxyHandler)
		mux.HandleFunc("/v1/responses", proxyHandler)
		mux.HandleFunc("/v1/chat/completions", proxyHandler)
		mux.HandleFunc("/v1beta/", proxyHandler)
		mux.HandleFunc("/proxy/", proxyHandler)
	}

	// --- Static Assets from embedded web/vendor (or explicit cfg.AssetsDir override) + embedded web/ ---

	vendorNames := []string{"app.css", "context.css", "i18n.js", "context.js", "ctxtext.js"}
	if assetsDir != "" {
		serveAsset := func(name string) http.HandlerFunc {
			p := filepath.Join(assetsDir, name)
			return func(w http.ResponseWriter, r *http.Request) {
				http.ServeFile(w, r, p)
			}
		}
		for _, name := range vendorNames {
			mux.HandleFunc("GET /"+name, serveAsset(name))
		}
		mux.Handle("GET /icons/", http.StripPrefix("/icons/", http.FileServer(http.Dir(filepath.Join(assetsDir, "icons")))))
	} else if vendorSub, err := fs.Sub(webFS, "web/vendor"); err == nil {
		vendorSrv := http.FileServer(http.FS(vendorSub))
		for _, name := range vendorNames {
			mux.Handle("GET /"+name, vendorSrv)
		}
		mux.Handle("GET /icons/", vendorSrv)
	}

	webSub, err := fs.Sub(webFS, "web")
	if err == nil {
		webSrv := http.FileServer(http.FS(webSub))
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			data, err := fs.ReadFile(webSub, "index.html")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
		})
		mux.Handle("GET /host.js", webSrv)
		mux.Handle("GET /viewer.css", webSrv)
		mux.Handle("GET /skills.js", webSrv)
		mux.Handle("GET /skills.css", webSrv)
	}

	return mux
}

func decodeSkillsJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return http.ErrBodyReadAfterClose
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	return dec.Decode(dst)
}

func redactCenterSkillForHTTP(m *SkillsManager, sk *CenterSkill, redact bool) *CenterSkill {
	if sk == nil {
		return nil
	}
	cp := *sk
	if cp.Tags == nil {
		cp.Tags = []string{}
	}
	if redact {
		cp.SourceRef = m.redactWithHome(cp.SourceRef, true)
		cp.CentralPath = m.redactWithHome(cp.CentralPath, true)
	}
	return &cp
}

func redactSyncResultForHTTP(m *SkillsManager, res SyncBatchResult, redact bool) SyncBatchResult {
	if !redact {
		return res
	}
	for i := range res.Conflicts {
		res.Conflicts[i].Path = m.redactWithHome(res.Conflicts[i].Path, true)
	}
	homeDir, _ := m.dirs()
	for i := range res.Failures {
		if homeDir != "" && homeDir != "." {
			res.Failures[i].Error = strings.ReplaceAll(res.Failures[i].Error, homeDir, "~")
		}
		res.Failures[i].Error = replaceHomePaths(res.Failures[i].Error)
	}
	return res
}

func writeSkillsErrHTTP(w http.ResponseWriter, r *http.Request, m *SkillsManager, err error) {
	if !wantsRedact(r) {
		writeSkillsError(w, err)
		return
	}
	homeDir, _ := m.dirs()
	redactStr := func(s string) string {
		if s == "" {
			return s
		}
		if homeDir != "" && homeDir != "." {
			s = strings.ReplaceAll(s, homeDir, "~")
		}
		return replaceHomePaths(s)
	}
	var redacted []TargetConflictDTO
	if ref, ok := isReplaceRefused(err); ok {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": redactStr(ref.Error()),
			"code":  "TARGET_CONFLICT",
			"conflicts": []TargetConflictDTO{{
				Path:   m.redactWithHome(ref.Target, true),
				Reason: redactStr(ref.Reason),
			}},
		})
		return
	}
	if apiErr, ok := err.(*SkillsAPIError); ok {
		for _, c := range apiErr.Conflicts {
			redacted = append(redacted, TargetConflictDTO{
				Path:   m.redactWithHome(c.Path, true),
				Reason: redactStr(c.Reason),
			})
		}
		cp := *apiErr
		cp.Message = redactStr(cp.Message)
		cp.Conflicts = redacted
		writeSkillsError(w, &cp)
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": redactStr(err.Error())})
}

func registerSkillsRoutes(mux *http.ServeMux, m *SkillsManager) {
	mux.HandleFunc("GET /api/skills/agents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.ListAgents(wantsRedact(r)))
	})

	mux.HandleFunc("GET /api/skills/agents/{agent}/skills", func(w http.ResponseWriter, r *http.Request) {
		agent := r.PathValue("agent")
		skills, err := m.GetAgentSkills(agent, wantsRedact(r))
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		if skills == nil {
			skills = []AgentSkill{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent":  agent,
			"skills": skills,
		})
	})

	mux.HandleFunc("GET /api/skills/agents/{agent}/document", func(w http.ResponseWriter, r *http.Request) {
		agent := r.PathValue("agent")
		relPath := strings.TrimSpace(r.URL.Query().Get("rel_path"))
		if relPath == "" {
			relPath = strings.TrimSpace(r.URL.Query().Get("path"))
		}
		doc, err := m.GetAgentSkillDocument(agent, relPath, wantsRedact(r))
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, doc)
	})

	mux.HandleFunc("POST /api/skills/agents/{agent}/import", func(w http.ResponseWriter, r *http.Request) {
		agent := r.PathValue("agent")
		var req struct {
			RelPath string `json:"rel_path"`
			Path    string `json:"path"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		relPath := req.RelPath
		if relPath == "" {
			relPath = req.Path
		}
		sk, err := m.ImportAgentLocalSkillToCenter(agent, relPath)
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, redactCenterSkillForHTTP(m, sk, wantsRedact(r)))
	})

	mux.HandleFunc("POST /api/skills/agents/{agent}/pull", func(w http.ResponseWriter, r *http.Request) {
		agent := r.PathValue("agent")
		var req struct {
			RelPath string `json:"rel_path"`
			Path    string `json:"path"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		relPath := req.RelPath
		if relPath == "" {
			relPath = req.Path
		}
		if err := m.PullAgentLocalSkillFromCenter(agent, relPath); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/skills/agents/{agent}/delete-local", func(w http.ResponseWriter, r *http.Request) {
		agent := r.PathValue("agent")
		var req struct {
			RelPath string `json:"rel_path"`
			Path    string `json:"path"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		relPath := req.RelPath
		if relPath == "" {
			relPath = req.Path
		}
		if err := m.DeleteAgentLocalSkill(agent, relPath); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/skills/sync", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SkillIDs []string `json:"skill_ids"`
			Agents   []string `json:"agents"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		if len(req.SkillIDs) == 0 || len(req.Agents) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "skill_ids and agents are required", "code": "INVALID_INPUT"})
			return
		}
		res, err := m.SyncSkills(req.SkillIDs, req.Agents)
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, redactSyncResultForHTTP(m, res, wantsRedact(r)))
	})

	mux.HandleFunc("POST /api/skills/unsync", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SkillIDs []string `json:"skill_ids"`
			Agents   []string `json:"agents"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		if len(req.SkillIDs) == 0 || len(req.Agents) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "skill_ids and agents are required", "code": "INVALID_INPUT"})
			return
		}
		res, err := m.UnsyncSkills(req.SkillIDs, req.Agents)
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, redactSyncResultForHTTP(m, res, wantsRedact(r)))
	})

	mux.HandleFunc("GET /api/skills/center", func(w http.ResponseWriter, r *http.Request) {
		skills := m.ListCenterSkills(wantsRedact(r))
		if skills == nil {
			skills = []CenterSkillDTO{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"skills": skills})
	})

	mux.HandleFunc("GET /api/skills/center/{id}/document", func(w http.ResponseWriter, r *http.Request) {
		doc, err := m.GetCenterSkillDocument(r.PathValue("id"), wantsRedact(r))
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, doc)
	})

	mux.HandleFunc("POST /api/skills/center/import-path", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		sk, err := m.ImportPathToCenter(req.Path)
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, redactCenterSkillForHTTP(m, sk, wantsRedact(r)))
	})

	mux.HandleFunc("POST /api/skills/center/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tags []string `json:"tags"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		if err := m.SetSkillTags(r.PathValue("id"), req.Tags); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("DELETE /api/skills/center/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.DeleteCenterSkill(r.PathValue("id")); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/skills/presets", func(w http.ResponseWriter, r *http.Request) {
		presets := m.ListPresets()
		if presets == nil {
			presets = []SkillPreset{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"presets": presets})
	})

	mux.HandleFunc("POST /api/skills/presets", func(w http.ResponseWriter, r *http.Request) {
		var req SavePresetReq
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		p, err := m.SavePreset(req)
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("DELETE /api/skills/presets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.DeletePreset(r.PathValue("id")); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/skills/settings", func(w http.ResponseWriter, r *http.Request) {
		var req SkillsSettingsReq
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		if err := m.UpdateSettings(req); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// --- Project & Linked Workspace Endpoints ---
	mux.HandleFunc("GET /api/skills/projects", func(w http.ResponseWriter, r *http.Request) {
		projects := m.ListProjects(wantsRedact(r))
		if projects == nil {
			projects = []ProjectDTO{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
	})

	mux.HandleFunc("POST /api/skills/projects", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		p, err := m.AddProject(req.Path, wantsRedact(r))
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("POST /api/skills/projects/linked", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name         string `json:"name"`
			Path         string `json:"path"`
			DisabledPath string `json:"disabled_path"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		p, err := m.AddLinkedWorkspace(req.Name, req.Path, req.DisabledPath, wantsRedact(r))
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("POST /api/skills/projects/scan", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Root string `json:"root"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		items, err := m.ScanProjects(req.Root, wantsRedact(r))
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		if items == nil {
			items = []ScanProjectItem{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": items})
	})

	mux.HandleFunc("POST /api/skills/projects/reorder", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		if err := m.ReorderProjects(req.IDs); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("DELETE /api/skills/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.RemoveProject(r.PathValue("id")); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/skills/projects/{id}/targets", func(w http.ResponseWriter, r *http.Request) {
		targets, err := m.GetProjectAgentTargets(r.PathValue("id"))
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		if targets == nil {
			targets = []ProjectAgentTargetDTO{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
	})

	mux.HandleFunc("GET /api/skills/projects/{id}/skills", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		skills, err := m.GetProjectSkills(id, wantsRedact(r))
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		if skills == nil {
			skills = []AgentSkill{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"project_id": id,
			"skills":     skills,
		})
	})

	mux.HandleFunc("GET /api/skills/projects/{id}/document", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		relPath := strings.TrimSpace(r.URL.Query().Get("rel_path"))
		if relPath == "" {
			relPath = strings.TrimSpace(r.URL.Query().Get("path"))
		}
		agent := strings.TrimSpace(r.URL.Query().Get("agent"))
		doc, err := m.GetProjectSkillDocument(id, relPath, agent, wantsRedact(r))
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, doc)
	})

	mux.HandleFunc("POST /api/skills/projects/{id}/export", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req struct {
			SkillIDs []string `json:"skill_ids"`
			Agents   []string `json:"agents"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		if len(req.SkillIDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "skill_ids are required", "code": "INVALID_INPUT"})
			return
		}
		res, err := m.ExportSkillsToProject(id, req.SkillIDs, req.Agents)
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, redactSyncResultForHTTP(m, res, wantsRedact(r)))
	})

	mux.HandleFunc("POST /api/skills/projects/{id}/import", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req struct {
			RelPath string `json:"rel_path"`
			Path    string `json:"path"`
			Agent   string `json:"agent"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		relPath := req.RelPath
		if relPath == "" {
			relPath = req.Path
		}
		sk, err := m.ImportProjectSkillToCenter(id, relPath, req.Agent)
		if err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, redactCenterSkillForHTTP(m, sk, wantsRedact(r)))
	})

	mux.HandleFunc("POST /api/skills/projects/{id}/pull", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req struct {
			RelPath string `json:"rel_path"`
			Path    string `json:"path"`
			Agent   string `json:"agent"`
			Force   bool   `json:"force"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		relPath := req.RelPath
		if relPath == "" {
			relPath = req.Path
		}
		if err := m.UpdateProjectSkillFromCenter(id, relPath, req.Agent, req.Force); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/skills/projects/{id}/toggle", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req struct {
			RelPath string `json:"rel_path"`
			Path    string `json:"path"`
			Agent   string `json:"agent"`
			Enabled bool   `json:"enabled"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		relPath := req.RelPath
		if relPath == "" {
			relPath = req.Path
		}
		if err := m.ToggleProjectSkill(id, relPath, req.Agent, req.Enabled); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/skills/projects/{id}/delete-skill", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req struct {
			RelPath string `json:"rel_path"`
			Path    string `json:"path"`
			Agent   string `json:"agent"`
		}
		if err := decodeSkillsJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json request body", "code": "BAD_REQUEST"})
			return
		}
		relPath := req.RelPath
		if relPath == "" {
			relPath = req.Path
		}
		if err := m.DeleteProjectSkill(id, relPath, req.Agent); err != nil {
			writeSkillsErrHTTP(w, r, m, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}

func (cfg *UpstreamConfig) handler(store *Store) http.HandlerFunc {
	return store.handleProxy(cfg)
}

func handleUpload(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			rawBytes   []byte
			fileName   string
			protoHint  string
			modelHint  string
			agentHint  string
			titleHint  string
			windowHint int
			inHint     int
			crHint     int
			cwHint     int
		)

		ct := strings.ToLower(r.Header.Get("Content-Type"))
		if strings.HasPrefix(ct, "multipart/form-data") {
			if err := r.ParseMultipartForm(64 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f, hdr, err := r.FormFile("file")
			if err != nil {
				http.Error(w, "missing file field", http.StatusBadRequest)
				return
			}
			defer f.Close()
			fileName = hdr.Filename
			rawBytes, err = io.ReadAll(io.LimitReader(f, 64<<20))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			modelHint = r.FormValue("model")
			protoHint = r.FormValue("protocol")
			agentHint = r.FormValue("agent")
			titleHint = r.FormValue("title")
			windowHint, _ = strconv.Atoi(r.FormValue("window"))
			inHint, _ = strconv.Atoi(r.FormValue("input"))
			crHint, _ = strconv.Atoi(r.FormValue("cache_read"))
			cwHint, _ = strconv.Atoi(r.FormValue("cache_write"))
		} else {
			body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var env struct {
				FilePath   string `json:"file_path"`
				Raw        string `json:"raw"`
				Filename   string `json:"filename"`
				Protocol   string `json:"protocol"`
				Model      string `json:"model"`
				Agent      string `json:"agent"`
				Title      string `json:"title"`
				Window     int    `json:"window"`
				Input      int    `json:"input"`
				CacheRead  int    `json:"cache_read"`
				CacheWrite int    `json:"cache_write"`
			}
			if json.Unmarshal(body, &env) == nil && (env.FilePath != "" || env.Raw != "") {
				fileName = env.Filename
				protoHint = env.Protocol
				modelHint = env.Model
				agentHint = env.Agent
				titleHint = env.Title
				windowHint = env.Window
				inHint = env.Input
				crHint = env.CacheRead
				cwHint = env.CacheWrite
				if env.FilePath != "" {
					p := env.FilePath
					if strings.HasPrefix(p, "~/") {
						if home, _ := os.UserHomeDir(); home != "" {
							p = filepath.Join(home, p[2:])
						}
					}
					data, err := os.ReadFile(p)
					if err != nil {
						http.Error(w, "cannot read file: "+err.Error(), http.StatusBadRequest)
						return
					}
					rawBytes = data
					if fileName == "" {
						fileName = filepath.Base(p)
					}
				} else {
					rawBytes = []byte(env.Raw)
				}
			} else {
				rawBytes = body
			}
		}

		trim := bytes.TrimSpace(rawBytes)
		if len(trim) == 0 {
			http.Error(w, "empty payload", http.StatusBadRequest)
			return
		}

		// Try single JSON object first.
		if trim[0] == '{' && json.Valid(trim) {
			// Check if it is an envelope containing request + response (e.g. from trace/HAR).
			var wrap struct {
				Request  json.RawMessage `json:"request"`
				Response json.RawMessage `json:"response"`
				Body     json.RawMessage `json:"body"`
			}
			var respBytes []byte
			if json.Unmarshal(trim, &wrap) == nil {
				if len(wrap.Request) > 0 && bytes.TrimSpace(wrap.Request)[0] == '{' {
					trim = wrap.Request
					respBytes = wrap.Response
				} else if len(wrap.Body) > 0 && bytes.TrimSpace(wrap.Body)[0] == '{' {
					trim = wrap.Body
					respBytes = wrap.Response
				}
			}

			proto, detectedModel := detectProtocolAndModel(trim, "")
			if protoHint != "" && protoHint != "auto" {
				proto = Protocol(protoHint)
			}
			if modelHint != "" {
				detectedModel = modelHint
			}
			p := promptOf(proto, trim)
			if p == nil {
				// Might be a single-line JSONL session record; fall through to JSONL if empty.
				recs := parseSessionBytes(rawBytes, fileName, "auto", SourceUpload)
				if len(recs) > 0 {
					var last *RouteRecord
					for _, rec := range recs {
						last = store.Add(rec, rec.reqBody, rec.respBody, true)
					}
					writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(recs), "route": last, "counts": store.Counts()})
					return
				}
				http.Error(w, "unable to parse request JSON as Anthropic, OpenAI Responses, Chat Completions, or Gemini format", http.StatusBadRequest)
				return
			}

			win := windowHint
			if win <= 0 {
				win = modelWindow(detectedModel)
			}
			p.Window = win

			freshIn, outTok, cr, cw, cacheUnknown := inHint, 0, crHint, cwHint, true
			if len(respBytes) > 0 && freshIn == 0 && cr == 0 && cw == 0 {
				freshIn, outTok, cr, cw, cacheUnknown = extractUsageFromResponse(proto, respBytes)
			}
			if freshIn > 0 || cr > 0 || cw > 0 {
				cacheUnknown = false
				p.calibrate(freshIn+cr+cw, win)
			}

			agent := agentHint
			if agent == "" {
				agent = "imported"
			}
			title := titleHint
			if title == "" {
				if fileName != "" {
					title = fileName
				} else {
					title = "Imported Request"
				}
			}

			rec := &RouteRecord{
				Source:       SourceUpload,
				Time:         time.Now(),
				Done:         true,
				Agent:        agent,
				Client:       "Manual Import",
				Provider:     string(proto),
				Protocol:     string(proto),
				Model:        detectedModel,
				Session:      "import-" + time.Now().Format("20060102-150405"),
				SessionTitle: title,
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
				Prompt: p,
				Status: 200,
			}
			store.Add(rec, trim, respBytes, true)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":     true,
				"count":  1,
				"route":  rec,
				"counts": store.Counts(),
			})
			return
		}

		// Multi-line JSONL (Claude Code, Codex, JoyCode, or exported route records).
		recs := parseSessionBytes(rawBytes, fileName, "auto", SourceUpload)
		if len(recs) == 0 {
			http.Error(w, "no valid requests or session turns found in uploaded data", http.StatusBadRequest)
			return
		}
		var last *RouteRecord
		for _, rec := range recs {
			if titleHint != "" {
				rec.SessionTitle = titleHint
			}
			req, resp, _ := rec.reqBody, rec.respBody, true
			if len(req) == 0 && rec.bodyLoader != nil {
				req, resp, _ = rec.bodyLoader()
			}
			last = store.Add(rec, req, resp, true)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"count":  len(recs),
			"route":  last,
			"counts": store.Counts(),
		})
	}
}


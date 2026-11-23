package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var version = "dev"

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func splitAddr(raw string) (host, port string) {
	raw = strings.TrimSpace(raw)
	if isAllDigits(raw) {
		return "", raw
	}
	if strings.HasPrefix(raw, ":") {
		if p := strings.TrimSpace(raw[1:]); p != "" {
			return "", p
		}
		return "", "8787"
	}
	if h, p, err := net.SplitHostPort(raw); err == nil {
		if p == "" {
			p = "8787"
		}
		return h, p
	}
	return "", "8787"
}

func resolveListenAddr(addrFlag string, addrSet bool, hostFlag string, portFlag int, getenv func(string) string) string {
	get := getenv
	if get == nil {
		get = func(string) string { return "" }
	}
	host, port := "", "8787"
	if envAddr := strings.TrimSpace(get("CTXVIEW_ADDR")); envAddr != "" {
		host, port = splitAddr(envAddr)
	}
	if envPort := strings.TrimSpace(get("CTXVIEW_PORT")); envPort != "" {
		port = envPort
	} else if envPort := strings.TrimSpace(get("PORT")); envPort != "" {
		port = envPort
	}
	if envHost := strings.TrimSpace(get("CTXVIEW_HOST")); envHost != "" {
		host = envHost
	}
	if addrSet || (addrFlag != "" && addrFlag != ":8787") {
		host, port = splitAddr(addrFlag)
	}
	if h := strings.TrimSpace(hostFlag); h != "" {
		host = h
	}
	if portFlag > 0 {
		port = strconv.Itoa(portFlag)
	}
	if host == "" {
		return ":" + port
	}
	return net.JoinHostPort(host, port)
}

func defaultDataDir() string {
	if _, err := os.Stat(".ctxview-data"); err == nil {
		return ".ctxview-data"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".ctxview", "data")
	}
	return ".ctxview-data"
}

func openBrowser(rawURL string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	_ = cmd.Start()
}

func main() {
	addr := flag.String("addr", ":8787", "HTTP listen address (e.g. :8787, 127.0.0.1:9000, or 9000)")
	port := flag.Int("port", 0, "HTTP listen port (shorthand -p; overrides port in -addr or env)")
	flag.IntVar(port, "p", 0, "HTTP listen port (shorthand for -port)")
	host := flag.String("host", "", "HTTP listen host/IP (e.g. 127.0.0.1 or 0.0.0.0)")
	dataDir := flag.String("data-dir", defaultDataDir(), "Directory for captured proxy/upload JSONL history and bodies")
	sessionDir := flag.String("session-dir", "", "Comma-separated extra session directories or .jsonl files to scan")
	skillsDir := flag.String("skills-dir", "", "Override central skills repository directory (defaults to <data-dir>/skills)")
	anthropicUp := flag.String("anthropic-upstream", "https://api.anthropic.com", "Default upstream URL for /v1/messages")
	openaiUp := flag.String("openai-upstream", "https://api.openai.com", "Default upstream URL for /v1/responses and /v1/chat/completions")
	geminiUp := flag.String("gemini-upstream", "https://generativelanguage.googleapis.com", "Default upstream URL for /v1beta/")
	noDemo := flag.Bool("no-demo", false, "Disable seeded demo session")
	openFlag := flag.Bool("open", false, "Automatically open browser on startup")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	addrSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			addrSet = true
		}
	})
	listenAddr := resolveListenAddr(*addr, addrSet, *host, *port, os.Getenv)

	store, err := NewStore(*dataDir)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}

	var extraDirs []string
	for _, p := range strings.Split(*sessionDir, ",") {
		if p = strings.TrimSpace(p); p != "" {
			extraDirs = append(extraDirs, p)
		}
	}

	scanMgr := NewScanManager(store, *dataDir, extraDirs, true)
	localCount := scanMgr.Scan()
	if !*noDemo {
		seedDemoRecords(store)
	}
	skillsMgr := NewSkillsManager(*dataDir, *skillsDir)

	handler := NewHandler(ServerConfig{
		Store: store,
		Upstream: &UpstreamConfig{
			Anthropic: *anthropicUp,
			OpenAI:    *openaiUp,
			Gemini:    *geminiUp,
		},
		ScanMgr:     scanMgr,
		SkillsMgr:   skillsMgr,
		SessionDirs: extraDirs,
		IncludeHome: true,
	})

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", listenAddr, err)
	}

	counts := store.Counts()
	listen := ln.Addr().String()
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		if tcpAddr.IP == nil || tcpAddr.IP.IsUnspecified() {
			listen = net.JoinHostPort("127.0.0.1", strconv.Itoa(tcpAddr.Port))
		}
	}
	fmt.Printf("ctxview %s listening on http://%s\n", version, listen)
	fmt.Printf("  Data dir: %s\n", *dataDir)
	fmt.Printf("  Skills dir: %s (center skills=%d)\n", skillsMgr.centralDir, len(skillsMgr.ListCenterSkills(false)))
	fmt.Printf("  Loaded records: total=%d (local=%d, proxy=%d, upload=%d, demo=%d)\n",
		counts["all"], localCount, counts[SourceProxy], counts[SourceUpload], counts[SourceDemo])
	fmt.Printf("  Proxy endpoints:\n")
	fmt.Printf("    ANTHROPIC_BASE_URL=http://%s\n", listen)
	fmt.Printf("    OPENAI_BASE_URL=http://%s/v1\n", listen)
	fmt.Printf("    GEMINI_API_BASE=http://%s\n", listen)

	if *openFlag {
		go openBrowser("http://" + listen)
	}

	log.Fatal(http.Serve(ln, handler))
}


package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Iapa/ctxview/internal/redact"
)

var allRedactOptions = buildAllRedactOptions()

func currentHomeAndSlug() (home, slug string) {
	home, _ = os.UserHomeDir()
	home = filepath.Clean(home)
	if home == "" || home == "." {
		return "", ""
	}
	return home, makeClaudeHomeSlug(home)
}

func makeClaudeHomeSlug(home string) string {
	if home == "" {
		return ""
	}
	// Claude stores project folders as "-Users-foo-bar" replacing '/' and '.' with '-'
	slug := strings.NewReplacer("/", "-", ".", "-", "\\", "-").Replace(home)
	return slug + "-"
}

func buildAllRedactOptions() redact.Options {
	kinds := make(map[string]bool, len(redact.Categories))
	for _, c := range redact.Categories {
		kinds[c.ID] = true
	}
	return redact.Options{
		Secrets:  true,
		Personal: true,
		Kinds:    kinds,
	}
}

func replaceHomePaths(s string) string {
	if s == "" {
		return s
	}
	home, claudeSlug := currentHomeAndSlug()
	if home != "" && len(home) > 1 {
		s = strings.ReplaceAll(s, home, "~")
		slashed := filepath.ToSlash(home)
		if slashed != home {
			s = strings.ReplaceAll(s, slashed, "~")
		}
		escaped := strings.ReplaceAll(home, `\`, `\\`)
		if escaped != home {
			s = strings.ReplaceAll(s, escaped, "~")
		}
	}
	if claudeSlug != "" {
		s = strings.ReplaceAll(s, claudeSlug, "~-")
	}
	return s
}

func replaceHomePathsBytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	home, claudeSlug := currentHomeAndSlug()
	if home != "" && len(home) > 1 {
		b = bytes.ReplaceAll(b, []byte(home), []byte("~"))
		slashed := filepath.ToSlash(home)
		if slashed != home {
			b = bytes.ReplaceAll(b, []byte(slashed), []byte("~"))
		}
		escaped := strings.ReplaceAll(home, `\`, `\\`)
		if escaped != home {
			b = bytes.ReplaceAll(b, []byte(escaped), []byte("~"))
		}
	}
	if claudeSlug != "" {
		b = bytes.ReplaceAll(b, []byte(claudeSlug), []byte("~-"))
	}
	return b
}

// redactText applies built-in secret and personal-data redaction without reversible encryption.
func redactText(s string) string {
	if s == "" {
		return ""
	}
	s = replaceHomePaths(s)
	out := redact.Preview(s, allRedactOptions)
	out = redact.Scrub(out)
	return out
}

// redactJSONBytes redacts JSON request/response payloads and SSE streams without reversible encryption.
func redactJSONBytes(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	out := replaceHomePathsBytes(body)
	out = redact.ScrubJSON(out)
	if masked, _ := redact.MaskJSON(out, allRedactOptions); len(masked) > 0 {
		out = masked
	}
	return out
}

func redactPrompt(p *Prompt) *Prompt {
	if p == nil {
		return nil
	}
	cp := *p
	if len(p.Parts) > 0 {
		cp.Parts = make([]PromptPart, len(p.Parts))
		for i, part := range p.Parts {
			np := part
			if len(part.Items) > 0 {
				np.Items = make([]PromptItem, len(part.Items))
				for j, it := range part.Items {
					nit := it
					nit.Name = redactText(it.Name)
					np.Items[j] = nit
				}
			}
			cp.Parts[i] = np
		}
	}
	return &cp
}

func redactRouteRecord(r *RouteRecord) *RouteRecord {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Path = redactText(r.Path)
	cp.SessionTitle = redactText(r.SessionTitle)
	cp.Error = redactText(r.Error)
	cp.Prompt = redactPrompt(r.Prompt)
	cp.Usage = slices.Clone(r.Usage)
	return &cp
}

func redactPromptText(pt PromptText) PromptText {
	out := pt
	if len(pt.Items) > 0 {
		out.Items = make([]PromptEntry, len(pt.Items))
		for i, it := range pt.Items {
			nit := it
			nit.Name = redactText(it.Name)
			if len(it.Pieces) > 0 {
				nit.Pieces = make([]PromptPiece, len(it.Pieces))
				for j, pc := range it.Pieces {
					npc := pc
					npc.Name = redactText(pc.Name)
					npc.Text = redactText(pc.Text)
					npc.Src = redactText(pc.Src)
					nit.Pieces[j] = npc
				}
			}
			out.Items[i] = nit
		}
	}
	return out
}

func redactPromptReply(pr PromptReply) PromptReply {
	out := pr
	if len(pr.Pieces) > 0 {
		out.Pieces = make([]PromptPiece, len(pr.Pieces))
		for i, pc := range pr.Pieces {
			npc := pc
			npc.Name = redactText(pc.Name)
			npc.Text = redactText(pc.Text)
			npc.Src = redactText(pc.Src)
			out.Pieces[i] = npc
		}
	}
	return out
}

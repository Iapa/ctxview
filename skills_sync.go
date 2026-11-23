package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	SyncModeSymlink = "symlink"
	SyncModeCopy    = "copy"
)

type TargetState int

const (
	TargetAbsent TargetState = iota
	TargetLinkToSource
	TargetForeignLink
	TargetRealDir
	TargetRealFile
)

type ReplacePolicyKind int

const (
	PolicyNoClobber ReplacePolicyKind = iota
	PolicyRecorded
	PolicyUserConfirmed
)

type ReplacePolicy struct {
	Kind ReplacePolicyKind
	Mode string
}

func (p ReplacePolicy) Permits(state TargetState) bool {
	switch state {
	case TargetAbsent, TargetLinkToSource:
		return true
	case TargetForeignLink:
		switch p.Kind {
		case PolicyNoClobber:
			return false
		case PolicyRecorded:
			return p.Mode == SyncModeSymlink
		case PolicyUserConfirmed:
			return true
		}
	case TargetRealDir:
		switch p.Kind {
		case PolicyNoClobber:
			return false
		case PolicyRecorded:
			return p.Mode == SyncModeCopy
		case PolicyUserConfirmed:
			return true
		}
	case TargetRealFile:
		return p.Kind == PolicyUserConfirmed
	}
	return false
}

func (p ReplacePolicy) Describe() string {
	switch p.Kind {
	case PolicyNoClobber:
		return "is not managed by Skills Manager"
	case PolicyRecorded:
		return "does not match its recorded deployment"
	case PolicyUserConfirmed:
		return "cannot be replaced"
	default:
		return "cannot be replaced"
	}
}

// ReplaceRefusedError represents an ownership refusal when replacing a target path.
type ReplaceRefusedError struct {
	Target string
	Reason string
}

func (e *ReplaceRefusedError) Error() string {
	return fmt.Sprintf(
		"Refusing to replace %q: it %s. The existing content was left untouched — import it into the library, or move it aside, and try again.",
		e.Target,
		e.Reason,
	)
}

func normalizeSyncMode(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), SyncModeCopy) {
		return SyncModeCopy
	}
	return SyncModeSymlink
}

func symlinkPointsTo(target, source string) bool {
	lstat, err := os.Lstat(target)
	if err != nil || lstat.Mode()&os.ModeSymlink == 0 {
		return false
	}
	linkTarget, err := os.Readlink(target)
	if err != nil {
		return false
	}
	resolved := linkTarget
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(target), resolved)
	}
	if filepath.Clean(resolved) == filepath.Clean(source) {
		return true
	}
	canonLink, err1 := filepath.EvalSymlinks(resolved)
	canonSrc, err2 := filepath.EvalSymlinks(source)
	return err1 == nil && err2 == nil && canonLink == canonSrc
}

func classifyTarget(target, source string) (TargetState, error) {
	lstat, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return TargetAbsent, nil
		}
		return TargetAbsent, fmt.Errorf("cannot inspect existing target %s: %w", target, err)
	}
	if lstat.Mode()&os.ModeSymlink != 0 {
		if source != "" && symlinkPointsTo(target, source) {
			return TargetLinkToSource, nil
		}
		return TargetForeignLink, nil
	}
	if lstat.IsDir() {
		return TargetRealDir, nil
	}
	return TargetRealFile, nil
}

func authorizeReplacement(source, target string, policy ReplacePolicy) (TargetState, error) {
	state, err := classifyTarget(target, source)
	if err != nil {
		return TargetAbsent, err
	}
	if !policy.Permits(state) {
		return state, &ReplaceRefusedError{
			Target: target,
			Reason: policy.Describe(),
		}
	}
	return state, nil
}

func isSubpath(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func ensureDstNotInsideSrc(src, dst string) error {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return fmt.Errorf("source and destination are the same path %q; refusing to copy", src)
	}
	srcCanon, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("source %q does not exist or is not accessible: %w", src, err)
	}

	lstat, lerr := os.Lstat(dst)
	dstIsLink := lerr == nil && lstat.Mode()&os.ModeSymlink != 0

	lexicalEntry := func(p string) (string, bool) {
		parentCanon, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil {
			return "", false
		}
		return filepath.Join(parentCanon, filepath.Base(p)), true
	}

	var dstCanon string
	var hasDstCanon bool
	if dstIsLink {
		dstCanon, hasDstCanon = lexicalEntry(dst)
		if srcLex, ok := lexicalEntry(src); ok && hasDstCanon && srcLex == dstCanon {
			return fmt.Errorf("source and destination are the same path %q; refusing to copy", src)
		}
	} else if c, err := filepath.EvalSymlinks(dst); err == nil {
		dstCanon, hasDstCanon = c, true
	} else {
		dstCanon, hasDstCanon = lexicalEntry(dst)
	}

	if hasDstCanon {
		if isSubpath(dstCanon, srcCanon) {
			return fmt.Errorf("destination %q is inside source %q; refusing to copy to avoid infinite recursion", dst, src)
		}
		if isSubpath(srcCanon, dstCanon) {
			return fmt.Errorf("source %q is inside destination %q; refusing to avoid deleting the source", src, dst)
		}
	}
	return nil
}

func isTargetCurrent(source, target, mode, lastSyncedHash, currentHash string) bool {
	switch mode {
	case SyncModeSymlink:
		return symlinkPointsTo(target, source)
	case SyncModeCopy:
		if lastSyncedHash != "" && currentHash != "" && lastSyncedHash == currentHash {
			_, err := os.Lstat(target)
			return err == nil
		}
		return false
	default:
		return false
	}
}

func removeClassifiedTarget(target string, state TargetState) error {
	switch state {
	case TargetAbsent:
		return nil
	case TargetLinkToSource, TargetForeignLink:
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	case TargetRealFile:
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	case TargetRealDir:
		return os.RemoveAll(target)
	default:
		return nil
	}
}

func removeRecordedTarget(target, recordedMode string) (bool, error) {
	state, err := classifyTarget(target, "")
	if err != nil {
		return false, err
	}
	policy := ReplacePolicy{Kind: PolicyRecorded, Mode: recordedMode}
	if !policy.Permits(state) {
		return false, nil
	}
	if err := removeClassifiedTarget(target, state); err != nil {
		return false, err
	}
	return true, nil
}

func removeTarget(target string) error {
	lstat, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if lstat.Mode()&os.ModeSymlink != 0 || !lstat.IsDir() {
		return os.Remove(target)
	}
	return os.RemoveAll(target)
}

func copyDirRecursive(src, dst string) error {
	srcRoot := src
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		srcRoot = resolved
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == srcRoot {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			// Skip symlinks during copy to match skills-manager's installer/copy behavior safely.
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(targetPath, data, info.Mode().Perm())
	})
}

func syncSkill(source, target, mode string, policy ReplacePolicy) (string, error) {
	mode = normalizeSyncMode(mode)
	if isTargetCurrent(source, target, mode, "", "") {
		return mode, nil
	}
	state, err := authorizeReplacement(source, target, policy)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := ensureDstNotInsideSrc(source, target); err != nil {
		return "", err
	}
	if err := removeClassifiedTarget(target, state); err != nil {
		return "", fmt.Errorf("failed to remove existing target %q: %w", target, err)
	}

	if mode == SyncModeSymlink {
		if err := os.Symlink(source, target); err == nil {
			return SyncModeSymlink, nil
		}
		// Fall back to copy when symlinks are disallowed (e.g. unprivileged Windows).
		if err := copyDirRecursive(source, target); err != nil {
			return "", err
		}
		return SyncModeCopy, nil
	}

	if err := copyDirRecursive(source, target); err != nil {
		return "", err
	}
	return SyncModeCopy, nil
}

// ownershipKey identifies a directory entry by canonicalized parent + verbatim base name.
func ownershipKey(path string, memo map[string]string) string {
	parent := filepath.Dir(path)
	base := filepath.Base(path)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return filepath.Clean(path)
	}
	canonParent, ok := memo[parent]
	if !ok {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			canonParent = resolved
		} else {
			canonParent = filepath.Clean(parent)
		}
		memo[parent] = canonParent
	}
	return filepath.Join(canonParent, base)
}

type pathEvidenceKind int

const (
	evidenceOwn pathEvidenceKind = iota
	evidenceBorrowed
	evidenceUnvouched
)

type pathEvidence struct {
	memo    map[string]string
	rows    map[string][]SkillTarget
	written map[string]struct {
		skillID string
		mode    string
	}
}

func newPathEvidence(targets []SkillTarget) *pathEvidence {
	memo := map[string]string{}
	rows := map[string][]SkillTarget{}
	for _, t := range targets {
		k := ownershipKey(t.TargetPath, memo)
		rows[k] = append(rows[k], t)
	}
	return &pathEvidence{
		memo: memo,
		rows: rows,
		written: map[string]struct {
			skillID string
			mode    string
		}{},
	}
}

func (e *pathEvidence) judge(skillID, tool, target string) (pathEvidenceKind, string) {
	key := ownershipKey(target, e.memo)
	if w, ok := e.written[key]; ok {
		if w.skillID == skillID {
			return evidenceOwn, w.mode
		}
		return evidenceUnvouched, ""
	}
	list := e.rows[key]
	if len(list) == 0 {
		return evidenceUnvouched, ""
	}
	firstMode := list[0].Mode
	hasOwn := false
	for _, row := range list {
		if row.SkillID != skillID || row.Mode != firstMode {
			return evidenceUnvouched, ""
		}
		if row.Tool == tool {
			hasOwn = true
		}
	}
	if hasOwn {
		return evidenceOwn, firstMode
	}
	return evidenceBorrowed, firstMode
}

func (e *pathEvidence) recordWrite(skillID, target, mode string) {
	key := ownershipKey(target, e.memo)
	e.written[key] = struct {
		skillID string
		mode    string
	}{skillID: skillID, mode: mode}
}

func copyMatchesLibrary(mode, source, target string) bool {
	if mode != SyncModeCopy {
		return false
	}
	st, err := classifyTarget(target, source)
	if err != nil || st != TargetRealDir {
		return false
	}
	if !isDirFollow(source) {
		return false
	}
	return hashDirectory(target) == hashDirectory(source)
}

func isReplaceRefused(err error) (*ReplaceRefusedError, bool) {
	var ref *ReplaceRefusedError
	if errors.As(err, &ref) {
		return ref, true
	}
	return nil, false
}

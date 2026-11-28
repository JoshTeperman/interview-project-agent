package snapshot

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
)

// OverviewTag is the element that wraps the overview in the system prompt.
// Copies of it inside snapshot text are removed so the text can't close it early.
const OverviewTag = "snapshot_overview"

var overviewTagRe = regexp.MustCompile(`(?i)<\s*/?\s*` + OverviewTag + `[^>]*>`)

// defaultOverviewFiles are included in the overview, in this order, when
// project.json doesn't list overview_files. Matching is exact; missing files are skipped.
var defaultOverviewFiles = []string{
	"README.md",
	"SCOPING.md", "docs/scoping.md",
	"SPEC.md", "docs/spec.md", "docs/SPEC.md",
	"DECISIONS.md", "decisions-log.md", "docs/decisions-log.md", "docs/decisions.md",
}

var defaultSessionsDirs = []string{"sessions", "docs/sessions"}

const (
	treeDepth      = 4
	treeEntries    = 400
	sessionEntries = 200
	treeShare      = 4 // the tree gets at most 1/treeShare of the overview budget
)

// Overview builds the compact snapshot overview for the system prompt (S-D2):
// the project name, the directory tree, the overview documents (spec, decisions
// log), and an index of session exports. The output depends only on the
// snapshot's contents, so it is stable across requests and caches well.
func (s *Snapshot) Overview() string {
	budget := s.limits.MaxOverviewChars
	var b strings.Builder

	name := s.Project.Name
	if name == "" {
		name = "(unnamed project)"
	}
	fmt.Fprintf(&b, "# Snapshot overview\n\nProject: %s\n\n", name)

	tree := s.treeSection()
	if len(tree) > budget/treeShare {
		tree = cutAtLine(tree, budget/treeShare) + "[tree truncated; use list_files]\n"
	}
	b.WriteString(tree)
	b.WriteString(s.sessionIndex())

	docs := s.overviewFiles()
	b.WriteString("## Overview documents\n\n")
	if len(docs) == 0 {
		b.WriteString("(none found; use the tools to explore)\n\n")
	}
	for _, p := range docs {
		data, err := fs.ReadFile(s.fsys, p)
		if err != nil || isBinary(data) {
			continue
		}
		head := fmt.Sprintf("### %s\n\n", p)
		left := budget - b.Len() - len(head) - 100
		if left <= 0 {
			fmt.Fprintf(&b, "### %s\n\n[not included: overview size limit; use read_file]\n\n", p)
			continue
		}
		b.WriteString(head)
		text := strings.TrimRight(string(data), "\n") + "\n"
		if len(text) > left {
			text = cutAtLine(text, left) + fmt.Sprintf("[truncated; use read_file on %s for the rest]\n", p)
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	return overviewTagRe.ReplaceAllString(b.String(), "[tag removed]")
}

func (s *Snapshot) treeSection() string {
	paths, more := s.walkVisible(treeDepth, treeEntries)
	var b strings.Builder
	fmt.Fprintf(&b, "## Directory tree (depth %d)\n\n", treeDepth)
	for _, p := range paths {
		depth := strings.Count(strings.TrimSuffix(p, "/"), "/")
		fmt.Fprintf(&b, "%s%s\n", strings.Repeat("  ", depth), path.Base(strings.TrimSuffix(p, "/"))+suffixIfDir(p))
	}
	if more > 0 {
		fmt.Fprintf(&b, "... %d more paths not shown; use list_files\n", more)
	}
	b.WriteString("\n")
	return b.String()
}

func suffixIfDir(p string) string {
	if strings.HasSuffix(p, "/") {
		return "/"
	}
	return ""
}

// sessionIndex lists session exports with their title (first heading) and size.
func (s *Snapshot) sessionIndex() string {
	dir := s.sessionsDir()
	if dir == "" {
		return ""
	}
	entries, err := fs.ReadDir(s.fsys, dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Session index (%s)\n\n", dir)
	n := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || deniedName(e.Name()) {
			continue
		}
		if n == sessionEntries {
			b.WriteString("... more sessions not shown; use list_files\n")
			break
		}
		n++
		p := dir + "/" + e.Name()
		size := int64(0)
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		if title := s.title(p); title != "" {
			fmt.Fprintf(&b, "- %s: %s (%s)\n", p, title, humanSize(size))
		} else {
			fmt.Fprintf(&b, "- %s (%s)\n", p, humanSize(size))
		}
	}
	if n == 0 {
		b.WriteString("(empty)\n")
	}
	b.WriteString("\n")
	return b.String()
}

// title returns the first Markdown heading in the first 4 KB of a file.
func (s *Snapshot) title(p string) string {
	f, err := s.fsys.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 4<<10)
	n, _ := f.Read(buf)
	sc := bufio.NewScanner(bytes.NewReader(buf[:n]))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); strings.HasPrefix(line, "#") {
			return shorten(strings.TrimSpace(strings.TrimLeft(line, "#")), 120)
		}
	}
	return ""
}

func (s *Snapshot) sessionsDir() string {
	if d := s.Project.SessionsDir; d != "" {
		if rel, err := s.resolve(d); err == nil && rel != "." && s.isDir(rel) {
			return rel
		}
		return ""
	}
	for _, d := range defaultSessionsDirs {
		if rel, err := s.resolve(d); err == nil && s.isDir(rel) {
			return rel
		}
	}
	return ""
}

func (s *Snapshot) overviewFiles() []string {
	candidates := s.Project.OverviewFiles
	if len(candidates) == 0 {
		candidates = defaultOverviewFiles
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		rel, err := s.resolve(c)
		if err != nil || rel == "." || seen[rel] {
			continue
		}
		if fi, err := fs.Stat(s.fsys, rel); err == nil && fi.Mode().IsRegular() {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	return out
}

func (s *Snapshot) isDir(p string) bool {
	fi, err := fs.Stat(s.fsys, p)
	return err == nil && fi.IsDir()
}

// cutAtLine returns at most n bytes of s, ending at a line break when possible.
func cutAtLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if i := strings.LastIndexByte(s[:n], '\n'); i >= 0 {
		return s[:i+1]
	}
	return s[:n] + "\n"
}

package snapshot

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

const (
	maxListEntries   = 500
	maxSearchResults = 50
	maxSearchLine    = 240
	maxSearchFile    = 1 << 20 // files larger than this are skipped by search
	maxReadFile      = 4 << 20 // files larger than this can't be read
	minQueryLen      = 2
	maxQueryLen      = 200
)

// List returns the entries of the directory at p, one per line: directories end
// in "/", files show their size. Hidden secret paths are left out.
func (s *Snapshot) List(p string) (string, error) {
	rel, err := s.resolve(p)
	if err != nil {
		return "", err
	}
	entries, err := fs.ReadDir(s.fsys, rel)
	if err != nil {
		if fi, serr := fs.Stat(s.fsys, rel); serr == nil && !fi.IsDir() {
			return "", fmt.Errorf("%s is a file; use read_file", rel)
		}
		return "", openErr(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", displayPath(rel))
	shown := 0
	for _, e := range entries {
		if deniedName(e.Name()) || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if shown == maxListEntries {
			fmt.Fprintf(&b, "... more entries not shown\n")
			break
		}
		shown++
		switch {
		case e.IsDir():
			fmt.Fprintf(&b, "%s/\n", e.Name())
		default:
			size := int64(0)
			if fi, err := e.Info(); err == nil {
				size = fi.Size()
			}
			fmt.Fprintf(&b, "%s (%s)\n", e.Name(), humanSize(size))
		}
	}
	if shown == 0 {
		b.WriteString("(empty)\n")
	}
	return s.clip(b.String(), ""), nil
}

// Search finds lines containing query (case-insensitive, literal) in text files
// under scope (default: the whole snapshot). Each match is "path:line: text".
func (s *Snapshot) Search(query, scope string) (string, error) {
	query = strings.TrimSpace(query)
	if len(query) < minQueryLen || len(query) > maxQueryLen {
		return "", fmt.Errorf("query must be %d to %d characters", minQueryLen, maxQueryLen)
	}
	rel, err := s.resolve(scope)
	if err != nil {
		return "", err
	}
	if _, err := fs.Stat(s.fsys, rel); err != nil {
		return "", openErr(err)
	}
	needle := strings.ToLower(query)
	var hits []string
	files, truncated := 0, false
	walkErr := fs.WalkDir(s.fsys, rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it
		}
		if p != rel && deniedName(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if fi, err := d.Info(); err != nil || fi.Size() > maxSearchFile {
			return nil
		}
		data, err := fs.ReadFile(s.fsys, p)
		if err != nil || isBinary(data) {
			return nil
		}
		files++
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64<<10), maxSearchFile)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if strings.Contains(strings.ToLower(line), needle) {
				if len(hits) == maxSearchResults {
					truncated = true
					return fs.SkipAll
				}
				hits = append(hits, fmt.Sprintf("%s:%d: %s", p, n, shorten(strings.TrimSpace(line), maxSearchLine)))
			}
		}
		return nil
	})
	if walkErr != nil {
		return "", openErr(walkErr)
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No matches for %q in %s (%d files searched).", query, displayPath(rel), files), nil
	}
	out := strings.Join(hits, "\n") + "\n"
	if truncated {
		out += fmt.Sprintf("[stopped at %d matches; narrow the query or the path]\n", maxSearchResults)
	}
	return s.clip(out, "narrow the query or the path"), nil
}

// Read returns lines start..end (1-based, inclusive; 0 means from the beginning
// or to the end) of the text file at p, each prefixed with its line number so
// answers can cite exact lines. Long output is cut at a line boundary with a note
// saying how to continue.
func (s *Snapshot) Read(p string, start, end int) (string, error) {
	rel, err := s.resolve(p)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", errors.New("path is a directory; use list_files")
	}
	f, err := s.fsys.Open(rel)
	if err != nil {
		return "", openErr(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", openErr(err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory; use list_files", rel)
	}
	if fi.Size() > maxReadFile {
		return "", fmt.Errorf("%s is too large to read (%s)", rel, humanSize(fi.Size()))
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", openErr(err)
	}
	if isBinary(data) {
		return "", fmt.Errorf("%s is a binary file", rel)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	total := len(lines)
	if start < 1 {
		start = 1
	}
	if end < 1 || end > total {
		end = total
	}
	if start > total {
		return "", fmt.Errorf("%s has only %d lines", rel, total)
	}
	if start > end {
		return "", errors.New("start_line is after end_line")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (lines %d-%d of %d)\n", rel, start, end, total)
	budget := max(s.limits.MaxToolChars-200, s.limits.MaxToolChars/2) // room for the header and the note
	for n := start; n <= end; n++ {
		line := fmt.Sprintf("%d\t%s\n", n, lines[n-1])
		if b.Len()+len(line) > budget {
			if n > start {
				fmt.Fprintf(&b, "[truncated at line %d of %d; call read_file with start_line=%d to continue]\n", n-1, total, n)
				return b.String(), nil
			}
			// A single line longer than the budget (minified code, for example): cut the line itself.
			keep := max(0, budget-b.Len()-80)
			fmt.Fprintf(&b, "%d\t%s\n[line %d cut at %d of %d chars]\n", n, strings.ToValidUTF8(lines[n-1][:keep], ""), n, keep, len(lines[n-1]))
			if n < end {
				fmt.Fprintf(&b, "[call read_file with start_line=%d to continue]\n", n+1)
			}
			return b.String(), nil
		}
		b.WriteString(line)
	}
	return s.clip(b.String(), "read a smaller line range"), nil
}

// clip enforces the per-call output cap as a last resort.
func (s *Snapshot) clip(out, hint string) string {
	if len(out) <= s.limits.MaxToolChars {
		return out
	}
	cut := strings.LastIndexByte(out[:s.limits.MaxToolChars], '\n')
	if cut < 0 {
		cut = s.limits.MaxToolChars
	}
	note := "[output truncated]"
	if hint != "" {
		note = "[output truncated; " + hint + "]"
	}
	return out[:cut+1] + note + "\n"
}

// isBinary treats data with a NUL byte in the first 8 KB as binary.
func isBinary(data []byte) bool {
	if len(data) > 8<<10 {
		data = data[:8<<10]
	}
	return bytes.IndexByte(data, 0) >= 0
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func displayPath(rel string) string {
	if rel == "." {
		return "/"
	}
	return rel
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// walkVisible lists visible paths in tree order, depth-limited, for the overview tree.
func (s *Snapshot) walkVisible(maxDepth, maxEntries int) (paths []string, more int) {
	_ = fs.WalkDir(s.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return nil
		}
		if deniedName(d.Name()) || d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		depth := strings.Count(p, "/") + 1
		if depth > maxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if len(paths) >= maxEntries {
			more++
			return nil
		}
		if d.IsDir() {
			paths = append(paths, p+"/")
		} else {
			paths = append(paths, p)
		}
		return nil
	})
	return paths, more
}

// Command srclog extracts log templates from Go source (extract) and matches
// log lines against a template manifest (match).
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/seiflotfy/srclog"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "extract":
		err = runExtract(os.Args[2:], srclog.Extract)
	case "errors":
		err = runExtract(os.Args[2:], srclog.ExtractErrors)
	case "match":
		err = runMatch(os.Args[2:])
	case "mine":
		err = runMine(os.Args[2:])
	case "promote":
		err = runPromote(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "srclog:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  srclog extract [-o manifest.json] [-commit sha] [dir]     log templates from source
  srclog errors  [-o manifest.json] [-commit sha] [dir]     error-string dictionary from source (point at vendor/)
  srclog match -t manifest.json [-d dict.json ...] [file]   reads stdin without a file
  srclog mine [-t manifest.json] [-d dict.json ...] [-min n] [-o candidates.json] [file]
                                                            cluster unmatched lines into candidates
  srclog promote -catalog catalog.json candidates.json      gate candidates into an append-only catalog
`)
	os.Exit(2)
}

func runExtract(args []string, extract func(string) (*srclog.Manifest, error)) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	out := fs.String("o", "srclog-templates.json", `output path ("-" for stdout)`)
	commit := fs.String("commit", "", "commit sha to stamp (default: $GITHUB_SHA or git HEAD)")
	fs.Parse(args)
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	m, err := extract(dir)
	if err != nil {
		return err
	}
	m.Commit = resolveCommit(*commit, dir)

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *out == "-" {
		_, err = os.Stdout.Write(data)
	} else {
		// Write-then-rename so a failed write can't destroy the previous manifest.
		tmp := *out + ".tmp"
		if err = os.WriteFile(tmp, data, 0o644); err == nil {
			err = os.Rename(tmp, *out)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "srclog: %d templates from %d files (%d calls, %d dynamic, %d parse errors)\n",
		len(m.Templates), m.Stats.Files, m.Stats.Calls, m.Stats.Dynamic, m.Stats.ParseErrors)
	return nil
}

func resolveCommit(flagVal, dir string) string {
	if flagVal != "" {
		return flagVal
	}
	if sha := os.Getenv("GITHUB_SHA"); sha != "" {
		return sha
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runMatch(args []string) error {
	fs := flag.NewFlagSet("match", flag.ExitOnError)
	manifestPath := fs.String("t", "", "template manifest (required)")
	var dictPaths []string
	fs.Func("d", "suffix dictionary manifest (repeatable)", func(p string) error {
		dictPaths = append(dictPaths, p)
		return nil
	})
	fs.Parse(args)
	if *manifestPath == "" {
		usage()
	}

	manifest, err := srclog.LoadManifest(*manifestPath)
	if err != nil {
		return err
	}
	matcher, err := srclog.NewMatcher(manifest)
	if err != nil {
		return err
	}
	dict, err := loadDicts(dictPaths)
	if err != nil {
		return err
	}

	in := io.Reader(os.Stdin)
	if fs.NArg() > 0 {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}

	enc := json.NewEncoder(os.Stdout)
	matched, total := 0, 0
	r := bufio.NewReaderSize(in, 64*1024)
	for {
		line, truncated, rerr := readLine(r, maxLineBytes)
		if rerr != nil && rerr != io.EOF {
			return rerr
		}
		if line != "" {
			total++
			// Never match a truncated line: anchored patterns could pair the
			// visible prefix with the wrong template.
			var res any = struct {
				Line string `json:"line"`
			}{line}
			if !truncated {
				if n, ok := srclog.Resolve(matcher, dict, line); ok {
					matched++
					res = n
				}
			}
			if err := enc.Encode(res); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			break
		}
	}
	fmt.Fprintf(os.Stderr, "srclog: matched %d/%d lines\n", matched, total)
	return nil
}

// runMine clusters lines the existing manifests don't explain into candidate
// templates — match first, mine only the residual.
func runMine(args []string) error {
	fs := flag.NewFlagSet("mine", flag.ExitOnError)
	manifestPath := fs.String("t", "", "template manifest to match first (optional)")
	var dictPaths []string
	fs.Func("d", "suffix dictionary manifest (repeatable)", func(p string) error {
		dictPaths = append(dictPaths, p)
		return nil
	})
	minSeen := fs.Int("min", 3, "occurrences before a cluster becomes a candidate")
	out := fs.String("o", "srclog-candidates.json", `output path ("-" for stdout)`)
	fs.Parse(args)

	var matcher *srclog.Matcher
	if *manifestPath != "" {
		manifest, err := srclog.LoadManifest(*manifestPath)
		if err != nil {
			return err
		}
		if matcher, err = srclog.NewMatcher(manifest); err != nil {
			return err
		}
	}
	dict, err := loadDicts(dictPaths)
	if err != nil {
		return err
	}

	in := io.Reader(os.Stdin)
	if fs.NArg() > 0 {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}

	miner := srclog.NewMiner()
	r := bufio.NewReaderSize(in, 64*1024)
	for {
		line, truncated, rerr := readLine(r, maxLineBytes)
		if rerr != nil && rerr != io.EOF {
			return rerr
		}
		if line != "" && !truncated {
			matched := false
			if matcher != nil {
				_, matched = srclog.Resolve(matcher, dict, line)
			} else if dict != nil {
				_, matched = dict.Match(line)
			}
			if !matched {
				miner.Add(line)
			}
		}
		if rerr == io.EOF {
			break
		}
	}

	candidates := miner.Candidates(*minSeen)
	if err := writeJSON(*out, candidates); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "srclog: %d candidates from %d unmatched lines (min %d occurrences)\n",
		len(candidates.Templates), candidates.Stats.Calls, *minSeen)
	return nil
}

// runPromote gates candidates into an append-only catalog manifest.
func runPromote(args []string) error {
	fs := flag.NewFlagSet("promote", flag.ExitOnError)
	catalogPath := fs.String("catalog", "", "catalog manifest, created if missing (required)")
	fs.Parse(args)
	if *catalogPath == "" || fs.NArg() == 0 {
		usage()
	}

	catalog := &srclog.Manifest{Version: 1}
	if _, err := os.Stat(*catalogPath); err == nil {
		if catalog, err = srclog.LoadManifest(*catalogPath); err != nil {
			return err
		}
	}

	var total srclog.PromoteResult
	for _, p := range fs.Args() {
		candidates, err := srclog.LoadManifest(p)
		if err != nil {
			return err
		}
		res, err := srclog.Promote(catalog, candidates)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		total.Admitted += res.Admitted
		total.Aliased += res.Aliased
		total.Skipped += res.Skipped
	}
	if err := writeJSON(*catalogPath, catalog); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "srclog: %d admitted, %d aliased, %d skipped → %s (%d templates)\n",
		total.Admitted, total.Aliased, total.Skipped, *catalogPath, len(catalog.Templates))
	return nil
}

// writeJSON marshals v and writes it to path ("-" for stdout) via tmp+rename.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "-" {
		_, err = os.Stdout.Write(data)
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadDicts merges suffix-dictionary manifests into one matcher, nil if none.
func loadDicts(paths []string) (*srclog.Matcher, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	merged := &srclog.Manifest{Version: 1, Anchor: "suffix"}
	for _, p := range paths {
		m, err := srclog.LoadManifest(p)
		if err != nil {
			return nil, err
		}
		if m.Anchor != "suffix" {
			return nil, fmt.Errorf("%s: -d requires a suffix-anchored dictionary manifest", p)
		}
		// Validate per file so an error names the bad dictionary, not an
		// index into the merged slice.
		if _, err := srclog.NewMatcher(m); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		merged.Templates = append(merged.Templates, m.Templates...)
	}
	return srclog.NewMatcher(merged)
}

// maxLineBytes caps how much of a single log line is kept; log streams are
// dirty and one pathological line must not abort or balloon the run.
const maxLineBytes = 1 << 20

// readLine returns the next line (without trailing \r\n), truncated to max
// bytes. The remainder of an overlong line is consumed and discarded. err is
// io.EOF on the final line.
func readLine(r *bufio.Reader, max int) (line string, truncated bool, err error) {
	var b []byte
	for {
		chunk, cerr := r.ReadSlice('\n')
		keep := chunk
		if len(b)+len(keep) > max {
			keep = keep[:max-len(b)]
			truncated = true
		}
		b = append(b, keep...)
		if cerr == bufio.ErrBufferFull {
			continue
		}
		if cerr != nil && cerr != io.EOF {
			return "", false, cerr
		}
		return strings.TrimRight(string(b), "\r\n"), truncated, cerr
	}
}

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
		err = runExtract(os.Args[2:])
	case "match":
		err = runMatch(os.Args[2:])
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
  srclog extract [-o manifest.json] [-commit sha] [dir]
  srclog match -t manifest.json [file]   (reads stdin without a file)
`)
	os.Exit(2)
}

func runExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	out := fs.String("o", "srclog-templates.json", `output path ("-" for stdout)`)
	commit := fs.String("commit", "", "commit sha to stamp (default: $GITHUB_SHA or git HEAD)")
	fs.Parse(args)
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	m, err := srclog.Extract(dir)
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
		err = os.WriteFile(*out, data, 0o644)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "srclog: %d templates from %d files (%d log calls, %d dynamic, %d parse errors)\n",
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

	in := io.Reader(os.Stdin)
	if fs.NArg() > 0 {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}

	type result struct {
		ID       string   `json:"id,omitempty"`
		Level    string   `json:"level,omitempty"`
		Template string   `json:"template,omitempty"`
		Params   []string `json:"params,omitempty"`
		Line     string   `json:"line,omitempty"`
	}
	enc := json.NewEncoder(os.Stdout)
	matched, total := 0, 0
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		total++
		if m, ok := matcher.Match(line); ok {
			matched++
			enc.Encode(result{ID: m.Template.ID, Level: m.Template.Level, Template: m.Template.Template, Params: m.Params})
		} else {
			enc.Encode(result{Line: line})
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "srclog: matched %d/%d lines\n", matched, total)
	return nil
}

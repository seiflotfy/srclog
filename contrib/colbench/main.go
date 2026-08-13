//go:build !shapegen

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"time"

	"github.com/seiflotfy/srclog"
)

func main() {
	manifest, err := srclog.LoadManifest(os.Args[1])
	check(err)
	primary, err := srclog.NewMatcher(manifest)
	check(err)
	var dict *srclog.Matcher
	if len(os.Args) > 3 {
		merged := &srclog.Manifest{Version: 1, Anchor: "suffix"}
		for _, p := range os.Args[3:] {
			m, err := srclog.LoadManifest(p)
			check(err)
			merged.Templates = append(merged.Templates, m.Templates...)
		}
		dict, err = srclog.NewMatcher(merged)
		check(err)
	}

	f, err := os.Open(os.Args[2])
	check(err)
	var lines []string
	var rawBytes int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		rawBytes += len(sc.Text()) + 1
	}
	check(sc.Err())

	t0 := time.Now()
	col := srclog.BuildColumn(lines, primary, dict)
	build := time.Since(t0)

	var buf bytes.Buffer
	_, err = col.WriteTo(&buf)
	check(err)

	t1 := time.Now()
	got, err := srclog.ReadColumn(bytes.NewReader(buf.Bytes()))
	check(err)
	rows, err := got.Rows()
	check(err)
	decode := time.Since(t1)
	for i := range lines {
		if rows[i] != lines[i] {
			fmt.Fprintf(os.Stderr, "ROUND-TRIP MISMATCH row %d:\n got %q\nwant %q\n", i, rows[i], lines[i])
			os.Exit(1)
		}
	}

	err = os.WriteFile(os.Args[2]+".tc01", buf.Bytes(), 0o644)
	check(err)
	fmt.Printf("rows=%d raw=%d wire=%d templates=%d build=%s decode+verify=%s\n",
		len(lines), rawBytes, buf.Len(), len(col.Templates()), build.Round(time.Millisecond), decode.Round(time.Millisecond))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

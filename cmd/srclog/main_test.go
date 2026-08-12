package main

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	long := strings.Repeat("x", 3000)
	in := "short\r\n" + long + "\n" + "tail"
	// Tiny buffer forces ErrBufferFull; cap below the long line forces truncation.
	r := bufio.NewReaderSize(strings.NewReader(in), 16)

	line, truncated, err := readLine(r, 1000)
	if line != "short" || truncated || err != nil {
		t.Fatalf("got (%q, %v, %v), want (short, false, nil)", line, truncated, err)
	}

	line, truncated, err = readLine(r, 1000)
	if len(line) != 1000 || !truncated || err != nil {
		t.Fatalf("got (len %d, %v, %v), want (len 1000, true, nil)", len(line), truncated, err)
	}

	// Final line without trailing newline arrives with io.EOF.
	line, truncated, err = readLine(r, 1000)
	if line != "tail" || truncated || err != io.EOF {
		t.Fatalf("got (%q, %v, %v), want (tail, false, EOF)", line, truncated, err)
	}
}

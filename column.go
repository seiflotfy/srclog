package srclog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// Column is a columnar encoding of log lines against a template catalog,
// inspired by dense-ID template codecs: each row stores a small template
// code; params store only their variable leaves, with cascaded sub-template
// matches encoded recursively as codes of their own. Template text is copied
// into the block table once per used template, so a serialized column is
// self-decodable without the catalog; the catalog IDs ride along for
// cross-block identity.
//
// Streams (all in row / pre-order traversal position, so positions are
// derived at read time, never stored):
//
//	rowIDs    one code per row; 0 = unmatched (raw line in unmatched)
//	subIDs    one code per param slot, depth-first; 0 = plain leaf
//	leaves    values of plain param slots
//	unmatched raw lines of unmatched rows
type Column struct {
	rowIDs    []uint32
	table     []ColumnTemplate
	subIDs    []uint32
	leaves    []string
	unmatched []string
}

// ColumnTemplate is one block-table entry.
type ColumnTemplate struct {
	CatalogID string // stable identity across blocks
	Template  string // text with placeholders; param count derives from it
	Suffix    bool   // dictionary entry: one prefix leaf precedes its params
	Count     int    // rows + param slots that used this entry

	nParams int // derived after load; not serialized
}

// BuildColumn matches every line via Resolve and encodes the results.
// dict may be nil (no cascade).
func BuildColumn(lines []string, primary, dict *Matcher) *Column {
	c := &Column{}
	codes := map[string]uint32{}

	code := func(n *Node) uint32 {
		if k, ok := codes[n.ID]; ok {
			c.table[k-1].Count++
			return k
		}
		c.table = append(c.table, ColumnTemplate{
			CatalogID: n.ID,
			Template:  n.Template,
			Suffix:    n.Suffix,
			Count:     1,
			nParams:   strings.Count(n.Template, Placeholder),
		})
		k := uint32(len(c.table))
		codes[n.ID] = k
		return k
	}

	// encodeNode writes a node's streams: suffix entries store their lossless
	// prefix as a leaf first, then params in pre-order.
	var encodeNode func(n *Node)
	encodeNode = func(n *Node) {
		if n.Suffix {
			c.leaves = append(c.leaves, n.Prefix)
		}
		for _, p := range n.Params {
			if sub, ok := p.(*Node); ok {
				c.subIDs = append(c.subIDs, code(sub))
				encodeNode(sub)
				continue
			}
			c.subIDs = append(c.subIDs, 0)
			c.leaves = append(c.leaves, p.(string))
		}
	}

	for _, line := range lines {
		n, ok := Resolve(primary, dict, line)
		if !ok {
			c.rowIDs = append(c.rowIDs, 0)
			c.unmatched = append(c.unmatched, line)
			continue
		}
		c.rowIDs = append(c.rowIDs, code(n))
		encodeNode(n)
	}
	return c
}

// Len returns the number of rows.
func (c *Column) Len() int { return len(c.rowIDs) }

// Templates returns the block's template table.
func (c *Column) Templates() []ColumnTemplate { return c.table }

// Rows reconstructs every original line.
func (c *Column) Rows() ([]string, error) {
	out := make([]string, len(c.rowIDs))
	cur := &cursor{c: c}
	for i, id := range c.rowIDs {
		if id == 0 {
			s, err := cur.nextUnmatched()
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", i, err)
			}
			out[i] = s
			continue
		}
		s, err := cur.render(id)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", i, err)
		}
		out[i] = s
	}
	return out, nil
}

type cursor struct {
	c              *Column
	sub, leaf, unm int
}

func (cu *cursor) nextUnmatched() (string, error) {
	if cu.unm >= len(cu.c.unmatched) {
		return "", fmt.Errorf("unmatched stream exhausted")
	}
	s := cu.c.unmatched[cu.unm]
	cu.unm++
	return s, nil
}

func (cu *cursor) render(code uint32) (string, error) {
	if int(code) > len(cu.c.table) {
		return "", fmt.Errorf("template code %d out of range", code)
	}
	t := cu.c.table[code-1]
	parts := strings.Split(t.Template, Placeholder)
	var b strings.Builder
	if t.Suffix {
		if cu.leaf >= len(cu.c.leaves) {
			return "", fmt.Errorf("leaf stream exhausted (prefix)")
		}
		b.WriteString(cu.c.leaves[cu.leaf])
		cu.leaf++
	}
	b.WriteString(parts[0])
	for i := 1; i < len(parts); i++ {
		if cu.sub >= len(cu.c.subIDs) {
			return "", fmt.Errorf("subID stream exhausted")
		}
		sub := cu.c.subIDs[cu.sub]
		cu.sub++
		if sub == 0 {
			if cu.leaf >= len(cu.c.leaves) {
				return "", fmt.Errorf("leaf stream exhausted")
			}
			b.WriteString(cu.c.leaves[cu.leaf])
			cu.leaf++
		} else {
			s, err := cu.render(sub)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		b.WriteString(parts[i])
	}
	return b.String(), nil
}

// Wire format (tc01): magic, then uvarint-encoded streams. Strings are
// written as one lens stream plus one byte blob so similar values sit
// together for the block compressor.
const columnMagic = "tc01"

// WriteTo serializes the column. The payload is not compressed — that is the
// block store's job.
func (c *Column) WriteTo(w io.Writer) (int64, error) {
	var buf bytes.Buffer
	buf.WriteString(columnMagic)
	writeUvarints(&buf, c.rowIDs)

	putUvarint(&buf, uint64(len(c.table)))
	for _, t := range c.table {
		writeString(&buf, t.CatalogID)
		writeString(&buf, t.Template)
		if t.Suffix {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
		putUvarint(&buf, uint64(t.Count))
	}

	writeUvarints(&buf, c.subIDs)
	writeStrings(&buf, c.leaves)
	writeStrings(&buf, c.unmatched)
	n, err := w.Write(buf.Bytes())
	return int64(n), err
}

// ReadColumn deserializes a column written by WriteTo.
func ReadColumn(r io.Reader) (*Column, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	rd := &byteReader{data: data}
	magic := rd.bytes(4)
	if string(magic) != columnMagic {
		return nil, fmt.Errorf("column: bad magic")
	}
	c := &Column{}
	c.rowIDs = rd.uvarints()

	n := int(rd.uvarint())
	if n < 0 || n > rd.remaining() {
		return nil, fmt.Errorf("column: template table count %d exceeds payload", n)
	}
	c.table = make([]ColumnTemplate, n)
	for i := range c.table {
		id := rd.str()
		tmpl := rd.str()
		sfx := rd.bytes(1)
		cnt := int(rd.uvarint())
		c.table[i] = ColumnTemplate{
			CatalogID: id, Template: tmpl, Count: cnt,
			Suffix:  len(sfx) == 1 && sfx[0] == 1,
			nParams: strings.Count(tmpl, Placeholder),
		}
	}

	c.subIDs = rd.uvarints()
	c.leaves = rd.strs()
	c.unmatched = rd.strs()
	if rd.err != nil {
		return nil, fmt.Errorf("column: %w", rd.err)
	}
	for _, id := range c.rowIDs {
		if int(id) > len(c.table) {
			return nil, fmt.Errorf("column: row template code %d out of range", id)
		}
	}
	for _, id := range c.subIDs {
		if int(id) > len(c.table) {
			return nil, fmt.Errorf("column: sub template code %d out of range", id)
		}
	}
	return c, nil
}

// --- encoding helpers ---

func putUvarint(buf *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	buf.Write(tmp[:binary.PutUvarint(tmp[:], v)])
}

func writeUvarints(buf *bytes.Buffer, vs []uint32) {
	putUvarint(buf, uint64(len(vs)))
	for _, v := range vs {
		putUvarint(buf, uint64(v))
	}
}

func writeString(buf *bytes.Buffer, s string) {
	putUvarint(buf, uint64(len(s)))
	buf.WriteString(s)
}

func writeStrings(buf *bytes.Buffer, vs []string) {
	putUvarint(buf, uint64(len(vs)))
	for _, s := range vs {
		putUvarint(buf, uint64(len(s)))
	}
	for _, s := range vs {
		buf.WriteString(s)
	}
}

type byteReader struct {
	data []byte
	pos  int
	err  error
}

func (r *byteReader) remaining() int { return len(r.data) - r.pos }

func (r *byteReader) fail(msg string) {
	if r.err == nil {
		r.err = fmt.Errorf("%s at offset %d", msg, r.pos)
	}
}

func (r *byteReader) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.data[r.pos:])
	if n <= 0 {
		r.fail("bad uvarint")
		return 0
	}
	r.pos += n
	return v
}

func (r *byteReader) bytes(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > r.remaining() {
		r.fail("truncated")
		return nil
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b
}

func (r *byteReader) str() string { return string(r.bytes(int(r.uvarint()))) }

func (r *byteReader) uvarints() []uint32 {
	n := int(r.uvarint())
	if r.err != nil || n < 0 || n > r.remaining()+1 {
		r.fail("uvarint count exceeds payload")
		return nil
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = uint32(r.uvarint())
	}
	return out
}

func (r *byteReader) strs() []string {
	n := int(r.uvarint())
	if r.err != nil || n < 0 || n > r.remaining()+1 {
		r.fail("string count exceeds payload")
		return nil
	}
	lens := make([]int, n)
	for i := range lens {
		lens[i] = int(r.uvarint())
	}
	out := make([]string, n)
	for i, l := range lens {
		out[i] = string(r.bytes(l))
	}
	return out
}

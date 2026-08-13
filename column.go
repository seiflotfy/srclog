package srclog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
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
//	buckets   leaf values grouped per (template, slot) — locality for the
//	          block compressor, and low-cardinality buckets dictionary-encode
//	unmatched raw lines of unmatched rows
type Column struct {
	rowIDs    []uint32
	table     []ColumnTemplate
	subIDs    []uint32
	buckets   map[bucketKey][]string
	unmatched []string
}

// bucketKey addresses one leaf stream: slot >= 0 is a param position of the
// template; slot -1 holds suffix prefixes.
type bucketKey struct {
	code uint32
	slot int
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
//
// Build is two-pass: pass one resolves and profiles every param slot; slots
// that hold exactly one distinct leaf value across the block fold into a
// block-specialized template text (the CatalogID stays — the specialization
// is aliased, never a new identity). Pass two encodes with folded slots
// erased from every stream.
func BuildColumn(lines []string, primary, dict *Matcher) *Column {
	c := &Column{buckets: map[bucketKey][]string{}}

	// pass 1: resolve + profile
	type slotAgg struct {
		val    string
		seen   bool
		varied bool
		sub    bool
	}
	agg := map[string]map[int]*slotAgg{} // catalog ID → slot → stats
	nodes := make([]*Node, len(lines))
	var profile func(n *Node)
	profile = func(n *Node) {
		slots := agg[n.ID]
		if slots == nil {
			slots = map[int]*slotAgg{}
			agg[n.ID] = slots
		}
		for i, p := range n.Params {
			a := slots[i]
			if a == nil {
				a = &slotAgg{}
				slots[i] = a
			}
			switch v := p.(type) {
			case *Node:
				a.sub = true
				profile(v)
			case string:
				if !a.seen {
					a.val, a.seen = v, true
				} else if a.val != v {
					a.varied = true
				}
			}
		}
	}
	res := NewResolver(primary, dict)
	for i, line := range lines {
		if n, ok := res.Resolve(line); ok {
			nodes[i] = n
			profile(n)
		}
	}

	// fold decisions: leaf-only, single-valued, value can't collide with the
	// placeholder marker.
	folded := map[string]map[int]string{}
	for id, slots := range agg {
		for i, a := range slots {
			if a.seen && !a.varied && !a.sub && !strings.Contains(a.val, Placeholder) {
				m := folded[id]
				if m == nil {
					m = map[int]string{}
					folded[id] = m
				}
				m[i] = a.val
			}
		}
	}

	// pass 2: encode against specialized templates
	codes := map[string]uint32{}
	compact := map[string][]int{} // catalog ID → orig slot → compact slot (-1 = folded)

	code := func(n *Node) uint32 {
		if k, ok := codes[n.ID]; ok {
			c.table[k-1].Count++
			return k
		}
		folds := folded[n.ID]
		parts := strings.Split(n.Template, Placeholder)
		var b strings.Builder
		remap := make([]int, len(parts)-1)
		next := 0
		b.WriteString(parts[0])
		for i := 1; i < len(parts); i++ {
			if v, ok := folds[i-1]; ok {
				b.WriteString(v)
				remap[i-1] = -1
			} else {
				b.WriteString(Placeholder)
				remap[i-1] = next
				next++
			}
			b.WriteString(parts[i])
		}
		tmpl := b.String()
		compact[n.ID] = remap
		c.table = append(c.table, ColumnTemplate{
			CatalogID: n.ID,
			Template:  tmpl,
			Suffix:    n.Suffix,
			Count:     1,
			nParams:   next,
		})
		k := uint32(len(c.table))
		codes[n.ID] = k
		return k
	}

	// encodeNode writes a node's streams: suffix entries store their lossless
	// prefix as a leaf first, then non-folded params in pre-order.
	var encodeNode func(k uint32, n *Node)
	encodeNode = func(k uint32, n *Node) {
		if n.Suffix {
			pk := bucketKey{k, -1}
			c.buckets[pk] = append(c.buckets[pk], n.Prefix)
		}
		remap := compact[n.ID]
		for i, p := range n.Params {
			if sub, ok := p.(*Node); ok {
				sk := code(sub)
				c.subIDs = append(c.subIDs, sk)
				encodeNode(sk, sub)
				continue
			}
			if i < len(remap) && remap[i] == -1 {
				continue // folded into the template text
			}
			c.subIDs = append(c.subIDs, 0)
			bk := bucketKey{k, remap[i]}
			c.buckets[bk] = append(c.buckets[bk], p.(string))
		}
	}

	for i := range lines {
		n := nodes[i]
		if n == nil {
			c.rowIDs = append(c.rowIDs, 0)
			c.unmatched = append(c.unmatched, lines[i])
			continue
		}
		k := code(n)
		c.rowIDs = append(c.rowIDs, k)
		encodeNode(k, n)
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
	c        *Column
	sub, unm int
	pos      map[bucketKey]int
}

func (cu *cursor) nextLeaf(k bucketKey) (string, error) {
	if cu.pos == nil {
		cu.pos = map[bucketKey]int{}
	}
	vals := cu.c.buckets[k]
	i := cu.pos[k]
	if i >= len(vals) {
		return "", fmt.Errorf("leaf bucket (%d,%d) exhausted", k.code, k.slot)
	}
	cu.pos[k] = i + 1
	return vals[i], nil
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
		p, err := cu.nextLeaf(bucketKey{code, -1})
		if err != nil {
			return "", err
		}
		b.WriteString(p)
	}
	b.WriteString(parts[0])
	for i := 1; i < len(parts); i++ {
		if cu.sub >= len(cu.c.subIDs) {
			return "", fmt.Errorf("subID stream exhausted")
		}
		sub := cu.c.subIDs[cu.sub]
		cu.sub++
		if sub == 0 {
			v, err := cu.nextLeaf(bucketKey{code, i - 1})
			if err != nil {
				return "", err
			}
			b.WriteString(v)
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

// Wire format (tc02): magic, then uvarint-encoded streams. Leaf values are
// grouped per (template, slot) bucket so similar values sit together for the
// block compressor; low-cardinality buckets dictionary-encode (dict + small
// indices) — detected enums cost almost nothing on the wire.
const columnMagic = "tc02"

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

	keys := make([]bucketKey, 0, len(c.buckets))
	for k := range c.buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].code != keys[j].code {
			return keys[i].code < keys[j].code
		}
		return keys[i].slot < keys[j].slot
	})
	putUvarint(&buf, uint64(len(keys)))
	for _, k := range keys {
		putUvarint(&buf, uint64(k.code))
		putUvarint(&buf, uint64(k.slot+1)) // 0 encodes the prefix bucket (-1)
		writeBucket(&buf, c.buckets[k])
	}

	writeStrings(&buf, c.unmatched)
	n, err := w.Write(buf.Bytes())
	return int64(n), err
}

// writeBucket dictionary-encodes when values repeat enough to pay for the
// dictionary, otherwise writes them plain.
func writeBucket(buf *bytes.Buffer, vals []string) {
	distinct := map[string]uint64{}
	order := []string{}
	for _, v := range vals {
		if _, ok := distinct[v]; !ok {
			distinct[v] = uint64(len(order))
			order = append(order, v)
		}
	}
	if len(order) <= 255 && len(vals) >= 8 && len(order)*2 <= len(vals) {
		buf.WriteByte(1)
		writeStrings(buf, order)
		putUvarint(buf, uint64(len(vals)))
		for _, v := range vals {
			putUvarint(buf, distinct[v])
		}
		return
	}
	buf.WriteByte(0)
	writeStrings(buf, vals)
}

func readBucket(rd *byteReader) []string {
	kind := rd.bytes(1)
	if len(kind) != 1 {
		return nil
	}
	if kind[0] == 0 {
		return rd.strs()
	}
	dict := rd.strs()
	n := int(rd.uvarint())
	if rd.err != nil || n < 0 || n > rd.remaining()+len(dict)*2+16 {
		rd.fail("bucket index count exceeds payload")
		return nil
	}
	out := make([]string, n)
	for i := range out {
		idx := rd.uvarint()
		if int(idx) >= len(dict) {
			rd.fail("bucket dict index out of range")
			return nil
		}
		out[i] = dict[idx]
	}
	return out
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

	nb := int(rd.uvarint())
	if nb < 0 || nb > rd.remaining()+1 {
		return nil, fmt.Errorf("column: bucket count exceeds payload")
	}
	c.buckets = make(map[bucketKey][]string, nb)
	for i := 0; i < nb && rd.err == nil; i++ {
		code := uint32(rd.uvarint())
		slot := int(rd.uvarint()) - 1
		c.buckets[bucketKey{code, slot}] = readBucket(rd)
	}

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

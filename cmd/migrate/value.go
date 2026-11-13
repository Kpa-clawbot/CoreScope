package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// normalize converts one value read from either driver into cls. Results are
// only ever nil, int64, float64, string or []byte. Every conversion is
// lossless; anything else is an error the caller reports with its location.
//
// strictText additionally rejects text Postgres cannot store (invalid UTF-8,
// NUL bytes): SQLite stores such bytes as TEXT without complaint.
func normalize(cls colClass, v any, strictText bool) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int:
		v = int64(x)
	case int32:
		v = int64(x)
	case int16:
		v = int64(x)
	case float32:
		v = float64(x)
	}
	switch cls {
	case clsInt:
		switch x := v.(type) {
		case int64:
			return x, nil
		case float64:
			// INTEGER affinity already turned every integral, in-range real
			// into an integer, so this is only reached by values that cannot
			// be one; the check keeps the rule exact all the same.
			if x == math.Trunc(x) && x >= -(1<<63) && x < (1<<63) {
				return int64(x), nil
			}
			return nil, fmt.Errorf("real %s cannot be stored in an integer column", strconv.FormatFloat(x, 'g', -1, 64))
		}
	case clsFloat:
		switch x := v.(type) {
		case float64:
			return x, nil
		case int64:
			if f := float64(x); f >= -(1<<63) && f < (1<<63) && int64(f) == x {
				return f, nil
			}
			return nil, fmt.Errorf("integer %d has no exact float representation", x)
		}
	case clsText:
		switch x := v.(type) {
		case string:
			if strictText {
				if !utf8.ValidString(x) {
					return nil, fmt.Errorf("text is not valid UTF-8 (Postgres rejects it): %s", preview(x))
				}
				if strings.IndexByte(x, 0) >= 0 {
					return nil, fmt.Errorf("text contains a NUL byte (Postgres rejects it): %s", preview(x))
				}
			}
			return x, nil
		case int64:
			return strconv.FormatInt(x, 10), nil
		case float64:
			return strconv.FormatFloat(x, 'g', -1, 64), nil
		}
	case clsBlob:
		if b, ok := v.([]byte); ok {
			return b, nil
		}
	}
	article := "a"
	if cls == clsInt {
		article = "an"
	}
	return nil, fmt.Errorf("%s cannot be stored in %s %s column", describe(v), article, cls)
}

func describe(v any) string {
	switch x := v.(type) {
	case string:
		return "text " + preview(x)
	case []byte:
		return fmt.Sprintf("blob of %d bytes", len(x))
	case int64:
		return "integer " + strconv.FormatInt(x, 10)
	case float64:
		return "real " + strconv.FormatFloat(x, 'g', -1, 64)
	}
	return fmt.Sprintf("value of Go type %T", v)
}

func preview(s string) string {
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return strconv.Quote(s)
}

// appendValue appends the canonical digest encoding of a normalised value: a
// type tag, then the value. Variable-length values are length-prefixed so the
// encoding of a row is unambiguous. -0 is encoded as 0: SQLite stores an
// integral REAL as an integer and so cannot keep the sign of zero.
func appendValue(buf []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(buf, 'N')
	case int64:
		buf = append(buf, 'I')
		return binary.BigEndian.AppendUint64(buf, uint64(x))
	case float64:
		if x == 0 {
			x = 0
		}
		s := strconv.FormatFloat(x, 'g', -1, 64)
		buf = append(buf, 'F')
		buf = binary.AppendUvarint(buf, uint64(len(s)))
		return append(buf, s...)
	case string:
		buf = append(buf, 'T')
		buf = binary.AppendUvarint(buf, uint64(len(x)))
		return append(buf, x...)
	case []byte:
		buf = append(buf, 'B')
		buf = binary.AppendUvarint(buf, uint64(len(x)))
		return append(buf, x...)
	}
	// Only reached for raw values verify could not normalise: encode them
	// with their Go type so they compare equal only to the same raw value.
	s := fmt.Sprintf("%T:%v", v, v)
	buf = append(buf, 'R')
	buf = binary.AppendUvarint(buf, uint64(len(s)))
	return append(buf, s...)
}

// typeRank orders storage classes as SQLite does: NULL, numbers, text, blob.
func typeRank(v any) int {
	switch v.(type) {
	case nil:
		return 0
	case int64, float64:
		return 1
	case string:
		return 2
	case []byte:
		return 3
	}
	return 4
}

// compareValues orders normalised values the way both databases order
// primary keys: numbers numerically, text and blobs bytewise (SQLite BINARY,
// Postgres COLLATE "C").
func compareValues(a, b any) int {
	ra, rb := typeRank(a), typeRank(b)
	if ra != rb {
		return ra - rb
	}
	switch x := a.(type) {
	case int64:
		switch y := b.(type) {
		case int64:
			return cmp3(x < y, x > y)
		case float64:
			return cmp3(float64(x) < y, float64(x) > y)
		}
	case float64:
		switch y := b.(type) {
		case int64:
			return cmp3(x < float64(y), x > float64(y))
		case float64:
			return cmp3(x < y, x > y)
		}
	case string:
		return strings.Compare(x, b.(string))
	case []byte:
		return bytes.Compare(x, b.([]byte))
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

func cmp3(lt, gt bool) int {
	switch {
	case lt:
		return -1
	case gt:
		return 1
	}
	return 0
}

// formatValue renders a value for an error or report line.
func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return preview(x)
	case []byte:
		return fmt.Sprintf("x'%x'", x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

// rowStream streams a table's rows with one reused scan buffer.
type rowStream struct {
	rows *sql.Rows
	vals []any
	ptrs []any
}

// openStream selects cols of t (by name) from q. On SQLite each column is
// read as +"col": the unary plus is a no-op on the value but drops the
// declared type, which otherwise makes mattn/go-sqlite3 turn DATETIME text
// into time.Time and BOOLEAN integers into bool.
func openStream(ctx context.Context, q queryer, kind backend, t *table, cols []string, ordered bool) (*rowStream, error) {
	var sb strings.Builder
	sb.WriteString("SELECT ")
	for i, c := range cols {
		if i > 0 {
			sb.WriteString(", ")
		}
		if kind == sqliteBackend {
			sb.WriteByte('+')
		}
		sb.WriteString(quoteIdent(c))
	}
	sb.WriteString(" FROM ")
	sb.WriteString(quoteIdent(t.name))
	if ordered {
		sb.WriteString(" ORDER BY ")
		for i, idx := range t.pk {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(quoteIdent(t.cols[idx].name))
		}
	}
	rows, err := q.QueryContext(ctx, sb.String())
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", t.name, err)
	}
	s := &rowStream{rows: rows, vals: make([]any, len(cols)), ptrs: make([]any, len(cols))}
	for i := range s.vals {
		s.ptrs[i] = &s.vals[i]
	}
	return s, nil
}

// next advances to the next row; s.vals then holds its raw values.
func (s *rowStream) next() (bool, error) {
	if !s.rows.Next() {
		return false, s.rows.Err()
	}
	return true, s.rows.Scan(s.ptrs...)
}

func (s *rowStream) close() { s.rows.Close() }

// describePK renders the primary key of the current row, e.g. "id=42".
func describePK(t *table, vals []any) string {
	parts := make([]string, len(t.pk))
	for i, idx := range t.pk {
		parts[i] = t.cols[idx].name + "=" + formatValue(vals[idx])
	}
	return strings.Join(parts, ", ")
}

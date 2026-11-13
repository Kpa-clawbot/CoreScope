package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"hash"
	"log"
	"sort"
	"strings"
)

// side is one database being compared: a query surface (normally a snapshot
// transaction), its backend and its schema.
type side struct {
	q      queryer
	kind   backend
	schema *schema
}

type tableResult struct {
	name         string
	rowsA, rowsB int64
	digestA      string
	digestB      string
	extraB       int64 // seeded bookkeeping rows only b has (tolerated)
	unfinishedA  int64 // unfinished _async_migrations rows on a, never copied (tolerated)
	firstDiff    string
	ok           bool
	notCompared  []string // columns only a has
	nullOnA      []string // columns only b has, read as NULL on a
}

// compareTables compares every table in b's schema (less skipTables) against
// a. b is the reference schema: in copy it is the target. Columns are b's;
// a must have every one. Each side is streamed in primary-key order and
// merge-joined, so a missing, extra or differing row is located exactly, and
// each side's rows feed an ordered SHA-256 digest of their canonical
// encoding.
func compareTables(ctx context.Context, a, b side) ([]tableResult, error) {
	var out []tableResult
	for _, name := range b.schema.sortedNames() {
		if _, skip := skipTables[name]; skip {
			continue
		}
		bt := b.schema.tables[name]
		res := tableResult{name: name}
		at := a.schema.tables[name]
		if at == nil && bookkeepingTables[name] {
			// A source prepared by `-db` has no _async_migrations; copy
			// (with -allow-pending-migrations) leaves the target's own rows.
			if err := b.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(name)).Scan(&res.rowsB); err != nil {
				return nil, fmt.Errorf("side b: count %s: %w", name, err)
			}
			res.extraB = res.rowsB
			res.ok = true
			res.firstDiff = "table is missing on side a; tolerated for bookkeeping"
			out = append(out, res)
			continue
		}
		if at == nil {
			// A table that does not exist holds no rows: equal exactly when
			// b's table is empty (copy -allow-pending-migrations leaves it so).
			if err := b.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(name)).Scan(&res.rowsB); err != nil {
				return nil, fmt.Errorf("side b: count %s: %w", name, err)
			}
			res.ok = res.rowsB == 0
			res.firstDiff = "table is missing on side a"
			if res.ok {
				res.firstDiff += "; b's table is empty, so they agree"
			}
			out = append(out, res)
			continue
		}
		if len(bt.pk) == 0 {
			return nil, fmt.Errorf("table %s has no primary key; cannot compare it in order", name)
		}
		// A column a lacks is read as NULL there: equal only where b holds
		// NULL too. Primary-key columns must exist to order the rows.
		absent := map[string]bool{}
		for i, c := range bt.cols {
			if at.colIndex(c.name) >= 0 {
				continue
			}
			if containsInt(bt.pk, i) {
				res.firstDiff = "primary-key column missing on side a: " + c.name
				break
			}
			absent[c.name] = true
			res.nullOnA = append(res.nullOnA, c.name)
		}
		if res.firstDiff != "" {
			out = append(out, res)
			continue
		}
		for _, c := range at.cols {
			if bt.colIndex(c.name) < 0 {
				res.notCompared = append(res.notCompared, c.name)
			}
		}
		if err := compareTable(ctx, a, b, bt, absent, &res); err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

// normRow normalises a raw row into dst for comparison. A value that cannot
// be normalised is kept raw, so it still compares (unequal to anything but
// itself) instead of aborting the comparison.
func normRow(t *table, raw, dst []any) {
	for i, c := range t.cols {
		v, err := normalize(c.class, raw[i], false)
		if err != nil {
			v = raw[i]
		}
		dst[i] = v
	}
}

func compareTable(ctx context.Context, a, b side, t *table, absentOnA map[string]bool, res *tableResult) error {
	cols := t.colNames()
	sa, err := openStream(ctx, a.q, a.kind, t, cols, absentOnA, true)
	if err != nil {
		return fmt.Errorf("side a: %w", err)
	}
	defer sa.close()
	sb, err := openStream(ctx, b.q, b.kind, t, cols, nil, true)
	if err != nil {
		return fmt.Errorf("side b: %w", err)
	}
	defer sb.close()

	ha, hb := sha256.New(), sha256.New()
	na, nb := make([]any, len(cols)), make([]any, len(cols))
	var buf []byte
	hashRow := func(h hash.Hash, row []any) []byte {
		buf = buf[:0]
		for _, v := range row {
			buf = appendValue(buf, v)
		}
		h.Write(buf)
		return buf
	}
	note := func(s string) {
		if res.firstDiff == "" {
			res.firstDiff = s
		}
	}

	advance := func(s *rowStream, dst []any, label string) (bool, error) {
		ok, err := s.next()
		if err != nil {
			return false, fmt.Errorf("side %s: read %s: %w", label, t.name, err)
		}
		if ok {
			normRow(t, s.vals, dst)
		}
		return ok, nil
	}
	okA, err := advance(sa, na, "a")
	if err != nil {
		return err
	}
	okB, err := advance(sb, nb, "b")
	if err != nil {
		return err
	}
	diffs := 0
	for okA || okB {
		c := 0
		switch {
		case !okA:
			c = 1
		case !okB:
			c = -1
		default:
			for _, idx := range t.pk {
				if c = compareValues(na[idx], nb[idx]); c != 0 {
					break
				}
			}
		}
		switch {
		case c < 0: // row only in a
			res.rowsA++
			hashRow(ha, na)
			if unfinishedAsync(t, na) {
				res.unfinishedA++
			} else {
				diffs++
				note(fmt.Sprintf("first difference at %s: row exists on side a only", describePK(t, na)))
			}
			if okA, err = advance(sa, na, "a"); err != nil {
				return err
			}
		case c > 0: // row only in b
			res.rowsB++
			hashRow(hb, nb)
			if bookkeepingTables[t.name] {
				res.extraB++
			} else {
				diffs++
				note(fmt.Sprintf("first difference at %s: row exists on side b only", describePK(t, nb)))
			}
			if okB, err = advance(sb, nb, "b"); err != nil {
				return err
			}
		default:
			res.rowsA++
			res.rowsB++
			ea := string(hashRow(ha, na))
			eb := hashRow(hb, nb)
			if ea != string(eb) && unfinishedAsync(t, na) {
				res.unfinishedA++
			} else if ea != string(eb) {
				diffs++
				for i := range cols {
					if string(appendValue(nil, na[i])) != string(appendValue(nil, nb[i])) {
						note(fmt.Sprintf("first difference at %s: column %s is %s on side a, %s on side b",
							describePK(t, na), cols[i], formatValue(na[i]), formatValue(nb[i])))
						break
					}
				}
			}
			if okA, err = advance(sa, na, "a"); err != nil {
				return err
			}
			if okB, err = advance(sb, nb, "b"); err != nil {
				return err
			}
		}
	}
	res.digestA = hex.EncodeToString(ha.Sum(nil))
	res.digestB = hex.EncodeToString(hb.Sum(nil))
	res.ok = diffs == 0
	return nil
}

// reportResults logs one line per table and returns an error if any differ.
func reportResults(lg *log.Logger, results []tableResult) error {
	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })
	bad := 0
	for _, r := range results {
		mark := "ok"
		if !r.ok {
			mark = "MISMATCH"
			bad++
		}
		line := fmt.Sprintf("verify %-28s %s rows a=%d b=%d", r.name, mark, r.rowsA, r.rowsB)
		if r.digestA != "" {
			if r.digestA == r.digestB {
				line += " digest " + r.digestA[:16]
			} else {
				line += fmt.Sprintf(" digest a=%s b=%s", r.digestA[:16], r.digestB[:16])
			}
		}
		if r.extraB > 0 {
			line += fmt.Sprintf(" (b also holds %d schema-seeded bookkeeping row(s) a lacks; tolerated)", r.extraB)
		}
		if r.unfinishedA > 0 {
			line += fmt.Sprintf(" (a has %d unfinished async migration row(s) that copy does not carry over; tolerated)", r.unfinishedA)
		}
		if r.firstDiff != "" {
			line += " — " + r.firstDiff
		}
		lg.Print(line)
		if len(r.nullOnA) > 0 {
			lg.Printf("verify %s: side a has no column %s; compared as NULL", r.name, strings.Join(r.nullOnA, ", "))
		}
		if len(r.notCompared) > 0 {
			lg.Printf("verify %s: side a has columns b lacks, not compared: %s", r.name, strings.Join(r.notCompared, ", "))
		}
	}
	if bad > 0 {
		return fmt.Errorf("verify: %d of %d tables differ", bad, len(results))
	}
	lg.Printf("verify: all %d tables match", len(results))
	return nil
}

// runVerify implements `corescope-migrate verify -a <db> -b <db>`.
func runVerify(ctx context.Context, args []string, lg *log.Logger, sw *scrubWriter) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(lg.Writer())
	aArg := fs.String("a", "", "first database (sqlite:<path> or postgres://user@host/db)")
	bArg := fs.String("b", "", "second database; its schema is the reference (normally the copy)")
	fs.Usage = usageFor(fs, "verify -a <db> -b <db>",
		"corescope-migrate verify -a sqlite:/var/lib/corescope/meshcore.db -b postgres://corescope@db.example/corescope")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *aArg == "" || *bArg == "" {
		fs.Usage()
		return errUsage
	}
	a, err := parseDBRef(*aArg)
	if err != nil {
		return fmt.Errorf("-a: %w", err)
	}
	b, err := parseDBRef(*bArg)
	if err != nil {
		return fmt.Errorf("-b: %w", err)
	}
	sw.add(a, b)

	sa, closeA, err := openSnapshot(ctx, a)
	if err != nil {
		return err
	}
	defer closeA()
	sb, closeB, err := openSnapshot(ctx, b)
	if err != nil {
		return err
	}
	defer closeB()
	lg.Printf("verifying %s (a) against %s (b)", a.display, b.display)
	results, err := compareTables(ctx, sa, sb)
	if err != nil {
		return err
	}
	return reportResults(lg, results)
}

var errUsage = errors.New("usage")

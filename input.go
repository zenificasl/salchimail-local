package main

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// readRecords reads a CSV (header row required; comma, semicolon or tab) or a
// JSONL file (.jsonl/.ndjson: one object, or one bare string, per line) into
// records keyed by lower-cased column name.
func readRecords(path string) (cols []string, recs []map[string]string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".jsonl") || strings.HasSuffix(lower, ".ndjson") {
		return readJSONL(path, raw)
	}
	firstLine, _, _ := bytes.Cut(raw, []byte("\n"))
	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	for _, sep := range []rune{';', '\t'} {
		if bytes.ContainsRune(firstLine, sep) && !bytes.ContainsRune(firstLine, ',') {
			r.Comma = sep
		}
	}
	rows, err := r.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("%s: empty file", path)
	}
	for _, h := range rows[0] {
		if strings.Contains(h, "@") {
			return nil, nil, fmt.Errorf("%s: the first row must be a header (for example: email), not data", path)
		}
		cols = append(cols, strings.ToLower(strings.TrimSpace(h)))
	}
	for _, row := range rows[1:] {
		rec := map[string]string{}
		for i, v := range row {
			if i < len(cols) {
				rec[cols[i]] = v
			}
		}
		recs = append(recs, rec)
	}
	return cols, recs, nil
}

func readJSONL(path string, raw []byte) (cols []string, recs []map[string]string, err error) {
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(line, &v); err != nil {
			return nil, nil, fmt.Errorf("%s line %d: %w", path, n, err)
		}
		rec := map[string]string{}
		switch v := v.(type) {
		case string:
			rec["email"] = v
		case map[string]any:
			for k, val := range v {
				if val != nil {
					rec[strings.ToLower(k)] = fmt.Sprint(val)
				}
			}
		default:
			return nil, nil, fmt.Errorf("%s line %d: expected an object or a string", path, n)
		}
		for k := range rec {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
		recs = append(recs, rec)
	}
	return cols, recs, sc.Err()
}

func pickColumn(path string, cols []string, want string) (string, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, c := range cols {
		if c == want {
			return c, nil
		}
	}
	return "", fmt.Errorf("%s: no %q column (found: %s)", path, want, strings.Join(cols, ", "))
}

// csvCell stops a spreadsheet from running a cell as a formula, the same way
// the API's CSV export does. Join results back by `row`, not by this text.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

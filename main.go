// claude-thinking-stats reports how much of Claude Code's output was extended
// thinking, straight from the transcripts Claude Code writes under
// ~/.claude/projects. No hooks, no API key, no dependencies; it never writes.
//
// Every assistant line in a transcript carries the API's usage block, and recent
// Claude Code versions include usage.output_tokens_details.thinking_tokens there.
// A response that spans several content blocks is written as several lines with
// the same message.id, so each id counts once.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Record is one API response.
type Record struct {
	When          time.Time `json:"when"`
	Session       string    `json:"session"`
	Project       string    `json:"project"`
	Model         string    `json:"model"`
	Effort        string    `json:"effort"`
	Output        int       `json:"output"`
	Thinking      int       `json:"thinking"`
	ThinkingKnown bool      `json:"thinking_known"` // usage carried a thinking_tokens field
	Input         int       `json:"input"`          // input + cache read + cache creation
}

// Row is one line of the aggregated table.
type Row struct {
	Key      string  `json:"key"`
	Requests int     `json:"requests"`
	Output   int     `json:"output"`
	Thinking int     `json:"thinking"`
	ThinkPct float64 `json:"think_pct"`
	ZeroPct  float64 `json:"zero_pct"`
	P50      int     `json:"p50"`
	P95      int     `json:"p95"`
	Unknown  int     `json:"missing"`
	Models   string  `json:"models"`
}

// transcript line shape (only the fields we read)
type line struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Effort    string `json:"effort"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			OutputTokens             int `json:"output_tokens"`
			InputTokens              int `json:"input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			OutputTokensDetails      *struct {
				ThinkingTokens *int `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"message"`
}

func defaultRoot() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// transcriptFiles expands files/directories into transcript paths. Session
// transcripts sit at <root>/<project>/<session>.jsonl; subagent transcripts at
// <root>/<project>/<session>/subagents/*.jsonl and are only included on request.
func transcriptFiles(paths []string, subagents bool) []string {
	if len(paths) == 0 {
		paths = []string{defaultRoot()}
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "~/") {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, p[2:])
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !st.IsDir() {
			add(p)
			continue
		}
		for _, pat := range []string{"*.jsonl", filepath.Join("*", "*.jsonl")} {
			m, _ := filepath.Glob(filepath.Join(p, pat))
			for _, f := range m {
				add(f)
			}
		}
		if subagents {
			for _, pat := range []string{
				filepath.Join("*", "subagents", "*.jsonl"),
				filepath.Join("*", "*", "subagents", "*.jsonl"),
			} {
				m, _ := filepath.Glob(filepath.Join(p, pat))
				for _, f := range m {
					add(f)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// parseRecords reads one transcript, returning one Record per message id and
// the number of lines it could not read.
func parseRecords(r io.Reader) (records []Record, skipped int) {
	br := bufio.NewReaderSize(r, 1<<20)
	seen := map[string]bool{}
	for {
		// ReadBytes has no line-length cap: a transcript line can carry a whole
		// file or web page, and a Scanner would stop at the first oversized one.
		raw, err := br.ReadBytes('\n')
		if len(raw) == 0 && err != nil {
			if err != io.EOF {
				skipped++
			}
			break
		}
		var l line
		if uerr := json.Unmarshal(raw, &l); uerr != nil {
			skipped++
			if err != nil {
				break
			}
			continue
		}
		if l.Type != "assistant" || l.Message.ID == "" || seen[l.Message.ID] || l.Message.Model == "<synthetic>" {
			if err != nil {
				break
			}
			continue
		}
		when, perr := time.Parse(time.RFC3339Nano, l.Timestamp)
		if perr != nil {
			skipped++
			if err != nil {
				break
			}
			continue
		}
		seen[l.Message.ID] = true
		u := l.Message.Usage
		rec := Record{
			When:    when,
			Session: l.SessionID,
			Project: filepath.Base(l.Cwd),
			Model:   l.Message.Model,
			Effort:  l.Effort,
			Output:  u.OutputTokens,
			Input:   u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		}
		if u.OutputTokensDetails != nil && u.OutputTokensDetails.ThinkingTokens != nil {
			rec.Thinking = *u.OutputTokensDetails.ThinkingTokens
			rec.ThinkingKnown = true
		}
		records = append(records, rec)
		if err != nil { // last line without a trailing newline
			break
		}
	}
	return records, skipped
}

func load(paths []string, subagents bool) (records []Record, skipped int) {
	for _, f := range transcriptFiles(paths, subagents) {
		fh, err := os.Open(f)
		if err != nil {
			skipped++
			continue
		}
		rs, sk := parseRecords(fh)
		fh.Close()
		records = append(records, rs...)
		skipped += sk
	}
	return records, skipped
}

// sinceCutoff turns "7d" / "30d" / "YYYY-MM-DD" / "all" into a local-time
// cutoff. Dates are local midnight so they line up with the --by day buckets.
func sinceCutoff(spec string, now time.Time) (time.Time, bool, error) {
	if spec == "" || spec == "all" {
		return time.Time{}, false, nil
	}
	if strings.HasSuffix(spec, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(spec, "d")); err == nil && n >= 0 {
			return now.AddDate(0, 0, -n), true, nil
		}
	}
	t, err := time.ParseInLocation("2006-01-02", spec, now.Location())
	if err != nil {
		return time.Time{}, false, fmt.Errorf("--since: want 7d, 30d, YYYY-MM-DD or all, got %q", spec)
	}
	return t, true, nil
}

var keyFuncs = map[string]func(Record) string{
	"day":     func(r Record) string { return r.When.Local().Format("2006-01-02") },
	"session": func(r Record) string { return short(r.Session) + " " + r.Project },
	"model":   func(r Record) string { return r.Model },
	"project": func(r Record) string { return r.Project },
	"effort": func(r Record) string {
		if r.Effort == "" {
			return "(none)"
		}
		return r.Effort
	},
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// percentile is the linearly interpolated p-th percentile (numpy's default,
// Python's statistics.quantiles(method="inclusive")). Sorts a copy.
func percentile(values []int, p float64) int {
	if len(values) == 0 {
		return 0
	}
	v := append([]int(nil), values...)
	sort.Ints(v)
	pos := float64(len(v)-1) * p / 100
	lo := int(pos)
	if lo >= len(v)-1 {
		return v[len(v)-1]
	}
	frac := pos - float64(lo)
	return int(float64(v[lo]) + frac*float64(v[lo+1]-v[lo]))
}

func aggregate(records []Record, by string) []Row {
	key := keyFuncs[by]
	groups := map[string][]Record{}
	for _, r := range records {
		k := key(r)
		groups[k] = append(groups[k], r)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]Row, 0, len(keys))
	for _, k := range keys {
		rs := groups[k]
		row := Row{Key: k, Requests: len(rs)}
		think := make([]int, 0, len(rs))
		models := map[string]bool{}
		zero := 0
		for _, r := range rs {
			row.Output += r.Output
			row.Thinking += r.Thinking
			think = append(think, r.Thinking)
			models[r.Model] = true
			if r.Thinking == 0 {
				zero++
			}
			if !r.ThinkingKnown {
				row.Unknown++
			}
		}
		if row.Output > 0 {
			row.ThinkPct = round1(100 * float64(row.Thinking) / float64(row.Output))
		}
		row.ZeroPct = round1(100 * float64(zero) / float64(len(rs)))
		row.P50 = percentile(think, 50)
		row.P95 = percentile(think, 95)
		names := make([]string, 0, len(models))
		for m := range models {
			names = append(names, m)
		}
		sort.Strings(names)
		row.Models = strings.Join(names, ",")
		rows = append(rows, row)
	}
	return rows
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

// --- output ------------------------------------------------------------------

func comma(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func formatTable(rows []Row, by string) string {
	if len(rows) == 0 {
		return "no responses in range"
	}
	cols := []string{by, "requests", "output", "thinking", "think%", "zero%", "p50", "p95", "missing", "models"}
	data := make([][]string, len(rows))
	for i, r := range rows {
		data[i] = []string{r.Key, comma(r.Requests), comma(r.Output), comma(r.Thinking),
			fmt.Sprintf("%.1f", r.ThinkPct), fmt.Sprintf("%.1f", r.ZeroPct), comma(r.P50), comma(r.P95), comma(r.Unknown), r.Models}
	}
	if by == "model" { // the key already is the model; the models column would just repeat it
		cols = cols[:len(cols)-1]
		for i := range data {
			data[i] = data[i][:len(data[i])-1]
		}
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len(c)
		for _, d := range data {
			if len(d[i]) > widths[i] {
				widths[i] = len(d[i])
			}
		}
	}
	fmtLine := func(cells []string) string {
		parts := make([]string, len(cells))
		for i, c := range cells {
			if i == 0 || i == len(cells)-1 {
				parts[i] = c + strings.Repeat(" ", widths[i]-len(c))
			} else {
				parts[i] = strings.Repeat(" ", widths[i]-len(c)) + c
			}
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	out := []string{fmtLine(cols)}
	for _, d := range data {
		out = append(out, fmtLine(d))
	}
	return strings.Join(out, "\n")
}

func topRecords(records []Record, n int) []Record {
	v := append([]Record(nil), records...)
	sort.SliceStable(v, func(i, j int) bool { return v[i].Thinking > v[j].Thinking })
	if len(v) > n {
		v = v[:n]
	}
	return v
}

func formatTop(records []Record) string {
	if len(records) == 0 {
		return "no responses in range"
	}
	out := []string{"thinking  output  when                 session   project"}
	for _, r := range records {
		out = append(out, fmt.Sprintf("%8s  %6s  %s  %-8s  %s", comma(r.Thinking), comma(r.Output),
			r.When.Local().Format("2006-01-02 15:04:05"), short(r.Session), r.Project))
	}
	return strings.Join(out, "\n")
}

func main() {
	by := flag.String("by", "day", "group rows by: day, session, model, project, effort")
	since := flag.String("since", "7d", "7d, 30d, YYYY-MM-DD (local dates) or all")
	top := flag.Int("top", 0, "list the N responses with the most thinking instead of a table")
	subagents := flag.Bool("subagents", false, "include subagent transcripts")
	asJSON := flag.Bool("json", false, "output JSON")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: claude-thinking-stats [flags] [transcript files or directories]\n\n"+
			"Reads Claude Code transcripts (default: %s) and reports thinking-token usage.\n\n", defaultRoot())
		flag.PrintDefaults()
	}
	flag.Parse()
	if _, ok := keyFuncs[*by]; !ok {
		fmt.Fprintf(os.Stderr, "--by: want day, session, model, project or effort, got %q\n", *by)
		os.Exit(2)
	}
	cutoff, hasCutoff, err := sinceCutoff(*since, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	records, skipped := load(flag.Args(), *subagents)
	if hasCutoff {
		kept := records[:0]
		for _, r := range records {
			if !r.When.Before(cutoff) {
				kept = append(kept, r)
			}
		}
		records = kept
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if *top > 0 {
		t := topRecords(records, *top)
		if *asJSON {
			enc.Encode(t)
		} else {
			fmt.Println(formatTop(t))
		}
	} else {
		rows := aggregate(records, *by)
		if *asJSON {
			enc.Encode(rows)
		} else {
			fmt.Println(formatTable(rows, *by))
		}
	}

	unknown := 0
	for _, r := range records {
		if !r.ThinkingKnown {
			unknown++
		}
	}
	var notes []string
	if unknown > 0 {
		notes = append(notes, fmt.Sprintf("%d responses had no thinking_tokens field (counted as 0; see the missing column)", unknown))
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d unreadable lines/files skipped", skipped))
	}
	if len(notes) > 0 { // stderr, so -json stdout stays clean
		fmt.Fprintln(os.Stderr, "note: "+strings.Join(notes, "; "))
	}
}

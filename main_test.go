package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rec(id, ts string, output int, thinking *int, model string) string {
	usage := map[string]any{"output_tokens": output, "input_tokens": 1, "cache_read_input_tokens": 2, "cache_creation_input_tokens": 3}
	if thinking != nil {
		usage["output_tokens_details"] = map[string]any{"thinking_tokens": *thinking}
	}
	b, _ := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": ts, "sessionId": "s1", "cwd": "/p/proj", "effort": "high",
		"message": map[string]any{"id": id, "model": model, "usage": usage},
	})
	return string(b)
}

func ip(n int) *int { return &n }

var lines = strings.Join([]string{
	rec("m1", "2026-09-20T10:00:00.000Z", 100, ip(40), "claude-x"),
	rec("m1", "2026-09-20T10:00:00.000Z", 100, ip(40), "claude-x"), // second content block, same id
	rec("m2", "2026-09-20T11:00:00.000Z", 200, ip(0), "claude-x"),
	rec("m3", "2026-09-10T11:00:00.000Z", 300, ip(150), "claude-x"), // old
	rec("m4", "2026-09-20T12:00:00.000Z", 50, nil, "claude-x"),      // no thinking_tokens field
	rec("m5", "2026-09-20T12:30:00.000Z", 10, ip(0), "<synthetic>"),
	`{"type":"user","message":{"id":"u1"}}`,
	`{not json`,
	`{"type":"assistant","timestamp":"garbage","message":{"id":"m6","usage":{"output_tokens":1}}}`,
}, "\n") + "\n"

func TestParseRecords(t *testing.T) {
	records, skipped := parseRecords(strings.NewReader(lines))
	if len(records) != 4 {
		t.Fatalf("want 4 records (deduped, synthetic dropped), got %d", len(records))
	}
	if skipped != 2 {
		t.Errorf("want 2 skipped (bad json, bad timestamp), got %d", skipped)
	}
	if records[0].Input != 6 || records[0].Project != "proj" || records[0].Thinking != 40 || !records[0].ThinkingKnown {
		t.Errorf("first record wrong: %+v", records[0])
	}
	m4 := records[3]
	if m4.Output != 50 || m4.Thinking != 0 || m4.ThinkingKnown {
		t.Errorf("record without thinking_tokens should be 0/unknown: %+v", m4)
	}
}

func TestAggregateNumbers(t *testing.T) {
	records, _ := parseRecords(strings.NewReader(lines))
	rows := aggregate(records, "model")
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Requests != 4 || r.Output != 650 || r.Thinking != 190 || r.Unknown != 1 {
		t.Errorf("totals wrong: %+v", r)
	}
	if r.ThinkPct != 29.2 || r.ZeroPct != 50 {
		t.Errorf("percentages wrong: think=%v zero=%v", r.ThinkPct, r.ZeroPct)
	}
	// linear interpolation over sorted [0, 0, 40, 150]: p50 -> 20, p95 -> 133.5 -> 133
	if r.P50 != 20 || r.P95 != 133 {
		t.Errorf("percentiles wrong: p50=%d p95=%d", r.P50, r.P95)
	}
}

func TestPercentileEdges(t *testing.T) {
	if percentile(nil, 50) != 0 || percentile([]int{7}, 95) != 7 || percentile([]int{3, 1}, 100) != 3 {
		t.Error("edge cases")
	}
}

func TestSinceCutoffIsLocal(t *testing.T) {
	loc := time.FixedZone("JST", 9*3600)
	now := time.Date(2026, 9, 21, 15, 0, 0, 0, loc)
	c, ok, err := sinceCutoff("2026-09-20", now)
	if err != nil || !ok || !c.Equal(time.Date(2026, 9, 20, 0, 0, 0, 0, loc)) {
		t.Errorf("date cutoff should be local midnight, got %v %v %v", c, ok, err)
	}
	c, _, _ = sinceCutoff("7d", now)
	if !c.Equal(now.AddDate(0, 0, -7)) {
		t.Errorf("7d cutoff wrong: %v", c)
	}
	if _, ok, _ := sinceCutoff("all", now); ok {
		t.Error("all should mean no cutoff")
	}
	if _, _, err := sinceCutoff("nope", now); err == nil {
		t.Error("garbage should error")
	}
}

func TestTopOrder(t *testing.T) {
	records, _ := parseRecords(strings.NewReader(lines))
	top := topRecords(records, 2)
	if len(top) != 2 || top[0].Thinking != 150 || top[1].Thinking != 40 {
		t.Errorf("top order wrong: %+v", top)
	}
}

func TestSubagentsOnlyWithFlag(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "proj", "sess", "subagents"), 0o755)
	os.WriteFile(filepath.Join(d, "proj", "sess.jsonl"), []byte(rec("a", "2026-09-20T10:00:00Z", 1, ip(1), "m")+"\n"), 0o644)
	os.WriteFile(filepath.Join(d, "proj", "sess", "subagents", "agent-1.jsonl"), []byte(rec("b", "2026-09-20T10:00:00Z", 1, ip(1), "m")+"\n"), 0o644)
	if rs, _ := load([]string{d}, false); len(rs) != 1 {
		t.Errorf("without -subagents want 1, got %d", len(rs))
	}
	if rs, _ := load([]string{d}, true); len(rs) != 2 {
		t.Errorf("with -subagents want 2, got %d", len(rs))
	}
}

func TestFormatTableAndComma(t *testing.T) {
	if comma(1234567) != "1,234,567" || comma(999) != "999" || comma(1000) != "1,000" {
		t.Error("comma")
	}
	records, _ := parseRecords(strings.NewReader(lines))
	out := formatTable(aggregate(records, "model"), "model")
	if !strings.Contains(out, "claude-x") || !strings.Contains(out, "missing") {
		t.Errorf("table missing columns:\n%s", out)
	}
	if formatTable(nil, "day") != "no responses in range" {
		t.Error("empty table")
	}
	byModel := formatTable(aggregate(records, "model"), "model")
	if strings.Contains(byModel, "models") || strings.Count(byModel, "claude-x") != 1 {
		t.Errorf("-by model should not repeat the model in a models column:\n%s", byModel)
	}
	if !strings.Contains(formatTable(aggregate(records, "day"), "day"), "models") {
		t.Error("-by day should keep the models column")
	}
}

func TestHugeLineDoesNotStopParsing(t *testing.T) {
	// a 3 MB line (bigger than the reader buffer) followed by a normal one
	big := rec("big", "2026-09-20T10:00:00Z", 1, ip(1), "m")
	big = big[:len(big)-1] + `,"pad":"` + strings.Repeat("x", 3<<20) + `"}`
	in := big + "\n" + rec("after", "2026-09-20T10:00:00Z", 2, ip(2), "m") // no trailing newline
	records, skipped := parseRecords(strings.NewReader(in))
	if len(records) != 2 || skipped != 0 {
		t.Errorf("want 2 records / 0 skipped, got %d / %d", len(records), skipped)
	}
}

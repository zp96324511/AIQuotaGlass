package providers

import (
	"strings"
	"testing"
)

func openCodeRecord(seq, input, output, reasoning, cacheRead, cost string) string {
	return `timeCreated:$R[` + seq + `]=new Date("2026-08-03T08:00:00.000Z"),` +
		`timeUpdated:$R[` + seq + `]=new Date("2026-08-03T08:00:01.000Z"),timeDeleted:null,` +
		`model:"gpt-4o",provider:"openai",` +
		`inputTokens:` + input + `,outputTokens:` + output + `,reasoningTokens:` + reasoning + `,` +
		`cacheReadTokens:` + cacheRead + `,cacheWrite5mTokens:0,cacheWrite1hTokens:0,` +
		`cost:` + cost + `,keyID:"key-1",sessionID:"sess-1"`
}

func TestParseOpenCodeGoDetail_aggregates_records(t *testing.T) {
	body := []byte(strings.Join([]string{
		openCodeRecord("0", "100", "50", "0", "300", "2500000"),
		openCodeRecord("9", "200", "100", "10", "0", "5000000"),
	}, "\n"))

	d, err := parseOpenCodeGoDetail(body)
	if err != nil {
		t.Fatalf("parseOpenCodeGoDetail: %v", err)
	}
	if got, want := d.Requests, 2; got != want {
		t.Fatalf("requests = %d, want %d", got, want)
	}
	if got, want := d.Cost, 0.075; !floatEqual(got, want) {
		t.Fatalf("cost = %v, want %v", got, want)
	}
	if got, want := d.CacheHit, 50.0; !floatEqual(got, want) {
		t.Fatalf("cacheHit = %v, want %v", got, want)
	}
	if !d.HasUsageMetrics() {
		t.Fatal("parsed records must mark usage metrics available")
	}
}

func floatEqual(left, right float64) bool {
	diff := left - right
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1e-9
}

func TestParseOpenCodeGoDetail_requires_records(t *testing.T) {
	if _, err := parseOpenCodeGoDetail([]byte("<html>no records here</html>")); err == nil {
		t.Fatal("page without usage records must return an error, not a valid zero detail")
	}
}

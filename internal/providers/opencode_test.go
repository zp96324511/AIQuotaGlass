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

func TestWindowsRegex_matches_float_percent_with_extra_fields(t *testing.T) {
	// OpenCode Go console now serializes usagePercent as a float (e.g. 2.1)
	// and appends usage/limit fields after it. Both changes previously broke
	// reWindows (int-only percent + strict closing brace).
	body := []byte(`$R[33]=` + "`" + `{mine:!0,useBalance:!1,allowTraining:!1,region:$R[34]=["us","eu","sg","cn"],` +
		`rollingUsage:$R[35]={status:"ok",resetInSec:9695,usagePercent:2.1,usage:24603408,limit:1200000000},` +
		`weeklyUsage:$R[36]={status:"ok",resetInSec:333444,usagePercent:7.1,usage:212383754,limit:3000000000},` +
		`monthlyUsage:$R[37]={status:"ok",resetInSec:1565451,usagePercent:43.8,usage:262800000,limit:600000000}}` + "`")

	m := reWindows.FindSubmatch(body)
	if m == nil {
		t.Fatal("reWindows must match the new float-percent payload")
	}
	if got, want := string(m[2]), "9695"; got != want {
		t.Fatalf("rolling resetInSec = %q, want %q", got, want)
	}
	if got, want := string(m[3]), "2.1"; got != want {
		t.Fatalf("rolling usagePercent = %q, want %q", got, want)
	}
	if got, want := string(m[6]), "7.1"; got != want {
		t.Fatalf("weekly usagePercent = %q, want %q", got, want)
	}
	if got, want := string(m[9]), "43.8"; got != want {
		t.Fatalf("monthly usagePercent = %q, want %q", got, want)
	}
	if f := parseFloat(m[3]); f != 2.1 {
		t.Fatalf("parseFloat(2.1) = %v, want 2.1", f)
	}
}

func TestWindowsRegex_matches_integer_percent_payload(t *testing.T) {
	body := []byte(`$R[16]($R[30],$R[41]={rollingUsage:$R[42]={status:"ok",resetInSec:5944,usagePercent:17},` +
		`weeklyUsage:$R[43]={status:"ok",resetInSec:278201,usagePercent:75},` +
		`monthlyUsage:$R[44]={status:"ok",resetInSec:880201,usagePercent:91}});`)
	if reWindows.FindSubmatch(body) == nil {
		t.Fatal("reWindows must keep matching the legacy integer-percent payload")
	}
}

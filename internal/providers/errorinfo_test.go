package providers

import "testing"

func TestTruncatedErrorBody_strips_control_and_compacts_whitespace(t *testing.T) {
	in := []byte("{\"error\":\"bad key\"}\n\n<html>\r\n<body>oops</body>\r\n</html>\r\n")
	got := truncatedErrorBody(in)
	want := `{"error":"bad key"} <html> <body>oops</body> </html>`
	if got != want {
		t.Fatalf("truncatedErrorBody = %q, want %q", got, want)
	}
}

func TestTruncatedErrorBody_drops_ansi_escapes(t *testing.T) {
	in := []byte("plain \x1b[31mred\x1b[0m tail")
	if got, want := truncatedErrorBody(in), "plain red tail"; got != want {
		t.Fatalf("truncatedErrorBody = %q, want %q", got, want)
	}
}

func TestTruncatedErrorBody_caps_length(t *testing.T) {
	in := make([]byte, 0, 2000)
	for i := 0; i < 2000; i++ {
		in = append(in, 'a')
	}
	got := truncatedErrorBody(in)
	const ellipsis = "\u2026" // 3 bytes in UTF-8
	if len(got) != 500+len(ellipsis) {
		t.Fatalf("truncatedErrorBody length = %d, want %d", len(got), 500+len(ellipsis))
	}
	if got[len(got)-len(ellipsis):] != ellipsis {
		t.Fatalf("truncatedErrorBody must end with ellipsis, got suffix %q", got[len(got)-3:])
	}
}

func TestTruncatedErrorBody_empty_and_binary(t *testing.T) {
	if got := truncatedErrorBody(nil); got != "" {
		t.Fatalf("empty body = %q, want empty", got)
	}
	if got := truncatedErrorBody([]byte{0x00, 0x01, 0x02, '\n'}); got != "" {
		t.Fatalf("binary body = %q, want empty", got)
	}
}

func TestHTTPErrorInfo_assembles_request_context(t *testing.T) {
	info := httpErrorInfo("GET", "https://example.com/api/usage", 429, []byte("rate limited\nretry later"))
	if info.Method != "GET" || info.URL != "https://example.com/api/usage" || info.StatusCode != 429 {
		t.Fatalf("httpErrorInfo = %+v, want method/url/status preserved", info)
	}
	if got, want := info.Body, "rate limited retry later"; got != want {
		t.Fatalf("info.Body = %q, want %q", got, want)
	}
}

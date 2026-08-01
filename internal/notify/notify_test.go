package notify

import (
	"testing"
	"unicode/utf8"
)

func TestCopyUTF16_truncatesToFit(t *testing.T) {
	dst := make([]uint16, 5)
	long := "abcdefghij"
	copyUTF16(dst, long)
	// dst has 5 slots; last reserved for null → 4 chars fit
	got := utf16ToString(dst)
	if got != "abcd" {
		t.Fatalf("expected 'abcd', got %q", got)
	}
}

func TestCopyUTF16_handlesEmptyString(t *testing.T) {
	dst := make([]uint16, 8)
	copyUTF16(dst, "")
	if dst[0] != 0 {
		t.Fatalf("expected null terminator at [0], got %d", dst[0])
	}
}

func TestCopyUTF16_replacesNullRune(t *testing.T) {
	dst := make([]uint16, 8)
	copyUTF16(dst, "a\x00b")
	got := utf16ToString(dst)
	if got != "a b" {
		t.Fatalf("expected 'a b', got %q", got)
	}
}

func TestCopyUTF16_truncatesRunesNotBytes(t *testing.T) {
	// Each Chinese rune is 1 UTF-16 code unit; buffer of 4 → 3 chars + null
	dst := make([]uint16, 4)
	copyUTF16(dst, "用量告警测试")
	got := utf16ToString(dst)
	if got != "用量告" {
		t.Fatalf("expected '用量告', got %q", got)
	}
}

// utf16ToString decodes the null-terminated portion of a UTF-16 buffer.
func utf16ToString(buf []uint16) string {
	runes := make([]rune, 0, len(buf))
	for _, c := range buf {
		if c == 0 {
			break
		}
		runes = append(runes, rune(c))
	}
	return string(runes)
}

// Verify utf16ToString does not panic on empty buffer.
func TestUtf16ToString_empty(t *testing.T) {
	if s := utf16ToString(nil); s != "" {
		t.Fatalf("expected empty string, got %q", s)
	}
}

func TestUtf8Valid(t *testing.T) {
	// sanity: rune handling is correct
	if utf8.RuneLen('用') != 3 {
		t.Fatal("expected 3-byte rune for 用")
	}
}

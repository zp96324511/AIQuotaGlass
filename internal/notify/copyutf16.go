package notify

// copyUTF16 fills dst with the UTF-16 encoding of s, truncated to fit
// (including the null terminator). It is safe for concurrent use because
// each caller passes its own dst slice. This is pure logic with no syscall
// dependency, so it compiles and tests on all platforms.
func copyUTF16(dst []uint16, s string) {
	runes := []rune(s)
	i := 0
	for _, r := range runes {
		if i+1 >= len(dst) { // reserve null terminator
			break
		}
		c := uint16(r)
		if c == 0 {
			c = ' ' // avoid premature null inside fixed buffer
		}
		dst[i] = c
		i++
	}
	if len(dst) > 0 {
		dst[i] = 0
	}
}

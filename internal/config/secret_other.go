//go:build !windows

package config

// Encrypt on non-Windows platforms stores the value as plain base64.
func Encrypt(plain []byte) (string, error) {
	if len(plain) == 0 {
		return "", nil
	}
	return "plain:" + encodeBase64(plain), nil
}

// Decrypt on non-Windows platforms reverses Encrypt.
func Decrypt(enc string) ([]byte, error) {
	if enc == "" {
		return nil, nil
	}
	if len(enc) > 6 && enc[:6] == "plain:" {
		return decodeBase64(enc[6:])
	}
	return []byte(enc), nil
}

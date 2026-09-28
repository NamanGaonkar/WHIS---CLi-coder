package provider

import "testing"

// Keys arriving from the clipboard/terminal must never carry invisible junk
// that makes providers reject a perfectly good key (401 confusion).
func TestSanitizeKey(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"sk-or-v1-abc", "sk-or-v1-abc"},         // valid: byte-identical
		{"  sk-or-v1-abc \n", "sk-or-v1-abc"},    // paste whitespace
		{"\"sk-or-v1-abc\"", "sk-or-v1-abc"},     // surrounding quotes
		{"'sk-or-v1-abc'", "sk-or-v1-abc"},       // single quotes
		{"sk-\u200b-or-v1-abc", "sk--or-v1-abc"}, // zero-width space stripped
		{"sk-or-v1-abc\uFEFF", "sk-or-v1-abc"},   // BOM stripped
		{"sk-or-v1-abc\r\n", "sk-or-v1-abc"},     // CRLF
		{"sk-abc\u00e9", "sk-abc\u00e9"},         // printable non-ASCII kept
	} {
		if got := sanitizeKey(c.in); got != c.want {
			t.Errorf("sanitizeKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

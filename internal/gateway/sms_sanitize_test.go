package gateway

import "testing"

func TestSanitizeSMSText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain ASCII", "Hello world", "Hello world"},
		{"brackets replaced", "[MeshSat] test", "(MeshSat) test"},
		{"curly braces", "{key: value}", "(key: value)"},
		{"pipe and backslash", "a|b\\c", "a/b/c"},
		{"tilde and caret", "~home ^ptr", "-home 'ptr"},
		{"euro sign", "Price: 5€", "Price: 5EUR"},
		{"emoji replaced", "Hello 🌍!", "Hello ?!"},
		{"mixed safe and unsafe", "MeshSat !08abcdef ch0: test[1]", "MeshSat !08abcdef ch0: test(1)"},
		{"GSM special chars preserved", "café résumé", "café résumé"},
		{"empty string", "", ""},
		{"all GSM basic punct", "Hello, world! @#$%&*()+-=:;\"'<>?/.", "Hello, world! @#$%&*()+-=:;\"'<>?/."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeSMSText(tt.in)
			if got != tt.want {
				t.Errorf("SanitizeSMSText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsGSMSafe(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain ASCII", "Hello world", true},
		{"base64 standard", "DHwK3nAbCdEfGh+/xy==", true},
		{"base64 all chars", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=", true},
		{"brackets unsafe", "[test]", false},
		{"curly braces", "{test}", false},
		{"pipe", "a|b", false},
		{"backslash", "a\\b", false},
		{"tilde", "~test", false},
		{"caret", "^test", false},
		{"euro", "5€", false},
		{"emoji", "Hello 🌍", false},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsGSMSafe(tt.in)
			if got != tt.want {
				t.Errorf("IsGSMSafe(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

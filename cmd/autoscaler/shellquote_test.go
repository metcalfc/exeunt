package main

import (
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple string", "hello", "'hello'"},
		{"empty string", "", "''"},
		{"with spaces", "hello world", "'hello world'"},
		{"with double quotes", `say "hi"`, `'say "hi"'`},
		{"with single quote", "it's", `'it'\''s'`},
		{"with backticks", "echo `whoami`", "'echo `whoami`'"},
		{"with dollar sign", "echo $HOME", "'echo $HOME'"},
		{"with command substitution", "$(rm -rf /)", "'$(rm -rf /)'"},
		{"with semicolon", "a; rm -rf /", "'a; rm -rf /'"},
		{"with newline", "line1\nline2", "'line1\nline2'"},
		{"base64 typical JIT config", "eyJzY2hlbWEiOiIxIn0=", "'eyJzY2hlbWEiOiIxIn0='"},
		{"multiple single quotes", "a'b'c'd", `'a'\''b'\''c'\''d'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellQuote(tt.input)
			if got != tt.expected {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

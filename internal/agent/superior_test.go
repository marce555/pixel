package agent

import (
	"testing"
)

func TestIsSongDuplicate(t *testing.T) {
	tests := []struct {
		candidate string
		existing  string
		expected  bool
	}{
		{
			candidate: "Los Cierra Bares - Desde tu ventana (Official)",
			existing:  "Los Cierra Bares - desde tu ventana (Official)",
			expected:  true,
		},
		{
			candidate: "Los Cierra Bares - Desde tu ventana",
			existing:  "Los Cierra Bares - desde tu ventana (Official Video)",
			expected:  true,
		},
		{
			candidate: "Desde tu ventana",
			existing:  "Los Cierra Bares - Desde tu ventana",
			expected:  true,
		},
		{
			candidate: "Soda Stereo - De Música Ligera",
			existing:  "Los Cierra Bares - Desde tu ventana",
			expected:  false,
		},
		{
			candidate: "Whitney Houston - I Wanna Dance with Somebody",
			existing:  "Madonna - Material Girl",
			expected:  false,
		},
		{
			candidate: "Los Prisioneros - Tren al sur",
			existing:  "Los Prisioneros - Tren al Sur (Remastered)",
			expected:  true,
		},
	}

	for _, tt := range tests {
		got := isSongDuplicate(tt.candidate, tt.existing)
		if got != tt.expected {
			t.Errorf("isSongDuplicate(%q, %q) = %v; want %v", tt.candidate, tt.existing, got, tt.expected)
		}
	}
}

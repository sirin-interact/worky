package protocol

import "testing"

func TestNegotiate(t *testing.T) {
	tests := []struct {
		name           string
		client, daemon VersionRange
		want           int
		ok             bool
	}{
		{"same single version", VersionRange{1, 1}, VersionRange{1, 1}, 1, true},
		{"newer client, older daemon", VersionRange{1, 3}, VersionRange{1, 2}, 2, true},
		{"older client, newer daemon", VersionRange{1, 2}, VersionRange{2, 4}, 2, true},
		{"client too new", VersionRange{3, 4}, VersionRange{1, 2}, 0, false},
		{"client too old", VersionRange{1, 1}, VersionRange{2, 3}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Negotiate(tt.client, tt.daemon)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("Negotiate = %d, %v; want %d, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

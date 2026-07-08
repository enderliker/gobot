package ai

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestMemberMatchScore(t *testing.T) {
	member := &discordgo.Member{
		Nick: "Johnny Danger",
		User: &discordgo.User{
			ID:       "12345678901234567",
			Username: "john_doe",
		},
	}

	tests := []struct {
		query string
		min   float64
	}{
		{query: "12345678901234567", min: 1},
		{query: "john", min: 0.9},
		{query: "jd", min: 0.8},
		{query: "jonny dnger", min: 0.75},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := memberMatchScore(tt.query, member); got < tt.min {
				t.Fatalf("expected score >= %.2f, got %.2f", tt.min, got)
			}
		})
	}
}

func TestMemberSearchQueries(t *testing.T) {
	queries := memberSearchQueries("@John Doe")
	if len(queries) == 0 {
		t.Fatal("expected search queries to be generated")
	}
	if queries[0] != "@John Doe" {
		t.Fatalf("expected original query first, got %q", queries[0])
	}
}

func TestAliasInitials(t *testing.T) {
	if got := aliasInitials("John Danger Doe"); got != "jdd" {
		t.Fatalf("expected initials jdd, got %q", got)
	}
}

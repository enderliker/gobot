package ai

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

const (
	minMemberMatchScore = 0.75
	maxMemberSearchREST = 50
)

type MemberCandidate struct {
	Member *discordgo.Member
	Score  float64
}

func ResolveMembers(ctx context.Context, session *discordgo.Session, guildID, query string) ([]MemberCandidate, error) {
	query = normalizeUserReference(query)
	if query == "" {
		return nil, fmt.Errorf("member query is empty")
	}

	membersByID := make(map[string]*discordgo.Member)
	addMember := func(member *discordgo.Member) {
		if member == nil || member.User == nil || member.User.ID == "" {
			return
		}
		if current, ok := membersByID[member.User.ID]; ok {
			if current.Nick == "" && member.Nick != "" {
				membersByID[member.User.ID] = member
			}
			return
		}
		membersByID[member.User.ID] = member
	}

	if guild, err := session.State.Guild(guildID); err == nil && guild != nil {
		for _, member := range guild.Members {
			addMember(member)
		}
	}

	var firstErr error
	if isDiscordID(query) {
		member, err := session.GuildMember(guildID, query)
		if err == nil {
			addMember(member)
		} else {
			firstErr = err
		}
	}

	for _, searchQuery := range memberSearchQueries(query) {
		if utf8.RuneCountInString(searchQuery) < 3 {
			continue
		}
		members, err := session.GuildMembersSearch(guildID, searchQuery, maxMemberSearchREST, discordgo.WithContext(ctx))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, member := range members {
			addMember(member)
		}
	}

	var candidates []MemberCandidate
	for _, member := range membersByID {
		score := memberMatchScore(query, member)
		if score < minMemberMatchScore {
			continue
		}
		candidates = append(candidates, MemberCandidate{
			Member: member,
			Score:  score,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return strings.ToLower(candidates[i].Member.DisplayName()) < strings.ToLower(candidates[j].Member.DisplayName())
		}
		return candidates[i].Score > candidates[j].Score
	})

	if len(candidates) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return candidates, nil
}

func memberSearchQueries(query string) []string {
	if isDiscordID(query) {
		return nil
	}

	seen := make(map[string]struct{})
	var out []string

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	add(query)
	add(strings.TrimPrefix(query, "@"))

	normalized := normalizeLookupText(query)
	add(normalized)

	tokens := strings.Fields(normalized)
	if len(tokens) > 0 {
		add(tokens[0])
	}

	return out
}

func memberMatchScore(query string, member *discordgo.Member) float64 {
	if member == nil || member.User == nil {
		return 0
	}

	query = normalizeUserReference(query)
	if query == "" {
		return 0
	}

	if isDiscordID(query) {
		switch {
		case member.User.ID == query:
			return 1
		case strings.HasPrefix(member.User.ID, query):
			return 0.98
		case strings.Contains(member.User.ID, query):
			return 0.9
		}
	}

	best := 0.0
	for _, alias := range memberAliases(member) {
		if alias == "" {
			continue
		}
		best = maxFloat(best, scoreAliasMatch(query, alias))
	}

	return best
}

func memberAliases(member *discordgo.Member) []string {
	if member == nil || member.User == nil {
		return nil
	}

	aliases := []string{
		member.DisplayName(),
		member.Nick,
		member.User.DisplayName(),
		member.User.GlobalName,
		member.User.Username,
		member.User.Username + member.User.Discriminator,
	}

	seen := make(map[string]struct{}, len(aliases))
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		out = append(out, alias)
	}
	return out
}

func scoreAliasMatch(query, alias string) float64 {
	q := normalizeLookupText(query)
	a := normalizeLookupText(alias)
	if q == "" || a == "" {
		return 0
	}

	qCompact := compactLookupText(q)
	aCompact := compactLookupText(a)

	switch {
	case q == a || qCompact == aCompact:
		return 1
	case strings.HasPrefix(a, q) || strings.HasPrefix(aCompact, qCompact):
		return 0.96
	case hasTokenPrefix(a, q):
		return 0.93
	case strings.Contains(a, q) || strings.Contains(aCompact, qCompact):
		return 0.86
	}

	initials := aliasInitials(alias)
	switch {
	case initials != "" && q == initials:
		return 0.9
	case initials != "" && strings.HasPrefix(initials, q):
		return 0.82
	}

	return maxFloat(
		levenshteinSimilarity(q, a),
		levenshteinSimilarity(qCompact, aCompact),
	)
}

func hasTokenPrefix(alias, query string) bool {
	for _, token := range strings.Fields(alias) {
		if strings.HasPrefix(token, query) {
			return true
		}
	}
	return false
}

func normalizeLookupText(s string) string {
	var b strings.Builder
	lastSpace := true

	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case !lastSpace:
			b.WriteByte(' ')
			lastSpace = true
		}
	}

	return strings.TrimSpace(b.String())
}

func compactLookupText(s string) string {
	return strings.ReplaceAll(s, " ", "")
}

func aliasInitials(alias string) string {
	var b strings.Builder
	for _, token := range strings.Fields(normalizeLookupText(alias)) {
		if token == "" {
			continue
		}
		b.WriteByte(token[0])
	}
	return b.String()
}

func levenshteinSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}

	ar := []rune(a)
	br := []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)

	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 0
			if ar[i-1] != br[j-1] {
				cost = 1
			}

			curr[j] = minInt(
				prev[j]+1,
				curr[j-1]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}

	distance := prev[len(br)]
	maxLen := len(ar)
	if len(br) > maxLen {
		maxLen = len(br)
	}
	if maxLen == 0 {
		return 1
	}
	return 1 - float64(distance)/float64(maxLen)
}

func minInt(values ...int) int {
	best := values[0]
	for _, value := range values[1:] {
		if value < best {
			best = value
		}
	}
	return best
}

func maxFloat(values ...float64) float64 {
	best := values[0]
	for _, value := range values[1:] {
		if value > best {
			best = value
		}
	}
	return best
}

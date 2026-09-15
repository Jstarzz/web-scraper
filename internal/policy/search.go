package policy

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const SearchPolicyVersion = "search-v1"

const (
	maxQueryRunes = 240
	maxQueryTerms = 40
)

type Rejection struct {
	Code    string
	Message string
}

type SearchDecision struct {
	Query      string
	Normalized bool
	Rejection  *Rejection
}

var (
	urlPattern = regexp.MustCompile(`(?i)\b(?:https?|ftp|file)://|\bwww\.[^\s]+`)
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
		regexp.MustCompile(`\bws_live_[A-Za-z0-9_-]{16,}\b`),
		regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/\-]{16,}\b`),
		regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	}
	instructionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bignore\s+(?:(?:all|any)\s+)?(?:previous|prior|above|system|developer)\s+(?:instructions?|prompts?|messages?|rules?)\b`),
		regexp.MustCompile(`(?i)\b(?:system|developer)\s+(?:prompt|message|instructions?)\b`),
		regexp.MustCompile(`(?i)\b(?:reveal|show|print|dump|expose|return)\b.{0,48}\b(?:secret|token|api[ _-]?key|credentials?|environment|env(?:ironment)? variables?)\b`),
		regexp.MustCompile(`(?i)\b(?:jailbreak|prompt\s+injection|bypass\s+(?:the\s+)?(?:filter|guardrail|policy|safety))\b`),
		regexp.MustCompile(`(?i)\b(?:do not|don't)\s+follow\b.{0,32}\b(?:instructions?|rules?|policy)\b`),
	}
	internalTargetPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:localhost|0\.0\.0\.0|127\.0\.0\.1|169\.254\.169\.254)\b`),
		regexp.MustCompile(`(?i)\bmetadata\.google\.internal\b`),
		regexp.MustCompile(`(?i)\bprocess\.env\b`),
		regexp.MustCompile(`(?i)(?:^|\s)/etc/(?:passwd|shadow)(?:\s|$)`),
	}
)

func FilterSearchQuery(input string) SearchDecision {
	if !utf8.ValidString(input) {
		return reject("control_characters", "query is not valid UTF-8")
	}
	if hasForbiddenCharacters(input) {
		return reject("control_characters", "query contains hidden, bidirectional, or non-text control characters")
	}

	query := normalizeQuery(input)
	if query == "" {
		return reject("empty_query", "query is empty after normalization")
	}
	if utf8.RuneCountInString(query) > maxQueryRunes {
		return reject("query_too_long", "query exceeds 240 characters")
	}
	if len(strings.Fields(query)) > maxQueryTerms {
		return reject("too_many_terms", "query exceeds 40 whitespace-delimited terms")
	}
	if urlPattern.MatchString(query) {
		return reject("url_in_query", "search queries must be product terms, not URLs")
	}
	if matchesAny(secretPatterns, query) {
		return reject("credential_in_query", "query appears to contain a credential or private key")
	}
	if matchesAny(instructionPatterns, query) {
		return reject("instruction_injection", "query contains instruction-like text that should not be forwarded")
	}
	if matchesAny(internalTargetPatterns, query) {
		return reject("internal_target", "query references a local, metadata, environment, or sensitive host/file target")
	}

	return SearchDecision{
		Query:      query,
		Normalized: query != input,
	}
}

func reject(code, message string) SearchDecision {
	return SearchDecision{Rejection: &Rejection{Code: code, Message: message}}
}

func matchesAny(patterns []*regexp.Regexp, value string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func hasForbiddenCharacters(value string) bool {
	for _, r := range value {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || (r >= 0x7f && r <= 0x9f) {
			return true
		}
		if unicode.In(r, unicode.Cf) {
			return true
		}
	}
	return false
}

func normalizeQuery(value string) string {
	folded := strings.Map(func(r rune) rune {
		switch {
		case r == 0x3000:
			return ' '
		case r >= 0xff01 && r <= 0xff5e:
			return r - 0xfee0
		default:
			return r
		}
	}, value)
	return strings.Join(strings.Fields(folded), " ")
}

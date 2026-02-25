// Package conversation provides intent parsing and user notification implementations.
package conversation

import (
	"context"
	"regexp"
	"strings"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Compile-time interface check.
var _ domain.IntentParser = (*KeywordParser)(nil)

// KeywordParser matches user input to intents using keywords and simple patterns.
// Swap this out for an LLM-backed parser when ready.
type KeywordParser struct {
	log      *logger.Logger
	patterns []patternRule
}

type patternRule struct {
	regex  *regexp.Regexp
	intent domain.IntentType
}

// NewKeywordParser creates a keyword-based intent parser.
func NewKeywordParser(log *logger.Logger) *KeywordParser {
	p := &KeywordParser{log: log}
	p.patterns = []patternRule{
		{regexp.MustCompile(`(?i)^(next|done|continue|n|advance)$`), domain.IntentAdvance},
		{regexp.MustCompile(`(?i)^(skip|s)$`), domain.IntentSkip},
		{regexp.MustCompile(`(?i)^(repeat|again|what\??|r|re)$`), domain.IntentRepeat},
		{regexp.MustCompile(`(?i)^(repeat last|say that again|what did you say|come again)$`), domain.IntentRepeatLast},
		{regexp.MustCompile(`(?i)^(pause|brb|wait|p)$`), domain.IntentPause},
		{regexp.MustCompile(`(?i)^(resume|back|continue|unpause)$`), domain.IntentResume},
		{regexp.MustCompile(`(?i)^(status|where|progress|info)$`), domain.IntentStatus},
		{regexp.MustCompile(`(?i)^(quit|exit|stop|q|abandon)$`), domain.IntentQuit},
		{regexp.MustCompile(`(?i)^(help|h|\?)$`), domain.IntentHelp},
		{regexp.MustCompile(`(?i)^(dismiss|ok|got it|acknowledged)$`), domain.IntentDismissTimer},
		{regexp.MustCompile(`(?i)^dismiss\b`), domain.IntentDismissTimer},
		{regexp.MustCompile(`(?i)^(list|recipes|show|browse)$`), domain.IntentListRecipes},
		{regexp.MustCompile(`(?i)^(start|cook|go|begin|let'?s go)$`), domain.IntentStartCooking},
		{regexp.MustCompile(`(?i)^(timer|start timer|ready|set timer)$`), domain.IntentStartTimer},
		// Modify intent — explicit keywords at the start.
		{regexp.MustCompile(`(?i)^(modify|change|swap|replace|double|halve|adjust|substitute)\b`), domain.IntentModify},
	}
	return p
}

// Parse converts user input into an intent.
func (p *KeywordParser) Parse(ctx context.Context, input string, session *domain.Session) (*domain.Intent, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return &domain.Intent{Type: domain.IntentUnknown}, nil
	}

	p.log.Debug("parsing input: %q", trimmed)

	// Check for recipe selection by number (e.g., "1", "2", "3").
	if len(trimmed) <= 2 && isDigits(trimmed) {
		return &domain.Intent{Type: domain.IntentSelectRecipe, Payload: trimmed}, nil
	}

	// Check keyword patterns.
	for _, rule := range p.patterns {
		if rule.regex.MatchString(trimmed) {
			p.log.Debug("matched intent: %s", rule.intent)
			// Carry the full input as payload for intents that need it.
			if rule.intent == domain.IntentModify || rule.intent == domain.IntentDismissTimer {
				return &domain.Intent{Type: rule.intent, Payload: trimmed}, nil
			}
			return &domain.Intent{Type: rule.intent}, nil
		}
	}

	// Check if input starts with "select" or "pick" followed by something.
	if strings.HasPrefix(strings.ToLower(trimmed), "select ") || strings.HasPrefix(strings.ToLower(trimmed), "pick ") {
		parts := strings.SplitN(trimmed, " ", 2)
		if len(parts) == 2 {
			return &domain.Intent{Type: domain.IntentSelectRecipe, Payload: strings.TrimSpace(parts[1])}, nil
		}
	}

	// Detect questions: ends with "?", or starts with a question word.
	if isQuestion(trimmed) {
		return &domain.Intent{Type: domain.IntentAskQuestion, Payload: trimmed}, nil
	}

	p.log.Debug("no match, returning unknown intent")
	return &domain.Intent{Type: domain.IntentUnknown, Payload: trimmed}, nil
}

// questionPrefixes are common English question starters.
var questionPrefixes = []string{
	"how", "what", "why", "when", "where", "who",
	"can", "could", "should", "would", "will", "do", "does", "is", "are",
	"am i", "tell me", "explain",
}

// isQuestion returns true if the input looks like a question.
func isQuestion(s string) bool {
	if strings.HasSuffix(s, "?") {
		return true
	}
	lower := strings.ToLower(s)
	for _, prefix := range questionPrefixes {
		if strings.HasPrefix(lower, prefix+" ") || lower == prefix {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

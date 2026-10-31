package core

// Privacy redaction — scans outgoing prompts for high-confidence secrets
// before they hit a third-party CLI / cloud endpoint. Regex-only tier
// (the on-device ML classifier Osaurus uses is out of scope for 1.0
// per the plan).
//
// Built-in patterns cover the high-signal-low-noise categories:
//
//   - SSN (US 9-digit)
//   - Credit card (12-19 digits, Luhn-checked to reduce false positives)
//   - AWS access key ID (AKIA / ASIA)
//   - AWS secret access key (40-char base64 after "aws_secret")
//   - GitHub PATs (ghp_, gho_, ghu_, ghs_, ghr_)
//   - OpenAI keys (sk-…)
//   - Anthropic keys (sk-ant-…)
//   - Slack tokens (xoxb-, xoxp-, xoxa-, xoxr-)
//   - Generic "Bearer <token>" headers
//   - Private-key PEM blocks
//
// Users add their own patterns through settings.privacy.patterns
// (decision deferred — Phase 4b adds the Settings TUI surface).
//
// Behavior:
//
//   - Match(text)   → []Match, the raw findings.
//   - Redact(text)  → (scrubbed, []Match) where each finding is
//     replaced with a stable [SSN_1] / [GITHUB_TOKEN_2] placeholder.
//   - Reveal(scrubbed, matches) → reverses the substitution. Used when
//     the LLM's reply streams back so the user sees their original
//     numbers, not the placeholders.
//
// Redact is deterministic — the i-th hit for a given category always
// gets the i-th placeholder of that category. Re-running Redact on
// the same input produces identical output.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// PrivacyCategory names a class of secret. Used to label matches and
// build placeholders.
type PrivacyCategory string

const (
	CatSSN          PrivacyCategory = "SSN"
	CatCreditCard   PrivacyCategory = "CREDIT_CARD"
	CatAWSAccessID  PrivacyCategory = "AWS_ACCESS_KEY"
	CatAWSSecret    PrivacyCategory = "AWS_SECRET"
	CatGitHubToken  PrivacyCategory = "GITHUB_TOKEN"
	CatOpenAIKey    PrivacyCategory = "OPENAI_KEY"
	CatAnthropicKey PrivacyCategory = "ANTHROPIC_KEY"
	CatSlackToken   PrivacyCategory = "SLACK_TOKEN"
	CatBearer       PrivacyCategory = "BEARER"
	CatPrivateKey   PrivacyCategory = "PRIVATE_KEY"
	CatCustom       PrivacyCategory = "CUSTOM"
)

// privacyPattern pairs a category with its detection regex and an
// optional post-match validator (used for credit cards' Luhn check).
type privacyPattern struct {
	Category PrivacyCategory
	Regex    *regexp.Regexp
	Pattern  string
	Validate func(s string) bool // returns true if the match should be kept
}

// builtinPatterns is the production catalogue. Order matters only for
// disambiguation — most-specific patterns first so e.g. CC numbers
// don't get caught by a permissive "long digit run" rule.
var builtinPatterns = []privacyPattern{
	{Category: CatAnthropicKey, Regex: regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`)},
	{Category: CatOpenAIKey, Regex: regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)},
	{Category: CatGitHubToken, Regex: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`)},
	{Category: CatSlackToken, Regex: regexp.MustCompile(`xox[bpar]-[A-Za-z0-9-]{10,}`)},
	{Category: CatAWSAccessID, Regex: regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}`)},
	{Category: CatAWSSecret, Regex: regexp.MustCompile(`(?i)aws_secret(?:_access)?_key\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})["']?`)},
	{Category: CatBearer, Regex: regexp.MustCompile(`(?i)Bearer\s+([A-Za-z0-9_\-\.]{20,})`)},
	{Category: CatPrivateKey, Regex: regexp.MustCompile(`-----BEGIN[A-Z ]*PRIVATE KEY-----`)},
	{Category: CatSSN, Regex: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), Validate: ssnCheck},
	{Category: CatCreditCard, Regex: regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`), Validate: luhnCheck},
}

// Match is one finding from a scan. Positions are byte offsets into
// the original text; Value is the literal that matched.
type Match struct {
	Category    PrivacyCategory
	Start       int
	End         int
	Value       string
	Placeholder string // assigned by Redact; empty after a raw Match() call
}

// PrivacyScanner is the configurable entry point. Hold one per Core;
// callers extend it via AddCustomPattern.
type PrivacyScanner struct {
	mu     sync.RWMutex
	custom []privacyPattern
}

// NewPrivacyScanner returns a scanner pre-loaded with the built-ins.
func NewPrivacyScanner() *PrivacyScanner {
	return &PrivacyScanner{}
}

// NewRedactionSession returns per-run redaction state. Keeping counters
// across turns prevents placeholder collisions in multi-step workflows.
func (p *PrivacyScanner) NewRedactionSession() *PrivacyRedaction {
	return &PrivacyRedaction{
		scanner:  p,
		counters: map[PrivacyCategory]int{},
	}
}

// AddCustomPattern registers a user-defined regex under the CUSTOM
// category. Returns an error if the pattern is invalid.
func (p *PrivacyScanner) AddCustomPattern(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("compile custom pattern: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.custom = append(p.custom, privacyPattern{Category: CatCustom, Regex: re, Pattern: pattern})
	return nil
}

// SetCustomPatterns replaces the custom pattern list atomically after
// validating every regex.
func (p *PrivacyScanner) SetCustomPatterns(patterns []string) error {
	next := make([]privacyPattern, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("compile custom pattern %q: %w", pattern, err)
		}
		next = append(next, privacyPattern{Category: CatCustom, Regex: re, Pattern: pattern})
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.custom = next
	return nil
}

// CustomPatterns returns the currently configured custom regex strings.
func (p *PrivacyScanner) CustomPatterns() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.custom))
	for _, pat := range p.custom {
		out = append(out, pat.Pattern)
	}
	return out
}

// Match scans text and returns every finding. Overlapping matches are
// resolved deterministically: leftmost-longest wins. The returned
// slice is sorted by Start ascending.
func (p *PrivacyScanner) Match(text string) []Match {
	p.mu.RLock()
	patterns := append([]privacyPattern{}, builtinPatterns...)
	patterns = append(patterns, p.custom...)
	p.mu.RUnlock()

	var hits []Match
	for _, pat := range patterns {
		for _, loc := range pat.Regex.FindAllStringIndex(text, -1) {
			value := text[loc[0]:loc[1]]
			if pat.Validate != nil && !pat.Validate(value) {
				continue
			}
			hits = append(hits, Match{
				Category: pat.Category,
				Start:    loc[0],
				End:      loc[1],
				Value:    value,
			})
		}
	}
	return dedupeOverlapping(hits)
}

// dedupeOverlapping resolves overlapping matches by leftmost-longest.
// O(n log n) over the match count, which is always tiny in practice.
func dedupeOverlapping(hits []Match) []Match {
	if len(hits) <= 1 {
		return hits
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Start != hits[j].Start {
			return hits[i].Start < hits[j].Start
		}
		// Longer match wins at same start.
		return hits[i].End-hits[i].Start > hits[j].End-hits[j].Start
	})
	out := make([]Match, 0, len(hits))
	lastEnd := -1
	for _, h := range hits {
		if h.Start < lastEnd {
			continue
		}
		out = append(out, h)
		lastEnd = h.End
	}
	return out
}

// Redact replaces every match in text with a category-stamped
// placeholder (e.g. "[OPENAI_KEY_1]"). Returns the scrubbed text and
// the populated Match list (each carrying its assigned Placeholder).
func (p *PrivacyScanner) Redact(text string) (string, []Match) {
	return p.NewRedactionSession().Redact(text)
}

// PrivacyRedaction carries placeholder state for one workflow run.
type PrivacyRedaction struct {
	scanner  *PrivacyScanner
	counters map[PrivacyCategory]int
	matches  []Match
}

// Redact replaces every match in text with a category-stamped
// placeholder unique within this redaction session.
func (r *PrivacyRedaction) Redact(text string) (string, []Match) {
	if r == nil || r.scanner == nil {
		return text, nil
	}
	matches := r.scanner.Match(text)
	if len(matches) == 0 {
		return text, nil
	}
	var b strings.Builder
	b.Grow(len(text))
	cursor := 0
	for i := range matches {
		m := &matches[i]
		r.counters[m.Category]++
		m.Placeholder = fmt.Sprintf("[%s_%d]", m.Category, r.counters[m.Category])
		b.WriteString(text[cursor:m.Start])
		b.WriteString(m.Placeholder)
		cursor = m.End
	}
	b.WriteString(text[cursor:])
	r.matches = append(r.matches, matches...)
	return b.String(), matches
}

// Reveal substitutes placeholders back to original values. Used to
// un-scrub a model's reply when it echoes our placeholders.
func (p *PrivacyScanner) Reveal(text string, matches []Match) string {
	if len(matches) == 0 || len(text) == 0 {
		return text
	}
	for _, m := range matches {
		if m.Placeholder == "" {
			continue
		}
		text = strings.ReplaceAll(text, m.Placeholder, m.Value)
	}
	return text
}

// Reveal substitutes all placeholders from this redaction session back
// to their original values.
func (r *PrivacyRedaction) Reveal(text string) string {
	if r == nil || r.scanner == nil {
		return text
	}
	return r.scanner.Reveal(text, r.matches)
}

// ssnCheck rejects invalid SSN area numbers / groups / serials per
// the SSA's published exclusion list. Go's RE2 has no lookarounds so
// the regex captures shape only; we do prefix validation here.
//
// Invalid: area 000, 666, 900-999; group 00; serial 0000.
func ssnCheck(s string) bool {
	// s is "AAA-GG-SSSS".
	if len(s) != 11 {
		return false
	}
	area := s[0:3]
	group := s[4:6]
	serial := s[7:11]
	if area == "000" || area == "666" {
		return false
	}
	if area[0] == '9' {
		return false
	}
	if group == "00" {
		return false
	}
	if serial == "0000" {
		return false
	}
	return true
}

// luhnCheck implements the Luhn algorithm used to validate credit card
// numbers. Digits-only string (spaces/dashes stripped before call).
func luhnCheck(s string) bool {
	digits := make([]int, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			digits = append(digits, int(c-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

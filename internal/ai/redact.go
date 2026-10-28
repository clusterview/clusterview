package ai

import (
	"regexp"
	"strings"
)

// Redacted replaces whatever was removed.
const Redacted = "[REDACTED]"

// secretName matches names that hold secrets: env vars, config keys, JSON
// fields, query parameters.
var secretName = regexp.MustCompile(`(?i)(pass(word|wd|phrase)?|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|client[_-]?secret|credential|auth(orization)?|session|cookie|signature|dsn|conn(ection)?[_-]?str(ing)?)`)

// SecretName reports whether a variable or key name looks like it holds a
// secret.
func SecretName(name string) bool { return secretName.MatchString(name) }

var redactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	// PEM private keys and certificates' keys.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[REDACTED PRIVATE KEY]"},
	// JSON Web Tokens.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`), "[REDACTED JWT]"},
	// Authorization: Bearer xxx / Basic xxx.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 " + Redacted},
	// Credentials in URLs: scheme://user:password@host.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+):[^\s@/]+@`), "$1:" + Redacted + "@"},
	// Well-known key formats.
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), Redacted},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`), Redacted},
	{regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`), Redacted},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), Redacted},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`), Redacted},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), Redacted},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), Redacted},
}

// assignment is name=value, name: value or "name": "value".
var assignment = regexp.MustCompile(`(?i)("?([a-z0-9_.-]*` + secretName.String() + `[a-z0-9_.-]*)"?\s*[:=]\s*"?)([^\s"',;&}]{3,})`)

// proseKey are words that sound secret but start an explanation, as in
// "Unauthorized: invalid key", whose value is the evidence.
var proseKey = regexp.MustCompile(`(?i)^(un)?authori[sz](ed|ation)$|^authenticat(ion|ed)$|^(invalid|expired|missing|bad)[_-]?(token|password|credentials?)$`)

// longToken is a run of key-like characters long enough to be a key.
var longToken = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)

// Redact removes anything in text that looks like a secret and reports how
// many it removed. It errs on the side of removing too much: the model can
// explain a failure without the password.
func Redact(text string) (string, int) {
	n := 0
	for _, r := range redactions {
		text = r.re.ReplaceAllStringFunc(text, func(m string) string {
			n++
			return r.re.ReplaceAllString(m, r.repl)
		})
	}
	text = replace(assignment, text, func(m []string, _ string) string {
		if proseKey.MatchString(m[2]) || m[3] == Redacted {
			return m[0]
		}
		n++
		return m[1] + Redacted
	})
	text = replace(longToken, text, func(m []string, before string) string {
		if !keyLike(m[0]) || strings.HasSuffix(before, "sha256:") {
			return m[0]
		}
		n++
		return Redacted
	})
	return text, n
}

// replace is ReplaceAllStringFunc with the submatches and the text before
// the match, so a digest (sha256:…) can be told from a key.
func replace(re *regexp.Regexp, text string, fn func(m []string, before string) string) string {
	var b strings.Builder
	last := 0
	for _, idx := range re.FindAllStringSubmatchIndex(text, -1) {
		m := make([]string, len(idx)/2)
		for i := range m {
			if idx[2*i] >= 0 {
				m[i] = text[idx[2*i]:idx[2*i+1]]
			}
		}
		b.WriteString(text[last:idx[0]])
		b.WriteString(fn(m, text[:idx[0]]))
		last = idx[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// keyLike tells a random key from things that are long but harmless: pod
// and image names, paths. Those are made of short parts joined by dashes and
// slashes; a key has one long random part.
func keyLike(s string) bool {
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' || r == '/' }) {
		if len(part) >= 32 && isHex(part) {
			return true // a hex key; labelled digests are kept by the caller
		}
		if len(part) >= 24 && mixed(part) {
			return true // base64-ish
		}
	}
	return false
}

func mixed(s string) bool {
	var upper, lower, digit bool
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

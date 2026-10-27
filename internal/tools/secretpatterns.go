package tools

import "regexp"

// SecretPattern is one entry of the shared secret pattern list
// (docs/architecture/memory.md §7, S10-L2): the memory write filter and the
// Redactor use the same list so patterns are never maintained twice.
type SecretPattern struct {
	Name string
	Re   *regexp.Regexp
}

// SecretPatterns is the shared list. Adding or changing an entry is a
// security review item.
var SecretPatterns = []SecretPattern{
	{"openai_key", regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`)},
	{"github_token", regexp.MustCompile(`(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`)},
	{"slack_token", regexp.MustCompile(`xox[abprs]-[A-Za-z0-9\-]{10,}`)},
	{"aws_access_key", regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`)},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`)},
	{"pem_block", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*(?:PRIVATE KEY|CERTIFICATE|KEY)-----`)},
	{"password_assignment", regexp.MustCompile(`(?i)(?:password|passwd|passcode|비밀번호|비번|암호|\bpw)\s*[:=]`)},
	{"hex_key", regexp.MustCompile(`[0-9a-fA-F]{32,}`)},
	{"url_userinfo", regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*://[^/\s@]+@`)},
	{"kr_rrn", regexp.MustCompile(`\b\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\s*-?\s*[1-8]\d{6}\b`)},
}

package memory

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

func TestCheckSecret_HomoglyphAndMultiDecode(t *testing.T) {
	key := "sk-" + strings.Repeat("abcdEFGH12", 2) // synthetic
	b64 := base64.StdEncoding.EncodeToString
	block := []string{
		"키는 ѕk-" + strings.Repeat("abcdEFGH12", 2),          // Cyrillic s
		"АKIA" + "ABCDEFGHIJKLMNOP",                         // Cyrillic A
		"ｓｋ-" + strings.Repeat("abcdEFGH12", 2),             // fullwidth
		"설정 password%253Dfoo",                               // double URL
		"blob " + b64([]byte(b64([]byte(key)))),             // base64(base64)
		"x " + url.QueryEscape(b64([]byte(key))),            // url(base64)
		"h " + hex.EncodeToString([]byte(b64([]byte(key)))), // hex(base64)
	}
	for _, s := range block {
		if CheckSecret(s) == nil {
			t.Errorf("not blocked: %q", s)
		}
	}
	for _, s := range []string{"Привет, как дела? Я люблю кофе", "Καλημέρα, ωραίος καιρός", "고양이를 키운다", "회의는 10%25 할인 쿠폰 얘기", "aGVsbG8gd29ybGQ="} {
		if err := CheckSecret(s); err != nil {
			t.Errorf("false positive %q: %v", s, err)
		}
	}
}

func TestVariants_Bounded(t *testing.T) {
	s := strings.Repeat(base64.StdEncoding.EncodeToString([]byte("abcdefghijklmnop"))+" ", 200)
	if n := len(variants(s)); n > maxVariants {
		t.Fatal(n)
	}
}

package memory

import (
	"errors"
	"testing"
)

func TestMemorySensitive_DenyList(t *testing.T) {
	deny := []string{
		"작년에 우울증 진단을 받았다", "나는 동성애자다", "종교는 불교", "지지 정당은 OO당",
		"전과가 있다", "노조 가입했다", "I was diagnosed with bipolar disorder", "I am gay",
		"우 울 증 치료 중", // squashed copy
	}
	for _, s := range deny {
		_, err := Clean(s)
		var rej *RejectError
		if !errors.As(err, &rej) {
			t.Errorf("%q: stored, want sensitive reject (err=%v)", s, err)
		}
	}
	allow := []string{"아침 브리핑은 짧게", "고양이를 키운다", "회의는 오전 10시 선호", "커피는 디카페인", "Go와 Python을 주로 쓴다"}
	for _, s := range allow {
		if _, err := Clean(s); err != nil {
			t.Errorf("%q: rejected: %v", s, err)
		}
	}
}

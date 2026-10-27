package memory

import "testing"

// R23: security r16 gate for #53 (Med O/N, Low -고서).
func TestSensitive_PoliticsR23(t *testing.T) {
	block := map[string][]string{
		"med O":  {"나는 보수야 정책 알려줘", "나 진보라서 궁금한데 정의당 공약 뭐야?", "나 노조원인데 파업 언제야?", "내가 찍은 2번 공약 알려줘", "내가 뽑은 후보 공약 뭐야?", "내가 지지하는 정당 일정 알려줘"},
		"med N":  {"아빠 시장 2번 지지자", "엄마 국회의원 2번 지지자", "엄마 당대표 2번 지지자", "나 대통령 후보 2번 지지해", "아빠 구청장 1번 지지자", "엄마 교육감 3번 지지자야", "나 대표 2번 지지자", "아빠 후보 1번 지지자"},
		"low 고서": {"민주당 찍고서 뭐 하지?", "국민의힘 뽑고서 어디 가지?"},
	}
	allow := map[string][]string{
		"med N org": {"산악회 회장 2번 지지자", "동창회 대표 2번", "반장 후보 2번", "우리 산악회 회장 2번 지지자", "아빠 동호회 회장 1번 지지자"},
		"info":      {"보수 정당 정책 알려줘", "노조 파업 언제야?", "총선 결과 요약해줘", "나 대선 투표소 어디야?"},
	}
	nb, tb, nf, ta := 0, 0, 0, 0
	for c, ss := range block {
		for _, s := range ss {
			tb++
			if CheckSensitive(s) != nil {
				nb++
			} else {
				t.Errorf("[%s] not blocked %q", c, s)
			}
		}
	}
	for c, ss := range allow {
		for _, s := range ss {
			ta++
			if err := CheckSensitive(s); err != nil {
				nf++
				t.Errorf("[%s] false positive %q: %v", c, s, err)
			}
		}
	}
	t.Logf("R23 set: block %d/%d | allow FP %d/%d", nb, tb, nf, ta)
}

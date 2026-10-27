package memory

import "testing"

// R21: union affiliation, org-election scope, vote-act verbs, 투표소 news,
// and security r14 (Med H/I/J, Low1/Low4).
func TestSensitive_PoliticsR21(t *testing.T) {
	block := map[string][]string{
		"union affiliation": {"나 노조 사무장이야", "아빠 노조 상근자셔", "저 노조 전임자예요", "남편이 노조 부장이야", "나 지부 노조 일 해", "엄마 노조 집회 나가셨어", "나 파업 참여 중이야 노조에서", "형 노동조합 소속이야",
			"나 금속노조야", "나 노조비 내", "나 유니언 멤버야", "저 민주노총 총무예요", "언니가 노조 회계원이야"},
		"vote act verbs":  {"기호 3번 줬어요", "기호 2번 쪽에 표 넣음", "기호 1번에 표 던졌어", "기호 2번 표 행사했어", "기호 3번 밀어줬어", "기호 1번 골랐어"},
		"med H simile":    {"나 민주당처럼 생각해", "나도 이재명같이 2번 찍었어", "나 국힘같이 보수야", "우리 아빠 조국처럼 진보야"},
		"med I supporter": {"엄마 더민 지지자임", "남편 찢 지지자야", "우리 엄마 진보 지지자야", "나 윤 지지자야", "나 보수 지지자야", "나 2번 지지자야", "아빠 우파 지지층이야", "누나 굥 팬이야"},
		"low1 cue":        {"나 구청에서 2번 찍었어", "주민센터에서 1번 찍고 왔어", "신분증 내고 2번 찍음"},
		"low4 길래":         {"나 2번 찍길래 사전투표 언제야?", "민주당 찍길래 투표소 어디야?"},
	}
	allow := map[string][]string{
		"union info/3rd": {"노조 사무장 역할이 뭐야?", "노조 상근자 월급 얼마야?", "노조 파업 언제래?", "그 회사 노조 집회 했대", "노조비 얼마야?", "유니언 가입 방법 알려줘"},
		"org election scope": {"우리 산악회는 회장 선거 기호 2번 찍었어", "우리 동아리는 회장 투표 기호 1번 뽑았어", "우리 크루는 대표 선거 3번 찍었어", "우리 모임에서 총무 선거 기호 2번 찍음", "우리 반의 반장 선거 기호 1번 찍었어",
			"우리 길드는 리더 투표 2번 줬어", "우리 팀은 주장 선거 1번 밀어줬어", "우리 학회는 회장 후보 2번 찍었어", "우리 봉사단은 단장 선거 기호 3번", "우리 합창단은 회장 투표 2번 쪽에 표 넣음", "우리 과는 과대 선거 기호 2번 찍었어"},
		"투표소 news":              {"투표소에 사람 많대", "투표소 줄 길대", "사전투표소 붐빈대", "총선 결과 나왔대"},
		"non-political 진보/보수/팬": {"보수 공사 끝났대", "보수적인 투자 좋아", "기술 진보가 빠르네", "나 아이유 팬이야", "엄마 손흥민 팬이셔", "나 토트넘 팬임", "우리 아빠 야구팀 지지자야"},
		"simile benign":         {"사전투표처럼 미리 해두자", "기호처럼 생긴 문양이야"},
		"backlog consistent":    {"나 2번 찍었어", "나 2번 했어"},
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
	t.Logf("R21 set: block %d/%d | allow FP %d/%d", nb, tb, nf, ta)
}

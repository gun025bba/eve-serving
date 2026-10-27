package memory

import "testing"

// R22: security final gate for #53 (Med K/L/M) and the info-request allow
// rule; '우리 엄마는 N번 지지자야, [단체] 회장 선거에서' stays blocked
// (accepted safe FP).
func TestSensitive_PoliticsR22(t *testing.T) {
	block := map[string][]string{
		"med K":        {"나 유니온 멤버야", "나 유니언 멤버야", "아빠 유니온 소속이셔"},
		"med L names":  {"엄마 재명 지지자야", "나 이 재명 지지자임", "아빠 윤 대통령 지지자야", "나 이 대표 팬이야", "남편 석열 지지층이야", "누나 동훈 팬임", "나 한 위원장 지지자야"},
		"med L number": {"아빠 산악회 2번 지지자", "엄마 동호회 3번 지지자야", "나 모임 1번 지지자임"},
		"med L fandom": {"나 찢빠야", "우리 엄마 문빠임", "아빠 윤빠야", "나 대깨문이야", "언니 개딸이야", "할아버지 박사모셔", "삼촌 노사모였어", "아빠 태극기부대야"},
		"med M":        {"우리 동창회 회장 선거 끝나고 나 사전투표 2번 찍었어", "우리 산악회 회장 투표하고 사전투표 1번 찍었어", "나라 대표 선거 2번 찍었어", "구청 회장 선거 2번 찍었어"},
		"accepted FP":  {"우리 엄마는 2번 지지자야, 산악회 회장 선거에서"},
		"med D/G keep": {"나 2번 찍을 건데 사전투표 어디서 해?", "나 사전투표 2번 찍었지?", "나 민주당 뽑을 건데 투표소 어디야?", "이재명 응원하는데 누가 이길까?"},
	}
	allow := map[string][]string{
		"info request":   {"나 대선 투표소 어디야?", "총선 결과 요약해줘", "민주당 경선 규칙 설명해줘", "국민의힘 전당대회 언제야?", "나 사전투표 기간 알려줘", "저 대선 후보 공약 정리해줘", "내 선거구 국회의원 누구야?", "조국혁신당 대표가 뭐래?"},
		"org scope kept": {"우리 산악회 회장 2번 지지자", "우리 동창회는 회장 선거 기호 2번 찍었어", "엄마 아이유 팬이야", "나 토트넘 팬임", "아빠 동호회 회장 1번 지지자"},
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
	t.Logf("R22 set: block %d/%d | allow FP %d/%d", nb, tb, nf, ta)
}

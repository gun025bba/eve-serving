package memory

import "testing"

func TestSensitive_IndirectVote(t *testing.T) {
	for _, s := range []string{"지난 선거에 2번 찍었어", "대선 때 기호 1번 뽑았어", "총선에서 3번 찍었다", "보궐선거 때 기호 2번 찍었어", "지선에서 3번 후보 뽑았어"} {
		if CheckSensitive(s) == nil {
			t.Errorf("not blocked: %q", s)
		}
	}
	for _, s := range []string{"선거 일정 알려줘", "투표소 위치", "2번 출구로 나가", "로또 번호 뽑았어", "사진 10번 찍었어", "가챠 5번 뽑았어", "시험에서 3번 찍었어", "반장 선거에서 2번 후보 뽑았어", "2번 찍었어"} {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("false positive %q: %v", s, err)
		}
	}
}

func TestSensitive_PoliticianAndJamo(t *testing.T) {
	for _, s := range []string{"이재명 지지해", "나 윤석열 지지함", "한동훈 대표 지지해", "저 이재명 팬이에요", "대선 때 김 후보 지지했어", "민ㅈㅜ당 지지", "민주당 지지", "민ㅈㅜ당 지지해", "민ㅈㅜ당 찍었어", "국민ㅇㅢ힘 지지해"} {
		if CheckSensitive(s) == nil {
			t.Errorf("not blocked: %q", s)
		}
	}
	for _, s := range []string{"이재명 대표 기사 요약해줘", "윤석열 관련 뉴스", "트럼프 관세 정책 설명해줘", "우리 팀장 응원해", "팀 대표 뽑았어", "반 대표 후보 지지해", "학생회장 후보 지지해", "동아리 대표 지지해"} {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("false positive %q: %v", s, err)
		}
	}
}

// R1: 기호 + number + 찍/뽑 is election context on its own (NFKC, spacing variants).
func TestSensitive_VoteNumberR1(t *testing.T) {
	for _, s := range []string{"기호 2번 찍었어", "기호2번 찍었어", "기호 ２번 찍었어", "기호 2 번 찍었어요", "기호 3번 뽑았어", "정의당 지지.", "정의당 지지!", "민주당 지지 ."} {
		if CheckSensitive(s) == nil {
			t.Errorf("not blocked: %q", s)
		}
	}
	for _, s := range []string{"기호 1번 후보 토론 영상", "팀 대표 뽑았어", "정의당 지지율 뉴스", "사진 2번 찍었어"} {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("false positive %q: %v", s, err)
		}
	}
}

// QA round 9: 투표 + N번 + 에/로 + 했/찍; comma/ellipsis party boundary;
// 찍/뽑 ending restricted (security Low).
func TestSensitive_VoteRound9(t *testing.T) {
	for _, s := range []string{"투표는 2번에 했어", "투표 3번으로 찍었어", "조국혁신당, 지지해", "녹색당…지지함", "녹색당...지지함", "기호 2번 뽑음", "기호 3번 찍을게"} {
		if CheckSensitive(s) == nil {
			t.Errorf("not blocked: %q", s)
		}
	}
	for _, s := range []string{"투표 2번 해도 돼?", "주차 기호 2번에 찍힌 차", "정의당 새 대표 기사 요약해줘", "조국혁신당 의석수 몇 석이야?", "기호 1번 후보 토론 영상", "팀 대표 뽑았어"} {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("false positive %q: %v", s, err)
		}
	}
}

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
	for _, s := range []string{"이재명 지지해", "나 윤석열 지지함", "한동훈 대표 지지해", "저 이재명 팬이에요", "김 후보 지지해", "민ㅈㅜ당 지지해", "민ㅈㅜ당 찍었어", "국민ㅇㅢ힘 지지해"} {
		if CheckSensitive(s) == nil {
			t.Errorf("not blocked: %q", s)
		}
	}
	for _, s := range []string{"이재명 대표 기사 요약해줘", "윤석열 관련 뉴스", "트럼프 관세 정책 설명해줘", "우리 팀장 응원해"} {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("false positive %q: %v", s, err)
		}
	}
}

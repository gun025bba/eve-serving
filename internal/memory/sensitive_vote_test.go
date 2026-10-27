package memory

import "testing"

func TestSensitive_IndirectVote(t *testing.T) {
	for _, s := range []string{"지난 선거에 2번 찍었어", "대선 때 기호 1번 뽑았어", "총선에서 3번 찍었다", "선거에서 그 후보 찍었어", "2번 찍었어"} {
		if CheckSensitive(s) == nil {
			t.Errorf("not blocked: %q", s)
		}
	}
	for _, s := range []string{"선거 일정 알려줘", "투표소 위치", "2번 출구로 나가", "로또 번호 뽑았어"} {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("false positive %q: %v", s, err)
		}
	}
}

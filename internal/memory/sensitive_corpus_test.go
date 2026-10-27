package memory

import "testing"

// QA corpus (memory P1 QA): must stay 0 false positives / 0 misses.
var sensitiveAllow = []string{
	"게이트웨이 설정을 바꿨다", "게이밍 노트북을 샀다", "게이머 친구가 있다", "연료 게이지가 고장났다", "게이트 앞에서 만나자",
	"이전과 같은 방식으로 해줘", "예전과 달라졌다", "전과목 1등급을 받았다", "이전과 있었던 일과 비교해줘",
	"수감 영화를 봤다", "추수감사절에 칠면조를 먹는다", "종교 법회 일정 알려줘", "종교 다큐를 봤다",
	"인종차별 반대 기사를 읽었다", "교회 건축 양식을 좋아한다", "소득세 신고 마감일 알려줘", "여권 사진 규격",
	"운전 연습을 하고 있다", "외국인 관광객이 많다", "I like gaming", "my favorite color is blue",
}

var sensitiveDeny = []string{
	"고혈압 약 먹고 있어", "교회 다녀", "천주교 신자야", "조선족 출신이야", "나 게이야", "나는 게이",
	"전과가 있어", "전과자야", "전과 있어", "감옥에 수감됐었어", "우울증 진단 받았어", "내 종교는 불교",
	"내 소득은 월 300", "부채가 5천만원", "신용점수 650점", "여권번호 M12345678", "운전면허 번호 11-22-333333-44",
	"외국인등록번호 900101-5123456", "my credit score is 700", "I'm a muslim",
}

func TestQA_SensitiveCorpus(t *testing.T) {
	for _, s := range sensitiveAllow {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("false positive: %q (%v)", s, err)
		}
	}
	for _, s := range sensitiveDeny {
		if CheckSensitive(s) == nil {
			t.Errorf("miss: %q", s)
		}
	}
}

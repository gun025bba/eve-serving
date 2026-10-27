package memory

// QA (PR #34): ordinary vs sensitive Korean corpus. Logs FP/FN; set
// QA_STRICT=1 to fail on them.
import (
	"os"
	"testing"
)

var qaOrdinary = []string{
	"내일 병원 예약 있어", "치과 스케일링 다음 주에 받아", "감기 기운이 있어서 약 먹었어", "요즘 허리가 좀 아파",
	"헬스장 주 3회 다녀", "건강검진 결과 괜찮대", "내일 비 온대", "오늘 미세먼지 심하다", "주말에 태풍 온다던데",
	"이번 겨울 엄청 춥대", "회의가 3시에 있어", "다음 달 이직 준비 중이야", "프로젝트 마감이 금요일이야",
	"팀장님이랑 1:1 있어", "API 게이트웨이 배포했어", "게이밍 노트북 사고 싶어", "게이머 친구랑 롤 했어",
	"연료 게이지가 바닥이야", "이전과 같은 설정으로 해줘", "예전과 달라진 점 알려줘", "전과목 A 받았어",
	"우리 엄마 생신이야", "아빠랑 낚시 가기로 했어", "동생 결혼식이 5월이야", "아이 어린이집 상담 있어",
	"할머니 댁에 다녀왔어", "주식 수익률 이야기", "월급날이 25일이야", "적금 만기됐어", "연말정산 서류 챙겨야 해",
	"대출 금리 비교해줘", "종교 법회 일정", "종교 관련 다큐 추천해줘", "인종차별 반대 캠페인 기사 읽었어",
	"정당 이름 유래가 궁금해", "노조 뉴스 요약해줘", "지문 인식 폰 케이스 샀어", "수감 생활 다룬 영화 봤어",
	"교회 앞 카페 맛있어", "민족 대명절 추석이다",
}

var qaSensitive = []string{
	"나 우울증 진단 받았어", "공황장애로 정신과 다녀", "항우울제 먹고 있어", "당뇨 진단 받았어", "고혈압 약 먹고 있어",
	"암 투병 중이야", "나 게이야", "난 동성애자야", "나 트랜스젠더야", "기독교인이야", "교회 다녀", "천주교 신자야",
	"불교 신자야", "나 무슬림이야", "신천지 다녀", "지지 정당은 ○○당이야", "○○당 당원이야", "전과가 있어",
	"집행유예 받았어", "노조 가입했어", "나 조선족 출신이야", "유전자 검사 결과 나왔어", "I was diagnosed with ADHD",
	"I'm gay", "I am a union member",
}

func TestQA_SensitiveCorpusV1(t *testing.T) {
	var fp, fn int
	for _, s := range qaOrdinary {
		if err := CheckSensitive(s); err != nil {
			fp++
			t.Logf("FP %q -> %v", s, err)
		}
	}
	for _, s := range qaSensitive {
		if CheckSensitive(s) == nil {
			fn++
			t.Logf("FN %q", s)
		}
	}
	t.Logf("ordinary=%d FP=%d | sensitive=%d FN=%d", len(qaOrdinary), fp, len(qaSensitive), fn)
	if os.Getenv("QA_STRICT") == "1" && fp+fn > 0 {
		t.Fail()
	}
}

package memory

import (
	"strings"
	"testing"
)

// Extended corpus (security review #34 Low): >=100 per side, including
// third-party, zero-width, spacing, compatibility jamo and leet variants.
var allowV2 = []string{
	"게이트웨이 설정을 바꿨다", "게이밍 노트북을 샀다", "게이머 친구가 있다", "연료 게이지가 고장났다", "게이트 앞에서 만나자",
	"이전과 같은 방식으로 해줘", "예전과 달라졌다", "전과목 1등급을 받았다", "수감 영화를 봤다", "추수감사절에 칠면조를 먹는다",
	"종교 법회 일정 알려줘", "종교 다큐를 봤다", "인종차별 반대 기사를 읽었다", "교회 건축 양식을 좋아한다", "소득세 신고 마감일 알려줘",
	"여권 사진 규격", "운전 연습을 하고 있다", "외국인 관광객이 많다", "I like gaming", "my favorite color is blue",
	"archive 폴더를 정리해줘", "shiv 라는 단어를 배웠다", "chivalry is not dead", "정신과 의사 인터뷰 기사를 읽었다", "정신과 병동 드라마가 재밌다",
	"가구 소득이 3% 늘었다는 뉴스", "평균 연봉은 5천이라는 기사", "국가 부채가 1000조를 넘었다", "소득 분위 통계를 찾아줘", "주문번호 1234567890123",
	"송장번호 9876543210987", "전화번호는 회사 대표번호로", "모델명 M12345678 부품 주문", "R12345678 시리얼 키보드", "엄마가 김치찌개를 좋아해",
	"아빠는 골프를 친다", "친구가 서울로 이사했다", "동료가 휴가를 갔다", "남편은 커피를 싫어해", "아내가 요가를 시작했다",
	"동생이 대학에 붙었다", "형은 개발자다", "언니가 결혼한다", "할머니 생신이 다음 주다", "팀장님이 출장 중이다",
	"my mom likes jazz", "my friend moved to busan", "she is a teacher", "he has a cat", "they are visiting next week",
	"나는 고양이를 키운다", "아침은 거른다", "커피는 디카페인으로", "매운 음식을 좋아한다", "주말엔 등산을 간다",
	"비행기 창가 자리를 선호한다", "회의는 오전이 좋다", "답변은 짧게 해줘", "존댓말로 대답해줘", "파이썬을 주로 쓴다",
	"Go 언어를 공부 중이다", "서울에 산다", "자전거로 출근한다", "아이폰을 쓴다", "다크 모드를 좋아한다",
	"채식 메뉴를 자주 먹는다", "와인보다 맥주", "넷플릭스 드라마를 좋아한다", "고양이 이름은 나비", "생일은 비밀로 해줘",
	"교회 앞 카페가 맛있다", "성당 결혼식에 초대받았다", "절 근처 템플스테이 정보", "불교 미술 전시회 일정", "이슬람 건축 다큐를 봤다",
	"당뇨 예방 식단 기사", "고혈압 관련 통계", "우울한 날씨다", "암벽 등반을 좋아한다", "암호화 라이브러리를 쓴다",
	"교도소 배경 영화를 봤다", "출소 장면이 인상적인 드라마", "노조 관련 뉴스 요약해줘", "정당 지지율 기사", "신용카드 혜택 비교해줘",
	"지문 인식 잠금이 편하다", "홍채 모양 그림을 그렸다", "DNA 구조를 공부한다", "여권 갱신 절차 알려줘", "운전면허 시험 일정",
	"g4ming laptop 추천", "l33t code 문제 풀이", "pr0 tips for go", "게\u200b이밍 마우스", "게 이 트 웨 이",
	"ㄱㅔㅇㅣㅁㅣㅇ 노트북", "전 과목 공부", "이 전과 같은 방식", "income tax deadline", "debt ceiling news",
	"salary negotiation tips article", "prison break 시즌2", "cancer research news", "depression era history", "hindu temple architecture",
	"엄마랑 영화 봤다", "친구 생일 선물 추천", "동료와 점심 약속", "남친이랑 여행 간다", "아들이 축구를 한다",
}

var denyV2 = []string{
	"고혈압 약 먹고 있어", "교회 다녀", "천주교 신자야", "조선족 출신이야", "나 게이야", "나는 게이", "전과가 있어", "전과자야",
	"감옥에 수감됐었어", "우울증 진단 받았어", "내 종교는 불교", "내 소득은 월 300", "부채가 5천만원", "신용점수 650점",
	"여권번호 M12345678", "여권 M12345678", "운전면허 번호 11-22-333333-44", "외국인등록번호 900101-5123456", "9001015123456",
	"my credit score is 700", "I'm a muslim", "나 무슬림이야", "당뇨 있어", "작년에 교도소에서 출소했어", "내 연봉은 8천이야",
	"나 이슬람교 믿어", "기독교인이야", "불교 믿어", "하나님을 믿어", "성당에 다녀", "정신과 다녀", "정신과 치료 받고 있어",
	"공황장애가 있어", "조현병 진단", "항우울제 먹어", "암 투병 중이야", "에이즈 감염", "HIV positive", "i have diabetes",
	"i was diagnosed with adhd", "천식이 있어", "갑상선 질환이 있어", "치매 판정 받았어", "adhd 약 먹어", "낙태 경험",
	"동성애자야", "레즈비언이야", "트랜스젠더야", "성 정체성 고민", "I'm gay", "I am bisexual", "지지 정당은 A당", "정당 가입했어",
	"당원이야", "정치 성향은 진보", "I voted for X", "범죄 기록 있음", "집행유예 받았어", "유죄 판결 받았어", "I was convicted",
	"출소했어", "노조 가입했어", "노동조합 조합원이야", "I'm a union member", "내 인종은", "고려인 출신이에요", "혼혈이야",
	"지문 정보 저장해줘", "유전자 검사 결과", "DNA 검사 했어", "my genetic test", "내 월급은 300", "제 연봉 6천",
	"빚이 3천", "연봉은 8천이야", "my salary is 100k", "my debt is huge", "신용등급 3등급", "passport number M12345678",
	"엄마가 불교를 믿어", "아빠는 당뇨가 있어", "친구가 게이야", "동료가 전과자래", "남편이 우울증이야", "형이 교도소에 있어",
	"언니가 천주교 신자", "누나는 정당 당원", "할머니가 치매", "아들이 adhd 진단", "my mom has cancer", "my friend is gay",
	"my brother was in prison", "she is muslim", "he has depression", "엄마 연봉은 5천", "아내가 임신 중이야", "동생 빚이 많아",
	"나 무\u200b슬림이야", "나 게\u200b이야", "당 뇨 있 어", "우 울 증 진단", "ㄴㅏ ㄱㅔㅇㅣㅇㅑ", "ㅁㅜㅅㅡㄹㄹㅣㅁ이야", "I'm g4y",
	"I'm a mus1im", "i h4ve diabetes", "my cr3dit score is 600", "전과\u200b가 있어", "여권번호M12345678",
}

func TestSensitiveCorpusV2(t *testing.T) {
	if len(allowV2) < 100 || len(denyV2) < 100 {
		t.Fatalf("corpus too small: allow=%d deny=%d", len(allowV2), len(denyV2))
	}
	for _, s := range allowV2 {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("false positive: %q (%v)", s, err)
		}
	}
	for _, s := range denyV2 {
		if CheckSensitive(s) == nil {
			t.Errorf("miss: %q", strings.ToValidUTF8(s, "?"))
		}
	}
}

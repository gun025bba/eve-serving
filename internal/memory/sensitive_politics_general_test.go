package memory

import "testing"

// Generalization set for the r16 general rule (anchor + verb class), written
// fresh for this round: it is not copied from QA's private or holdout lists.
var politicsGeneralBlock = []string{
	// vote class, varied conjugations
	"이번 총선에선 녹색당 뽑는다", "다음 대선 때 개혁신당 찍겠어", "아버지는 늘 국민의힘 뽑으셨대", "할머니가 민주당 찍으셨어",
	"총선에 진보당 투표했대", "지방선거 때 기본소득당 기표했어", "국민투표는 반대표 냈어", "국민투표에서 찬성표 던졌어",
	"대선에 조국혁신당한테 한 표", "재보선에서 국힘에 표 줬대", "기호 5번 투표했어", "기호 2번에 표 드렸어",
	"선배가 이준석 뽑는대", "대선 때 김문수 찍으셨대",
	// support class
	"삼촌은 정의당 지지하신다", "우리 회사 사장님 국민의힘 지지한대", "새로운미래 지지하는 사람이야", "나는 개혁신당 밀어준다",
	"엄마는 녹색당 밀어주셨어", "내가 응원하는 정당은 진보당이야", "지지하는 정당은 민주당이야", "기본소득당 지지하겠어",
	"동생이 사회민주당 지지합니다", "작은아버지는 홍준표 지지하셨어",
	// membership class
	"나 국민의힘 권리당원이야", "누나가 민주당 권리당원 됐대", "형이 정의당 입당했대", "삼촌이 국힘 탈당하셨어",
	"아내가 진보당에 가입했대", "친구가 개혁신당 들어갔어", "장인어른이 녹색당 당원이셔서", "저 조국혁신당 당원이에요",
	"나 정의당 가입해 있어", "민주당 탈퇴했어",
	// public-election word + verb without a number
	"사전투표로 기호 3번 찍었어", "대선 때 바이든 찍었대", "총선 땐 국힘 찍을 거야", "지선에서 민주당 후보 뽑았어",
	"교육감 선거는 진보 쪽 후보 찍었어", "구청장 선거에서 국민의힘 후보한테 표 줬어",
}

var politicsGeneralAllow = []string{
	// information requests with anchors and verbs
	"민주당 입당 절차 알려줘", "국민의힘 탈당 방법 설명해줘", "정의당 가입 조건이 뭐야?", "권리당원 투표 일정 알려줘",
	"총선 투표 방법 알려줘", "대선 사전투표 어디서 해?", "국민투표 찬성표 몇 표였어?", "기호 2번 후보 누구야?",
	"민주당 지지율 기사 요약해줘", "국민의힘 탈당 뉴스 정리해줘", "진보당 입당한 의원 누구야?", "대선 후보 공약 비교해줘",
	// anchors without a verb class
	"민주당 당사 앞 카페 추천해줘", "국힘 대변인 브리핑 봤어", "정의당 로고 색 바뀌었네", "총선 결과 나왔대",
	"대선 토론 다시 보기 링크", "국민투표 제도 공부 중", "기호학 과제 끝냈어", "지방선거 포스터 디자인",
	"트럼프 사진 찍었어", "바이든 영상 찍어 봤어", "민주당 지지자 많네", "국민의힘 지지율 떨어졌대",
	"민주주의 지지해", "민주 시민 응원해", "민주 방식으로 뽑았어", "한국민의힘 지지해",
	// verbs without anchors
	"반장 선거 1번 찍었어", "동아리 회장 뽑으셨대", "팀장님이 대리님 뽑는대", "영화 투표 2번 찍겠어",
	"카페 회원 가입했어", "헬스장 가입했어", "독서 모임 들어갔어", "축구 동호회 탈퇴했어",
	"친구 의견 지지해", "동생 꿈 응원해", "부장님 아이디어 밀어줬어", "한 표 차이로 졌어",
	"사진 2번 찍었음", "객관식 3번 찍었어", "제비 뽑았어", "신입 사원 3명 뽑았어",
	// union: 조합원 is not 당원
	"나 신협 조합원이야", "생협 조합원 가입했어", "아파트 조합원 총회 갔어", "농협 조합원 혜택 알려줘",
	"노조 위원장 선거에서 2번 찍었어", "남편은 회사 노조 선거에서 1번 뽑았대",
	// non-public votes with a person subject but a direct qualifier
	"아빠가 교회 투표 2번 찍었어", "누나가 학교 투표에서 1번 뽑았대", "친구들끼리 투표 2번 찍었어", "강아지 이름 정하는 투표 3번 뽑았어",
	"친구들이랑 영화 고르는 투표 1번 뽑았어", "우리 팀 MVP 투표 1번 찍었어", "회식 장소 투표 2번 뽑았어", "아이돌 투표 3번 찍었어",
	"민주당 경선 투표 결과 알려줘", "대선 날 쉬는 날이야?",
}

func TestSensitive_PoliticsGeneralR16(t *testing.T) {
	var fn, fp int
	for _, s := range politicsGeneralBlock {
		if CheckSensitive(s) == nil {
			fn++
			t.Errorf("not blocked: %q", s)
		}
	}
	for _, s := range politicsGeneralAllow {
		if err := CheckSensitive(s); err != nil {
			fp++
			t.Errorf("false positive %q: %v", s, err)
		}
	}
	t.Logf("general set: block %d/%d | allow FP %d/%d", len(politicsGeneralBlock)-fn, len(politicsGeneralBlock), fp, len(politicsGeneralAllow))
}

package memory

// REPORT-ONLY holdout (round 11, written after backend saw earlier lists).
// core = public election / party / candidate support in plain form (gate-relevant);
// recall = dialect, English-mixed, slang verbs (recall only). allow = must not block (FP).
import "testing"

type qaHold struct{ s, kind string }

var qaHoldout = []qaHold{
	// core public
	{"국민의당 지지해", "core"}, {"새로운미래 지지해요", "core"}, {"자유통일당 찍었어", "core"}, {"기본소득당 뽑았어", "core"},
	{"시장 선거에서 2번 찍었어", "core"}, {"구청장 선거 때 1번 뽑았어", "core"}, {"교육감 선거는 3번 찍었어", "core"},
	{"국회의원 선거에서 1번 뽑았어", "core"}, {"도지사 선거 2번 찍었음", "core"}, {"대선에서 민주당 후보 찍었어", "core"},
	{"총선 때 국민의힘 후보 뽑았어", "core"}, {"지난 지방선거에 정의당 찍었어", "core"}, {"재보선에서 2번 찍었어", "core"},
	{"이번 대통령 선거는 2번 찍을 거야", "core"}, {"우리 엄마는 진보당 지지자야", "core"}, {"남편은 국민의힘 당원이야", "core"},
	{"민주당 후보한테 표 줬어", "core"}, {"투표소 가서 1번 찍고 왔어", "core"},
	// recall: other verbs, dialect, informal, English-mixed
	{"민주당 밀어줬어", "recall"}, {"국힘 찍었어", "recall"}, {"더민주 지지함", "recall"}, {"정의당 완전 팬이야", "recall"},
	{"이번엔 1번 찍었다 아이가", "recall"}, {"대선 땐 2번 찍었당께", "recall"}, {"총선 2번 뽑았어여", "recall"},
	{"I voted for 민주당", "recall"}, {"국민의힘 support 해", "recall"}, {"election에서 1번 찍음", "recall"},
	{"윤 후보 찍었어", "recall"}, {"이재명 후보 지지해", "recall"},
	// allow
	{"선거 포스터 디자인 피드백 줘", "allow"}, {"구청 민원 넣는 법 알려줘", "allow"}, {"민주당 당사 주소가 어디야?", "allow"},
	{"국민의힘 중앙당 전화번호 알려줘", "allow"}, {"학교 투표에서 2번 찍었어", "allow"}, {"반별 투표 1번이 이겼어", "allow"},
	{"게임 투표에서 3번 골랐어", "allow"}, {"아이돌 투표 2번 했어", "allow"}, {"아이돌 투표에서 1번 찍었어", "allow"},
	{"택배 받고 도장 찍었어", "allow"}, {"도장 2개 새로 팠어", "allow"}, {"지지대 1번 기둥에 묶어", "allow"},
	{"토마토 지지대 2번 세웠어", "allow"}, {"선거관리위원회 홈페이지 링크 줘", "allow"}, {"출구조사 결과 요약해줘", "allow"},
	{"후보 토론회 일정 알려줘", "allow"}, {"정당 가입 절차가 어떻게 돼?", "allow"}, {"점심 메뉴 투표 2번에 했어", "allow"},
	{"회식 장소 투표 1번 찍었어", "allow"}, {"가족 여행지 투표에서 2번 뽑았어", "allow"}, {"MVP 투표 3번 찍었어", "allow"},
	{"쿠폰 도장 10개 찍었어", "allow"}, {"스탬프 2번 찍었어", "allow"}, {"사진 1번 컷이 제일 좋아", "allow"},
	{"사전 주문 2번 했어", "allow"}, {"후보지 2번 부지가 더 넓어", "allow"}, {"기호학 수업 2번 들었어", "allow"},
	{"우리 반 회장 1번 뽑았어", "allow"}, {"노조 위원장 선거에서 2번 찍었어", "allow"},
	// document 도장 FP probes
	{"계약서 2번에 도장 찍었어", "allow"}, {"도장을 2번에 찍었어", "allow"}, {"신청서 2번 칸에 도장 찍었어", "allow"},
}

// policy probes: ambiguous 도장 (block expected) and plain 도장 (allow expected)
var qaHoldDojang = []qaHold{{"2번에 도장 찍었어", "core"}, {"도장 3번 찍었어", "allow"}, {"이 서류 2번에 도장 찍어야 해?", "allow"}}

func TestQA_PoliticalHoldoutReport(t *testing.T) {
	n := map[string]int{}
	ok := map[string]int{}
	for _, h := range append(append([]qaHold{}, qaHoldout...), qaHoldDojang...) {
		blocked := CheckSensitive(h.s) != nil
		n[h.kind]++
		if blocked == (h.kind != "allow") {
			ok[h.kind]++
		} else {
			t.Logf("MISS[%s] %q blocked=%v", h.kind, h.s, blocked)
		}
	}
	t.Logf("holdout core blocked %d/%d | recall %d/%d | allow FP %d/%d", ok["core"], n["core"], ok["recall"], n["recall"], n["allow"]-ok["allow"], n["allow"])
}

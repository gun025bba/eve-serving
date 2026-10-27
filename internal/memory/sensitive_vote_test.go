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

// Architect policy (#53): every core public-election disclosure blocks;
// non-public votes have zero false positives. QA round 10 shapes included.
func TestSensitive_PoliticsPolicyR10(t *testing.T) {
	block := []string{
		"대선 때 3번 뽑았음", "총선에서 2번 찍음", "지방선거에서 1번 찍었어", "지선 때 2번 뽑았어", "재보궐선거에서 3번 찍었다",
		"국민투표에서 1번 찍었어", "국민투표에서 찬성 찍었어", "국민투표 반대에 찍음", "선거에서 2번 찍었어", "기호 2번 찍었어", "기호 3번 찍고 왔어", "기호 1번 뽑겠다",
		"사전투표 때 1번에 했어", "이번 투표는 1번에 찍었다", "투표용지에 1번 찍었어", "2번에 도장 찍었어", "투표는 2번에 했어",
		"정의당·지지해", "민주당/지지함", "국민의힘~ 찍었음", "진보당? 지지자야", `"개혁신당" 지지해`, "'조국혁신당' 찍었어",
		"국민의힘 👍 지지", "녹색당 지지자임", "우리 아빠는 국민의힘 지지자야", "이재명 찍었어", "한동훈 지지함",
		// security review of 9e6c43a
		"주차하고 사전투표 2번 찍었어", "회의 끝나고 투표 2번 찍었어", "사진 찍고 국민투표 찬성 찍었어",
		"사전투표 2번 찍었어?", "국민투표 찬성 찍었어?", "민주당을 오래전부터 열심히 지지해",
		// round 11 structure: abbreviations, more parties, support verbs, offices
		"국힘 후보한테 표 줬음", "조국혁신당 밀어줬어", "새로운미래 뽑았어", "구청장 선거에서 3번 뽑았음", "대선에서 2번 찍었어?",
		// security review of 1af50a9
		"나 투표 2번 찍었어", "제가 투표 2번 찍었어요", "우리 가족 투표 2번 찍었어", "나 선거 때 2번 찍었어", "저는 선거 때 1번 뽑았어요",
		"국민의힘을 10년 넘게 계속 지지하고 있어", "민.주.당 지지해", "민-주-당 지지해", "민_주_당 지지해", "국짐 찍었어", "개혁당 지지해", "새미래 뽑았어",
		"대선 2번에 도장 찍었어", "사전투표 2번 찍을게?", "사전투표 2번 뽑음?", "남편은 정의당 당원이야",
		// round 12: abbreviation + vote-verb dictionaries
		"대선 때 1번에 한 표 던졌어", "조혁당 찍음", "총선에서 2번에 표를 던졌다", "지방선거 3번 기표했어", "재보선 때 1번에 표 행사했어",
		"더민 찍었다", "민주 뽑음", "사민당 지지해", "기본소득당한테 표 줬어", "조국당에 한 표 던졌어",
		// round 13: one vote-verb dictionary across party / number / 국민투표 rules
		"지난 대선에서 민주당에 투표했어", "지선 때 개혁신당을 기표했음", "녹색당에게 한 표 줬음", "작년 총선은 정의당한테 표를 던졌다",
		"국민투표에서 반대로 기표했어", "국민투표 때 찬성으로 표 행사했음", "국민투표에서 반대에 투표했어",
		"기호 3번 정당에 투표했어", "총선 때 2번 당에 한 표 던졌다", "대선에서 1번 후보에게 표 줬어", "사전투표로 5번 후보를 찍었어",
		// security Lows on 59169ef
		"election에서 2번 찍었어", "아들 투표 2번 찍었어", "여친 투표 1번 뽑았대", "내 딸 선거 때 2번 찍었어", "남친도 투표 3번 찍었어",
		"나 민.주.당 지지해", "우리 아빠 정.의.당 찍었어", "민주 찍었어", "더민에 한 표 던졌어",
	}
	allow := []string{
		"반장 투표는 2번에 했어", "반장 선거에서 2번 찍었어", "동아리 회장 선거에서 1번 찍었어", "회의에서 투표 2번 했어", "아파트 동대표 선거에서 2번 뽑았어",
		"오디션 투표 1번에 했어", "인기투표 3번 찍었어", "학생회장 선거 1번 뽑았어", "투표 2번 해도 돼?", "투표는 2번 해야 돼?",
		"이 서류 2번에 도장 찍어야 해?", "사진 10번 찍었어", "주차 기호 2번에 찍힌 차", "후보 2번이 누구야?",
		"조국혁신당 의석수 몇 석이야?", "정의당 새 대표 기사 요약해줘", "정의당·녹색당 합당 뉴스 요약해줘",
		"민주당/국민의힘 의석수 비교해줘", "민주당 지지율 기사 요약해줘", "기호 1번 후보 토론 영상", "팀 대표 뽑았어",
		"지지대 2번 다시 박았어", "투표용지 디자인 바뀌었대", "도장 3번 찍었어",
		// security review of 9e6c43a (over-block)
		"투표 2번 했어", "민주당 지지자 많네", "민주당 지지층 분석해줘",
		"반 티 투표 2번 찍었어", "축제 공연 투표에서 1번 뽑았어", "영화 투표 3번 찍었어", "신청서에 2번 도장 찍었어",
		"더민주 당사 어디야?", "국민의힘 팬미팅 기사", "정당 가입 절차 알려줘",
		"트럼프 사진 찍었어", "선거 포스터 2번 찍었어", "이재명 영상 찍었어", "나 사진 2번 찍었어", "학교 투표에서 1번 뽑았어",
		"반장 선거 때 1번에 한 표 던졌어", "동아리 투표에서 2번 기표했어", "공 2번 던졌어", "민주주의 역사 요약해줘",
		"반장 후보한테 표 줬어", "동아리 2번 후보한테 한 표 던졌어", "학생회 선거에서 1번 후보에게 투표했어", "회의에서 2번 안건에 투표했어",
		"찬성에 한 표 던졌어", "아이돌 2번 후보한테 투표했어",
		"민주주의 지지해", "민주 시민 응원해", "민주 시민 후원해", "민주주의 만세", "사내 투표 2번 찍었어", "팬클럽 투표에서 1번 뽑았어",
		"카페 투표 3번 찍었어", "나 정 말 좋아",
	}
	for _, s := range block {
		if CheckSensitive(s) == nil {
			t.Errorf("not blocked: %q", s)
		}
	}
	for _, s := range allow {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("false positive %q: %v", s, err)
		}
	}
}

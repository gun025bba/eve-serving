package memory

// QA (PR #34 round 3): fresh unseen sentences + edge cases. Synthetic IDs only.
// Logs FP/FN; QA_STRICT=1 fails on them.
import (
	"os"
	"testing"
)

var qaFreshOrdinary = []string{
	"hive 테이블 파티션 정리했어", "로그는 archive 버킷으로 옮겨", "HIV 검사 기사 읽었어", "친구랑 교회 앞 카페에서 코딩했어",
	"팀장님이 당뇨 관리 앱 기획서 공유했어", "엄마가 성당 바자회 사진 보내줬어", "게이트웨이 timeout 30초로 늘려", "k8s 노드 이전과 동일하게 맞춰",
	"외국인 등록 절차 블로그 글 요약해줘", "여권 갱신 서류 뭐 필요해?", "빌드 번호 991340-5123456 확인", "주문번호 12345678 배송 조회",
	"신용카드 결제일 바꿨어", "정치 뉴스 요약해줘",
	// round 4: benign politics/health
	"총선 투표율 통계 정리해줘", "정당 지지율 여론조사 기사 요약", "대통령 연설문 영어로 번역해줘",
	// round 5: benign controls for indirect vote preference
	"선거 날 투표소 위치 알려줘", "사전투표 기간 언제야?", "후보 2번 공약 요약해줘", "기호 1번 후보 토론 영상 찾아줘",
	"혈압계 추천해줘", "독감 예방접종 시기 알려줘", "Kubernetes health check 엔드포인트 만들어",
}
var qaFreshSensitive = []string{
	"나 HIV 양성이야", "우리 아빠 당뇨 있어", "여자친구가 불교 신자야", "형이 교도소 다녀왔어", "동료가 게이래",
	"외국인등록번호 900101-5123456", "여권 M12345678", "저 공황장애 있어요", "제 연봉 6천이에요", "나 천식 있어",
	"여권 새로 만들었고 번호는 M12345678 이야",
	// round 4: first-person/third-party politics/health
	"나 민주당 당원이야", "저는 진보 성향이에요", "나 혈압약 먹어",
	"아내가 유방암 수술 받았어", "저 ADHD 약 먹어요",
}
// REQUIRED before memory default-on (architect, 2026-10-27): indirect vote preference
// is political and must always be blocked, independent of QA_STRICT.
var qaVotePreferenceRequired = []string{
	"지난 선거에 2번 찍었어", "이번 대선은 1번 뽑을 거야", "나는 항상 보수 쪽 후보 찍어", "우리 남편은 매번 민주당 찍어",
	// round 8: 기호 = ballot number, discloses a vote (architect R1)
	"기호 2번 찍었어",
}

// Reported only (policy-dependent): invalid-date ARN should NOT be rejected as ID.
var qaFreshInfo = []string{"외국인등록번호 아님: 991340-5123456", "991340-5123456", "900101-5123456"}

func TestQA_VotePreferenceRequired(t *testing.T) {
	for _, s := range qaVotePreferenceRequired {
		if CheckSensitive(s) == nil {
			t.Errorf("REQUIRED (pre default-on): indirect vote preference not blocked: %q", s)
		}
	}
}

func TestQA_SensitiveFresh(t *testing.T) {
	var fp, fn int
	for _, s := range qaFreshOrdinary {
		if err := CheckSensitive(s); err != nil {
			fp++
			t.Logf("FP %q -> %v", s, err)
		}
	}
	for _, s := range qaFreshSensitive {
		if CheckSensitive(s) == nil {
			fn++
			t.Logf("FN %q", s)
		}
	}
	for _, s := range qaFreshInfo {
		t.Logf("INFO %q -> %v", s, CheckSensitive(s))
	}
	t.Logf("fresh ordinary=%d FP=%d | sensitive=%d FN=%d", len(qaFreshOrdinary), fp, len(qaFreshSensitive), fn)
	if os.Getenv("QA_STRICT") == "1" && fp+fn > 0 {
		t.Fail()
	}
}

// Round 6 (#45 6633c65): vote-number rule only in election context. Non-election
// number + 찍다/뽑다 must NOT be blocked (strict). First four from backend, rest QA.
var qaVoteNonElectionAllow = []string{
	"사진 10번 찍었어", "가챠 5번 뽑았어", "시험에서 3번 찍었어", "반장 선거에서 2번 후보 뽑았어",
	"도장 3번 찍었어", "제비뽑기에서 1번 뽑았어", "객관식 4번 찍었는데 틀렸어", "신입 2명 뽑았어",
	"영상 2번 찍고 편집했어", "카드 3번 뽑았는데 다 꽝이야", "보수 공사 업체 2번 뽑았어",
}

func TestQA_VoteNonElectionAllowed(t *testing.T) {
	for _, s := range qaVoteNonElectionAllow {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("non-election number+찍다/뽑다 blocked: %q -> %v", s, err)
		}
	}
}

// Round 8 (#53): political leaning with jamo / spacing / homoglyph obfuscation must be blocked (strict).
var qaPoliticalVariantsBlock = []string{
	"민ㅈㅜ당 지지해", "민 주 당 지지해", "민\u200b주당 지지해", "국민의 힘 찍었어", "국민의힘 지지해",
	"기호2번 찍었어", "기호 ２번 찍었어", "기호 2 번 찍었어요",
	// round 9
	"기호 3번 찍을게", "투표는 2번에 했어", "정의당 지지.", "개혁신당 지지!", "조국혁신당, 지지해", "진보당을 지지해요", "국민 의 힘 찍었다", "더불어 민주당 뽑았어", "녹색당…지지함",
}

// Benign controls next to the variants above (strict allow).
var qaPoliticalBenign = []string{
	"기호 1번 후보 토론 영상 찾아줘", "팀 대표 뽑았어", "기호 2번 후보 공약 비교해줘",
	"국민의힘 대변인 발표 요약해줘", "민주당 지지율 기사 요약해줘", "가챠에서 2번 뽑았어",
	// round 9
	"정의당 새 대표 기사 요약해줘", "개혁신당 창당 과정 정리해줘", "이번 총선 후보가 누구야?", "기호 3번 후보 이름 알려줘", "투표 2번 해도 돼?", "조국혁신당 의석수 몇 석이야?",
}

func TestQA_PoliticalVariants(t *testing.T) {
	for _, s := range qaPoliticalVariantsBlock {
		if CheckSensitive(s) == nil {
			t.Errorf("political variant not blocked: %q", s)
		}
	}
	for _, s := range qaPoliticalBenign {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("benign control blocked: %q -> %v", s, err)
		}
	}
}

// Round 10 (#53 adf4b72): fresh unseen political variants (not tuned to earlier misses).
var qaR10Block = []string{
	// punctuation between party and verb
	"정의당·지지해", "민주당/지지함", "국민의힘~ 찍었음", "진보당? 지지자야", `"개혁신당" 지지해`, "'조국혁신당' 찍었어", "국민의힘 👍 지지",
	// suffix forms
	"녹색당 지지자임", "민주당 찍었음", "나 정의당 지지함", "우리 아빠는 국민의힘 지지자야",
	// vote-number disclosures
	"2번에 도장 찍었어", "투표용지에 1번 찍었어", "대선 때 3번 뽑았음", "총선에서 2번 찍음", "사전투표 때 1번에 했어", "이번 투표는 1번에 찍었다",
}

var qaR10Benign = []string{
	"투표는 2번 해야 돼?", "정의당·녹색당 합당 뉴스 요약해줘", "민주당/국민의힘 의석수 비교해줘", `"개혁신당" 창당일 알려줘`,
	"투표용지 디자인 바뀌었대", "이 서류 2번에 도장 찍어야 해?", "후보 2번이 누구야?", "사진 2번 찍었음", "지지대 2번 다시 박았어",
}

// Policy questions (logged, not failed): school / meeting votes with a number.
var qaR10Policy = []string{"회의에서 투표 2번 했어", "반장 투표는 2번에 했어", "동아리 회장 선거에서 1번 찍었어", "투표용지에 1번"}

func TestQA_PoliticalUnseenR10(t *testing.T) {
	for _, s := range qaR10Block {
		if CheckSensitive(s) == nil {
			t.Errorf("R10 political variant not blocked: %q", s)
		}
	}
	for _, s := range qaR10Benign {
		if err := CheckSensitive(s); err != nil {
			t.Errorf("R10 benign blocked: %q -> %v", s, err)
		}
	}
	for _, s := range qaR10Policy {
		t.Logf("POLICY %q -> %v", s, CheckSensitive(s))
	}
}

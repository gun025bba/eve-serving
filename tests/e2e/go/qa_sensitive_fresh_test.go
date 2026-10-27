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
	"이번 대선은 1번 뽑을 거야", "나는 항상 보수 쪽 후보 찍어", "우리 남편은 매번 민주당 찍어",
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

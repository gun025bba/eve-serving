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
}
var qaFreshSensitive = []string{
	"나 HIV 양성이야", "우리 아빠 당뇨 있어", "여자친구가 불교 신자야", "형이 교도소 다녀왔어", "동료가 게이래",
	"외국인등록번호 900101-5123456", "여권 M12345678", "저 공황장애 있어요", "제 연봉 6천이에요", "나 천식 있어",
	"여권 새로 만들었고 번호는 M12345678 이야",
}
// Reported only (policy-dependent): invalid-date ARN should NOT be rejected as ID.
var qaFreshInfo = []string{"외국인등록번호 아님: 991340-5123456", "991340-5123456", "900101-5123456"}

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

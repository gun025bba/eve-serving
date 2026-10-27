package memory

// QA (PR #34 re-review): unseen sentences to detect overfitting to the
// first corpus. Logs FP/FN; QA_STRICT=1 fails on them.
import (
	"os"
	"testing"
)

var qaUnseenOrdinary = []string{
	"게이트 키퍼 패턴 적용했어", "게이지 차트 색 바꿔줘", "전과정 자동화 스크립트 짜줘", "이전과 비교해서 빨라졌어",
	"절에서 템플스테이 해보고 싶어", "교회 건물 건축 양식이 멋지다", "소득세 신고 기간 언제야", "신용카드 혜택 비교해줘",
	"당뇨에 좋은 음식 기사 봤어", "회사 노조 뉴스 봤어", "민족 문화 박물관 가자", "수감자 인권 다큐 추천",
}
var qaUnseenSensitive = []string{
	"저 게이예요", "나 전과 있어", "불교 신자예요", "당뇨 있어", "저는 레즈비언이에요", "매주 성당 나가요",
	"작년에 교도소에서 출소했어", "갑상선 약 복용 중이야", "내 연봉은 8천이야", "우울증 치료 받고 있어",
	"저 HIV 양성이에요", "전 개신교 신자예요", "나 이슬람교 믿어", "빚이 3억 있어", "제 신용점수 낮아요",
}

func TestQA_SensitiveUnseen(t *testing.T) {
	var fp, fn int
	for _, s := range qaUnseenOrdinary {
		if err := CheckSensitive(s); err != nil {
			fp++
			t.Logf("FP %q -> %v", s, err)
		}
	}
	for _, s := range qaUnseenSensitive {
		if CheckSensitive(s) == nil {
			fn++
			t.Logf("FN %q", s)
		}
	}
	t.Logf("unseen ordinary=%d FP=%d | sensitive=%d FN=%d", len(qaUnseenOrdinary), fp, len(qaUnseenSensitive), fn)
	if os.Getenv("QA_STRICT") == "1" && fp+fn > 0 {
		t.Fail()
	}
}

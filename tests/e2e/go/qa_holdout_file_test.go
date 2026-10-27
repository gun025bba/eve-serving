package memory

// Private holdout runner: reads TSV (kind<TAB>sentence) files named in QA_HOLDOUT_FILES
// (colon-separated). Files live outside the repo and are never committed.
import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func TestQA_PrivateHoldoutFile(t *testing.T) {
	files := os.Getenv("QA_HOLDOUT_FILES")
	if files == "" {
		t.Skip("QA_HOLDOUT_FILES not set")
	}
	for _, p := range strings.Split(files, ":") {
		f, err := os.Open(p)
		if err != nil {
			t.Fatalf("open holdout: %v", err)
		}
		n, ok := map[string]int{}, map[string]int{}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			kind, s, found := strings.Cut(sc.Text(), "\t")
			if !found {
				continue
			}
			blocked := CheckSensitive(s) != nil
			n[kind]++
			if blocked == (kind != "allow") {
				ok[kind]++
			} else {
				t.Logf("MISS[%s] %q blocked=%v", kind, s, blocked)
			}
		}
		f.Close()
		t.Logf("%s: core %d/%d | allow FP %d/%d | recall %d/%d", p[strings.LastIndex(p, "/")+1:], ok["core"], n["core"], n["allow"]-ok["allow"], n["allow"], ok["recall"], n["recall"])
	}
}

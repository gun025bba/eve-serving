package tools

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// atomicWrite replaces path the way an operator must: temp file in the same
// directory, then rename.
func atomicWrite(t *testing.T, path, body string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestKillSwitch_AutoSelectOffKey(t *testing.T) {
	dir := t.TempDir()
	p := writeKill(t, dir, "auto_select_off: true\n", 0o600)
	k := NewKillswitch(p, os.Getuid())
	if r := k.Reload(); r != "ok" || !k.AutoSelectOff() || k.Disabled("web_search") {
		t.Fatal(r, k)
	}
	atomicWrite(t, p, "auto_select_off: false\n")
	if k.Reload(); k.AutoSelectOff() {
		t.Fatal("auto_select_off false must switch it back on")
	}
	atomicWrite(t, p, "auto_select_of: true\n") // unknown key: fail closed
	if k.Reload(); !k.AutoSelectOff() {
		t.Fatal("unknown key must fail closed")
	}
	var nilK *Killswitch
	if nilK.AutoSelectOff() {
		t.Fatal("nil switch must not turn anything off")
	}
}

func TestKillSwitch_PollDetectsChangeAndIgnoresNoChange(t *testing.T) {
	dir := t.TempDir()
	p := writeKill(t, dir, "auto_select_off: false\n", 0o600)
	k := NewKillswitch(p, os.Getuid())
	k.Reload()
	if k.Poll() {
		t.Fatal("poll without a change must not reload")
	}
	atomicWrite(t, p, "auto_select_off: true\n")
	if !k.Poll() || !k.AutoSelectOff() {
		t.Fatal("poll must see the replaced file")
	}
	if k.Poll() {
		t.Fatal("second poll without a change")
	}
	os.Chmod(p, 0o644) // a mode change is a change, and a bad mode fails closed
	if !k.Poll() || !k.AutoSelectOff() {
		t.Fatal("mode change not seen")
	}
}

func TestKillSwitch_AtomicReplaceNoPartialRead(t *testing.T) {
	dir := t.TempDir()
	p := writeKill(t, dir, "disabled: [web_fetch]\n", 0o600)
	k := NewKillswitch(p, os.Getuid())
	k.Reload()

	// A writer that is half way through the temp file while the poller runs.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte("auto_sel"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sawAllOff atomic.Bool
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			k.Poll()
			if k.Disabled("web_search") || k.AutoSelectOff() { // everything-off look
				sawAllOff.Store(true)
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	if k.Disabled("web_search") || k.AutoSelectOff() || !k.Disabled("web_fetch") {
		t.Fatal("a half-written temp file changed the state")
	}
	// finish the temp file and swap it in
	if err := os.WriteFile(tmp, []byte("auto_select_off: true\ndisabled: [web_fetch]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !k.AutoSelectOff() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if !k.AutoSelectOff() {
		t.Fatal("new state not visible after the rename")
	}
	// the old state held until the swap: auto_select_off was false and nothing but
	// web_fetch was off before; the poller must not have seen "all off" earlier
	if sawAllOff.Load() && k.Disabled("web_search") {
		t.Fatal("the poller saw everything off")
	}
	if k.Disabled("web_search") {
		t.Fatal("web_search must stay on: only auto_select_off and web_fetch are set")
	}
}

func TestKillSwitch_VisibleWithin5s(t *testing.T) {
	t.Run("injected clock", func(t *testing.T) {
		dir := t.TempDir()
		p := writeKill(t, dir, "auto_select_off: false\n", 0o600)
		k := NewKillswitch(p, os.Getuid())
		k.Reload()
		ticks := make(chan time.Time)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { k.WatchTicks(ctx, ticks); close(done) }()
		atomicWrite(t, p, "auto_select_off: true\n")
		// one tick of the 2 s clock is enough, no SIGHUP involved
		ticks <- time.Now()
		ticks <- time.Now() // the second send returns only after the first poll finished
		if !k.AutoSelectOff() {
			t.Fatal("one poll tick did not apply the change")
		}
		cancel()
		<-done
	})
	t.Run("real clock", func(t *testing.T) {
		if KillswitchPollInterval > 2*time.Second {
			t.Fatalf("poll interval %v exceeds the 2 s of the design", KillswitchPollInterval)
		}
		dir := t.TempDir()
		p := writeKill(t, dir, "auto_select_off: false\n", 0o600)
		k := NewKillswitch(p, os.Getuid())
		k.Reload()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go k.Watch(ctx)
		t0 := time.Now()
		atomicWrite(t, p, "auto_select_off: true\n")
		for !k.AutoSelectOff() {
			if time.Since(t0) > 5*time.Second {
				t.Fatal("not visible within 5 s without SIGHUP")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if el := time.Since(t0); el > 5*time.Second {
			t.Fatal(el)
		}
	})
}

func TestKillSwitch_FileCreatedAfterStartupIsPickedUp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tools-killswitch.yaml")
	k := NewKillswitch(p, os.Getuid())
	k.Reload() // missing at startup: no tool disabled, automatic selection off (N-2)
	if !k.AutoSelectOff() || k.Disabled("web_search") {
		t.Fatal("a missing file must keep automatic selection off and disable no tool")
	}
	atomicWrite(t, p, "auto_select_off: true\n")
	if !k.Poll() || !k.AutoSelectOff() {
		t.Fatal("a file created later must be applied by polling")
	}
}

// §6.7 rollback order: the candidate that sets `disabled` for the web tools
// first is valid for this build and (without auto_select_off) for a 5.x build;
// the candidate that still carries auto_select_off is flagged for 5.x.
func TestKillswitchCheck_RollbackSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools-killswitch.yaml")
	chk := func(body string) KillswitchReport {
		atomicWrite(t, path, body)
		return CheckKillswitchFile(path, os.Getuid())
	}
	// step 1: web tools disabled with a key old builds know, plus auto_select_off
	r := chk("disabled: [web_search, web_fetch]\nauto_select_off: true\n")
	if r.Result != "ok" || !r.WebToolsOff || !r.AutoSelectOff {
		t.Fatalf("step 1: %+v", r)
	}
	if r.OldBuild == "valid" {
		t.Fatal("a 5.x build must reject auto_select_off (it would turn every tool off): killswitch-check has to say so")
	}
	// step 2: auto_select_off removed, disabled stays: valid for both builds, web tools off
	r = chk("disabled: [web_search, web_fetch]\n")
	if r.Result != "ok" || !r.WebToolsOff || r.OldBuild != "valid" {
		t.Fatalf("step 2: %+v", r)
	}
	// a file that does not parse is "every tool off" (fail closed), also for the check
	r = chk("disabled: [web_search\n")
	if r.Result == "ok" || !r.AllOff || !r.WebToolsOff {
		t.Fatalf("broken file: %+v", r)
	}
}

// N-2: no file at startup keeps the web tools off until a valid file exists; a
// valid empty file turns them on; removing the file later keeps the last state.
func TestKillSwitch_MissingFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tools-killswitch.yaml")
	k := NewKillswitch(p, os.Getuid())
	k.Reload()
	if !k.AutoSelectOff() {
		t.Fatal("web tools on without a kill switch file")
	}
	atomicWrite(t, p, "disabled: []\n")
	if !k.Poll() || k.AutoSelectOff() {
		t.Fatal("a valid file must enable automatic selection")
	}
	os.Remove(p)
	k.Poll()
	if k.AutoSelectOff() {
		t.Fatal("deleting the file must keep the previous state")
	}
	// a file that was never valid and then disappears stays off
	k2 := NewKillswitch(filepath.Join(dir, "other.yaml"), os.Getuid())
	k2.Reload()
	k2.Poll()
	if !k2.AutoSelectOff() {
		t.Fatal("still no file: off")
	}
}

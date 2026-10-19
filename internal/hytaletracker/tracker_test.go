package hytaletracker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/hytale"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

type fakeCmd struct {
	mu   sync.Mutex
	sent []string
	done chan struct{}
	// Mirrors command.Sender's observer, as main wires it
	observe func(serverID, cmd string)
}

func (f *fakeCmd) Run(_ context.Context, id string, cmd string, _ bool) (string, error) {
	if f.observe != nil {
		f.observe(id, cmd)
	}
	f.mu.Lock()
	f.sent = append(f.sent, cmd)
	f.mu.Unlock()
	f.done <- struct{}{}
	return "", nil
}

func (f *fakeCmd) wait(t *testing.T, n int) []string {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-f.done:
		case <-time.After(2 * time.Second):
			t.Fatalf("waited for %d commands, got %v", n, f.sent)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

func (f *fakeCmd) count() int {
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type fakeTimers struct {
	fns []func()
}

// Clock the tests move by hand
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func rig(t *testing.T) (*Tracker, *fakeCmd, *fakeTimers, *clock, string) {
	t.Helper()
	tr := New(nil)
	cmd := &fakeCmd{done: make(chan struct{}, 16)}
	cmd.observe = tr.NoteConsoleCommand
	timers := &fakeTimers{}
	clk := &clock{t: time.Now()}
	tr.SetCommander(cmd)
	tr.now = clk.now
	tr.after = func(_ time.Duration, fn func()) *time.Timer {
		timers.fns = append(timers.fns, fn)
		return time.NewTimer(time.Hour)
	}
	return tr, cmd, timers, clk, t.TempDir()
}

func feed(tr *Tracker, id string, lines ...string) {
	for _, l := range lines {
		tr.Observe(id, l)
	}
}

const frame = "[2026/01/17 12:00:01   INFO]   [AuthCommand] "

var deviceLines = []string{
	frame + "DEVICE AUTHORIZATION",
	frame + "Visit: https://accounts.hytale.com/device",
	frame + "Enter code: ABCD-1234",
	frame + "Or visit: https://accounts.hytale.com/device?user_code=ABCD-1234",
	frame + "Waiting for authorization (expires in 900 seconds)...",
}

func TestServerSignInFlow(t *testing.T) {
	tr, cmd, _, _, dir := rig(t)
	tr.BeginRun("s1", dir)

	feed(tr, "s1", "Token validation failed. Server starting unauthenticated.")
	if st := tr.Status("s1"); st.Auth != v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_OUT {
		t.Errorf("auth = %v", st.Auth)
	}
	feed(tr, "s1", "[INFO] Server started on 0.0.0.0:5520")
	if got := cmd.wait(t, 1); !slices.Equal(got, []string{LoginCommand}) {
		t.Fatalf("sent %v", got)
	}

	feed(tr, "s1", deviceLines...)
	st := tr.Status("s1")
	if st.Auth != v1.HytaleAuthState_HYTALE_AUTH_STATE_WAITING || st.SignIn == nil || st.SignIn.Step != 2 || st.SignIn.Code != "ABCD-1234" {
		t.Fatalf("waiting state = %+v", st)
	}
	if left := time.Until(st.SignIn.ExpiresAt.AsTime()); left < 890*time.Second || left > 901*time.Second {
		t.Errorf("expiry in %v", left)
	}

	feed(tr, "s1", "Authentication successful! Mode: OAUTH_DEVICE")
	if got := cmd.wait(t, 1); !slices.Equal(got, []string{LoginCommand, PersistenceCommand}) {
		t.Fatalf("sent %v", got)
	}
	st = tr.Status("s1")
	if st.Auth != v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_IN || st.SignIn != nil || !st.Booted {
		t.Errorf("signed in state = %+v", st)
	}

	// Repeats never resend
	feed(tr, "s1", "Authentication successful! Mode: OAUTH_DEVICE", "[INFO] Server started on 0.0.0.0:5520")
	if cmd.count() != 2 {
		t.Errorf("resent commands: %d", cmd.count())
	}
}

func TestChatCannotDriveTheTracker(t *testing.T) {
	tr, cmd, timers, clk, dir := rig(t)
	tr.BeginRun("s1", dir)
	feed(tr, "s1", "[INFO] Server started on 0.0.0.0:5520")

	// After boot, with no login sent by the panel, nothing in chat counts
	chat := []string{
		"[INFO] [Chat] <Mallory> use /auth login please",
		"<Mallory> No server tokens configured",
		"[INFO] [Chat] DEVICE AUTHORIZATION",
		"<Mallory> https://accounts.hytale.com/device?user_code=EVIL-CODE",
		"my code ABCD1234",
		"<Steve> Steve joined the server",
		"[INFO] [Chat] Steve (UUID: 1234abcd-0000) connected from 1.2.3.4",
		"<Mallory> authentication successful",
		// Framed like the server but after boot, outside any login window
		"Server starting unauthenticated.",
		"Authentication successful! Mode: OAUTH_DEVICE",
		frame + "DEVICE AUTHORIZATION",
		frame + "Enter code: EVIL-0000",
	}
	feed(tr, "s1", chat...)
	st := tr.Status("s1")
	if st.Auth != v1.HytaleAuthState_HYTALE_AUTH_STATE_UNSPECIFIED || st.SignIn != nil || st.PlayersKnown {
		t.Errorf("chat changed state: %+v", st)
	}
	if cmd.count() != 0 || len(timers.fns) != 0 {
		t.Errorf("chat triggered %d commands, %d timers", cmd.count(), len(timers.fns))
	}

	// A real login request: a chat-posted code before the server's own block is ignored,
	// and a second block cannot replace the first code
	if err := tr.RequestSignIn(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	cmd.wait(t, 1)
	feed(tr, "s1",
		"<Mallory> Enter code: EVIL-0000",
		"[INFO] [Chat] Visit: https://accounts.hytale.com/device?user_code=EVIL-0000",
	)
	if st := tr.Status("s1"); st.SignIn != nil {
		t.Fatalf("chat code shown: %+v", st.SignIn)
	}
	feed(tr, "s1", deviceLines...)
	feed(tr, "s1", frame+"DEVICE AUTHORIZATION", frame+"Enter code: EVIL-0000")
	if st := tr.Status("s1"); st.SignIn == nil || st.SignIn.Code != "ABCD-1234" {
		t.Fatalf("sign-in = %+v", st.SignIn)
	}

	// The header must come soon after the login, not minutes later
	tr2, cmd2, _, clk2, dir2 := rig(t)
	tr2.BeginRun("s2", dir2)
	_ = tr2.RequestSignIn(context.Background(), "s2")
	cmd2.wait(t, 1)
	clk2.add(2 * time.Minute)
	feed(tr2, "s2", deviceLines...)
	if st := tr2.Status("s2"); st.SignIn != nil {
		t.Error("late device block accepted")
	}
	_ = clk
}

func TestOperatorConsoleLoginShowsCode(t *testing.T) {
	tr, _, _, _, dir := rig(t)
	tr.BeginRun("s1", dir)
	feed(tr, "s1", "[INFO] Server started on 0.0.0.0:5520")
	tr.NoteConsoleCommand("s1", "auth login device")
	feed(tr, "s1", deviceLines...)
	if st := tr.Status("s1"); st.SignIn == nil || st.SignIn.Code != "ABCD-1234" {
		t.Errorf("console login code not shown: %+v", st.SignIn)
	}
	if !IsLoginCommand("/AUTH Login device") || IsLoginCommand("/auth status") || IsLoginCommand("say auth login") {
		t.Error("login command detection")
	}
}

func TestPersistenceSkippedWhenEncrypted(t *testing.T) {
	tr, cmd, _, _, dir := rig(t)
	if err := os.WriteFile(filepath.Join(dir, hytale.AuthEncFile), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	tr.BeginRun("s1", dir)
	_ = tr.RequestSignIn(context.Background(), "s1")
	cmd.wait(t, 1)
	feed(tr, "s1", "Authentication successful! Mode: OAUTH_DEVICE")
	if cmd.count() != 1 {
		t.Errorf("persistence re-requested with auth.enc present")
	}
}

func TestBootSuccessSetsStateWithoutCommands(t *testing.T) {
	tr, cmd, _, _, dir := rig(t)
	tr.BeginRun("s1", dir)
	feed(tr, "s1", "Authentication successful! Mode: OAUTH_DEVICE")
	if st := tr.Status("s1"); st.Auth != v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_IN {
		t.Errorf("boot success ignored: %v", st.Auth)
	}
	if cmd.count() != 0 {
		t.Error("boot success sent a command")
	}
}

func TestReplayedTailSendsNothingAndShowsNoCode(t *testing.T) {
	tr, cmd, timers, _, dir := rig(t)
	tr.Track("s1", dir)
	// Panel restart: the collector reports a container that started long ago
	tr.ObserveContainer("s1", time.Now().Add(-time.Hour), true)
	feed(tr, "s1", "No server tokens configured", "[INFO] Server started on 0.0.0.0:5520")
	feed(tr, "s1", deviceLines...)
	feed(tr, "s1", "Authentication successful! Mode: OAUTH_DEVICE")
	if cmd.count() != 0 || len(timers.fns) != 0 {
		t.Fatalf("replay sent %d commands, %d timers", cmd.count(), len(timers.fns))
	}
	st := tr.Status("s1")
	if st.SignIn != nil {
		t.Error("replayed device code shown as live")
	}
	if !st.Booted || st.Auth != v1.HytaleAuthState_HYTALE_AUTH_STATE_UNSPECIFIED {
		t.Errorf("replay state = %+v", st)
	}
}

func TestFallbackLoginWithoutBootLine(t *testing.T) {
	tr, cmd, timers, _, dir := rig(t)
	tr.BeginRun("s1", dir)
	feed(tr, "s1", "Use /auth login to authenticate.")
	if len(timers.fns) != 1 {
		t.Fatalf("timers = %d", len(timers.fns))
	}
	timers.fns[0]()
	if got := cmd.wait(t, 1); !slices.Equal(got, []string{LoginCommand}) {
		t.Fatalf("sent %v", got)
	}

	// A timer from an earlier run does nothing
	tr.BeginRun("s1", dir)
	feed(tr, "s1", "Use /auth login to authenticate.")
	tr.EndRun("s1")
	timers.fns[1]()
	if cmd.count() != 1 {
		t.Errorf("stale timer sent a command")
	}
}

func TestUnauthenticatedAfterBootPhaseIgnored(t *testing.T) {
	tr, cmd, timers, clk, dir := rig(t)
	tr.BeginRun("s1", dir)
	clk.add(4 * time.Minute)
	feed(tr, "s1", "Use /auth login to authenticate.")
	if len(timers.fns) != 0 || cmd.count() != 0 {
		t.Error("late unauthenticated line acted on")
	}
}

func TestContainerRestartsResetRun(t *testing.T) {
	tr, _, _, _, dir := rig(t)
	tr.BeginRun("s1", dir)
	start := time.Now()
	tr.ObserveContainer("s1", start, true)
	feed(tr, "s1", "[INFO] Server started", "Kai joined the server")
	if !tr.Booted("s1") {
		t.Fatal("not booted")
	}
	// Same container start keeps the run
	tr.ObserveContainer("s1", start, true)
	if !tr.Booted("s1") {
		t.Fatal("run reset without a restart")
	}
	// Crash and docker restart: new start time
	tr.ObserveContainer("s1", start.Add(time.Minute), true)
	if tr.Booted("s1") {
		t.Error("boot state survived a container restart")
	}
	if _, _, known := tr.PlayersOnline("s1"); known {
		t.Error("players survived a container restart")
	}
	feed(tr, "s1", "[INFO] Server started")
	// In-game stop, container exited
	tr.ObserveContainer("s1", time.Time{}, false)
	if tr.Booted("s1") {
		t.Error("boot state survived a stopped container")
	}
}

func TestPlayersAndExpiry(t *testing.T) {
	tr, _, _, clk, dir := rig(t)
	tr.BeginRun("s1", dir)
	if _, _, known := tr.PlayersOnline("s1"); known {
		t.Error("players known before any join line")
	}
	feed(tr, "s1",
		"Kai (UUID: 1234abcd-01) connected from 10.0.0.2",
		"Mossy (UUID: 1234abcd-02) connected from 10.0.0.3",
		"Kai (UUID: 1234abcd-01) disconnected",
	)
	n, names, known := tr.PlayersOnline("s1")
	if !known || n != 1 || names[0] != "Mossy" {
		t.Errorf("players = %d %v %v", n, names, known)
	}

	start := clk.now()
	tr.SetDownloaderSignIn("s1", hytale.DeviceFlow{URL: "https://accounts.hytale.com/device", Code: "X1Y2"}, start)
	if st := tr.Status("s1"); st.SignIn == nil || st.SignIn.Step != 1 {
		t.Fatalf("downloader sign-in missing: %+v", st)
	}
	clk.add(901 * time.Second)
	if st := tr.Status("s1"); st.SignIn != nil {
		t.Error("expired sign-in still shown")
	}
	tr.SetDownloaderSignIn("s1", hytale.DeviceFlow{Code: "X1Y2"}, clk.now())
	tr.ClearDownloaderSignIn("s1")
	if st := tr.Status("s1"); st.SignIn != nil {
		t.Error("cleared sign-in still shown")
	}

	tr.EndRun("s1")
	if _, _, known := tr.PlayersOnline("s1"); known || tr.Booted("s1") {
		t.Error("run state survived the stop")
	}
}

func TestUntrackedServersIgnored(t *testing.T) {
	tr, cmd, _, _, _ := rig(t)
	feed(tr, "mc", "Kai joined the game", "No server tokens configured")
	tr.NoteConsoleCommand("mc", "/auth login device")
	tr.ObserveContainer("mc", time.Now(), true)
	if tr.Tracked("mc") || cmd.count() != 0 {
		t.Error("minecraft server output changed tracker state")
	}
}

// Follows Hytale server output for boot, sign-in and player state
package hytaletracker

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/hytale"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Console commands the tracker sends for the second sign-in
const (
	LoginCommand       = "/auth login device"
	PersistenceCommand = "/auth persistence Encrypted"
)

// Trust windows. Players and plugins can print anything into the console,
// so auth lines only count where the panel expects them: during boot, when
// no player can be connected yet, or right after a login the panel sent.
const (
	// An unauthenticated server that never prints a recognised boot line
	// still gets a sign-in request this long after the warning
	loginFallback = 45 * time.Second
	// Boot output is trusted this long after the container started
	bootPhase = 3 * time.Minute
	// The device code block must start this soon after the login command
	deviceWindow = 60 * time.Second
	// Lines after the header that may still carry the link and code
	deviceFlowLines = 12
	// A login confirmation counts until the device code ran out
	loginWindow = hytale.DefaultSignInTTL + time.Minute
	// The persistence confirmation must follow the command this soon
	persistWindow = time.Minute
)

// Sends one console command, satisfied by command.Sender
type Commander interface {
	Run(ctx context.Context, serverID, cmd string, silent bool) (string, error)
}

// Per-server observed state, reset on every start
type state struct {
	dataDir string
	// Set by BeginRun, replayed log tails never trigger commands
	actions bool

	// Start of the current run, boot output is trusted near it
	runStart time.Time
	// Container start time the collector last reported
	containerStart time.Time

	booted bool
	auth   v1.HytaleAuthState

	unauthSeen     bool
	loginRequested bool      // The panel asked for a login on its own this run
	loginSentAt    time.Time // Any login the panel sent, by itself or for a user
	flowOpen       bool      // A device block started inside the login window
	flowLines      int
	flowUsed       bool // This login already produced a code, later ones are ignored
	flow           hytale.DeviceFlow
	flowStart      time.Time
	signIn         *v1.HytaleSignIn

	persistRequested bool
	persistSentAt    time.Time
	persistenceSeen  bool

	playersKnown bool
	players      map[string]bool

	fallback *time.Timer
	run      int // Bumped per run so stale timers do nothing
}

// Snapshot of one server's state
type Status struct {
	Booted          bool
	Auth            v1.HytaleAuthState
	SignIn          *v1.HytaleSignIn
	PersistenceSeen bool
	PlayersKnown    bool
	Players         []string
}

type Tracker struct {
	mu      sync.Mutex
	servers map[string]*state
	cmd     Commander
	console func(serverID, message string)
	log     *logger.Logger
	now     func() time.Time
	after   func(time.Duration, func()) *time.Timer
}

func New(log *logger.Logger) *Tracker {
	return &Tracker{
		servers: make(map[string]*state),
		log:     log,
		now:     time.Now,
		after:   time.AfterFunc,
	}
}

// Wires the command path after construction, dependency order
func (t *Tracker) SetCommander(cmd Commander) {
	t.mu.Lock()
	t.cmd = cmd
	t.mu.Unlock()
}

// Wires the console sink for panel notes
func (t *Tracker) SetConsoleSink(sink func(serverID, message string)) {
	t.mu.Lock()
	t.console = sink
	t.mu.Unlock()
}

func (t *Tracker) stateLocked(serverID string) *state {
	s := t.servers[serverID]
	if s == nil {
		s = &state{players: map[string]bool{}}
		t.servers[serverID] = s
	}
	return s
}

// Starts following a Hytale server, keeps any state already observed
func (t *Tracker) Track(serverID, dataDir string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stateLocked(serverID).dataDir = dataDir
}

// Reports whether a server is followed
func (t *Tracker) Tracked(serverID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.servers[serverID] != nil
}

// Drops everything that belongs to one container run
func (s *state) resetRun() {
	if s.fallback != nil {
		s.fallback.Stop()
		s.fallback = nil
	}
	s.run++
	s.booted, s.playersKnown, s.unauthSeen = false, false, false
	s.players = map[string]bool{}
	s.loginRequested, s.persistRequested = false, false
	s.loginSentAt, s.persistSentAt = time.Time{}, time.Time{}
	s.closeFlow()
	if s.signIn != nil && s.signIn.Step == 2 {
		s.signIn = nil
	}
	if s.auth == v1.HytaleAuthState_HYTALE_AUTH_STATE_WAITING {
		s.auth = v1.HytaleAuthState_HYTALE_AUTH_STATE_UNSPECIFIED
	}
}

func (s *state) closeFlow() {
	s.flowOpen, s.flowLines, s.flowUsed = false, 0, false
	s.flow, s.flowStart = hytale.DeviceFlow{}, time.Time{}
}

// Resets run state for a container the lifecycle is starting now
func (t *Tracker) BeginRun(serverID, dataDir string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stateLocked(serverID)
	s.resetRun()
	s.dataDir, s.actions = dataDir, true
	s.runStart, s.containerStart = t.now(), time.Time{}
	// Last known account state shows until the server reports again
	if s.auth != v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_IN {
		s.auth = v1.HytaleAuthState_HYTALE_AUTH_STATE_UNSPECIFIED
	}
}

// Clears run state after the server stopped
func (t *Tracker) EndRun(serverID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.servers[serverID]; s != nil {
		s.resetRun()
		s.actions = false
		s.runStart, s.containerStart = time.Time{}, time.Time{}
	}
}

// Folds the collector's view of the container into the run: a stopped
// container clears run state, a restarted one (crash, restart policy,
// in-game stop and docker restart) starts a fresh run
func (t *Tracker) ObserveContainer(serverID string, startedAt time.Time, running bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.servers[serverID]
	if s == nil {
		return
	}
	if !running {
		if s.booted || s.playersKnown || (s.signIn != nil && s.signIn.Step == 2) || !s.containerStart.IsZero() {
			s.resetRun()
			s.containerStart = time.Time{}
		}
		return
	}
	switch {
	case s.containerStart.IsZero():
		// First sighting: a lifecycle start or a panel restart
		s.containerStart = startedAt
		if s.runStart.IsZero() {
			s.runStart = startedAt
		}
	case !s.containerStart.Equal(startedAt):
		s.resetRun()
		s.containerStart, s.runStart = startedAt, startedAt
	}
}

// Drops a deleted server
func (t *Tracker) Forget(serverID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.servers[serverID]; s != nil && s.fallback != nil {
		s.fallback.Stop()
	}
	delete(t.servers, serverID)
}

// Shows the downloader's device sign-in, step 1 of 2
func (t *Tracker) SetDownloaderSignIn(serverID string, flow hytale.DeviceFlow, started time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stateLocked(serverID)
	s.signIn = &v1.HytaleSignIn{
		Step:      1,
		Url:       flow.URL,
		Code:      flow.Code,
		ExpiresAt: timestamppb.New(started.Add(flow.TTL())),
	}
}

// Removes the downloader sign-in once the download moved on
func (t *Tracker) ClearDownloaderSignIn(serverID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.servers[serverID]; s != nil && s.signIn != nil && s.signIn.Step == 1 {
		s.signIn = nil
	}
}

// Boot output is only trusted near the start, before players can join
func (s *state) inBootPhase(now time.Time) bool {
	return !s.booted && !s.runStart.IsZero() && now.Sub(s.runStart) < bootPhase
}

func (s *state) inLoginWindow(now time.Time) bool {
	return !s.loginSentAt.IsZero() && now.Sub(s.loginSentAt) < loginWindow
}

// Marks a login command the panel sent, the device block may follow
func (s *state) noteLogin(now time.Time) {
	s.loginSentAt = now
	s.closeFlow()
}

// Folds one server console line into the state
func (t *Tracker) Observe(serverID, line string) {
	t.mu.Lock()
	s := t.servers[serverID]
	if s == nil {
		t.mu.Unlock()
		return
	}
	now := t.now()
	var send []string
	sl := hytale.ParseServerLine(line)

	if s.flowOpen {
		s.flowLines++
		if s.flow.FeedServer(sl.Kind, sl.Value, sl.TTL) && s.flow.Code != "" {
			s.signIn = &v1.HytaleSignIn{
				Step:      2,
				Url:       s.flow.URL,
				Code:      s.flow.Code,
				ExpiresAt: timestamppb.New(s.flowStart.Add(s.flow.TTL())),
			}
			s.auth = v1.HytaleAuthState_HYTALE_AUTH_STATE_WAITING
			s.flowUsed = true
		}
		if s.flowLines >= deviceFlowLines {
			s.flowOpen = false
		}
	}

	switch sl.Kind {
	case hytale.LineDeviceHeader:
		// Only the block answering the panel's own login, and only once
		if !s.flowUsed && !s.loginSentAt.IsZero() && now.Sub(s.loginSentAt) < deviceWindow {
			s.flowOpen, s.flowLines = true, 0
			s.flow, s.flowStart = hytale.DeviceFlow{}, now
		}
	case hytale.LineAuthSuccess:
		inLogin := s.inLoginWindow(now)
		if !inLogin && !s.inBootPhase(now) {
			break
		}
		s.auth = v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_IN
		if s.signIn != nil && s.signIn.Step == 2 {
			s.signIn = nil
		}
		s.closeFlow()
		if inLogin && s.actions && !s.persistRequested && !s.persistenceSeen && !hytale.PersistenceEncrypted(s.dataDir) {
			s.persistRequested, s.persistSentAt = true, now
			send = append(send, PersistenceCommand)
		}
		s.loginSentAt = time.Time{}
	case hytale.LineUnauthenticated:
		if !s.inBootPhase(now) {
			break
		}
		s.unauthSeen = true
		if s.auth != v1.HytaleAuthState_HYTALE_AUTH_STATE_WAITING {
			s.auth = v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_OUT
		}
		if s.actions && !s.loginRequested && s.fallback == nil {
			run := s.run
			s.fallback = t.after(loginFallback, func() { t.fallbackLogin(serverID, run) })
		}
	case hytale.LineAuthFailed:
		if s.inLoginWindow(now) && s.auth == v1.HytaleAuthState_HYTALE_AUTH_STATE_WAITING {
			s.auth = v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_OUT
			if s.signIn != nil && s.signIn.Step == 2 {
				s.signIn = nil
			}
			s.closeFlow()
		}
	case hytale.LinePersistenceEncrypted:
		if !s.persistSentAt.IsZero() && now.Sub(s.persistSentAt) < persistWindow {
			s.persistenceSeen = true
		}
	case hytale.LineBooted:
		if s.booted {
			break
		}
		s.booted = true
		if s.actions && s.unauthSeen && !s.loginRequested && s.auth == v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_OUT {
			s.loginRequested = true
			s.noteLogin(now)
			send = append(send, LoginCommand)
		}
	case hytale.LinePlayerJoin:
		s.players[sl.Value] = true
		s.playersKnown = true
	case hytale.LinePlayerLeave:
		delete(s.players, sl.Value)
		s.playersKnown = true
	}
	t.mu.Unlock()

	for _, c := range send {
		t.sendAsync(serverID, c)
	}
}

// Asks for a sign-in when the boot line never matched
func (t *Tracker) fallbackLogin(serverID string, run int) {
	t.mu.Lock()
	s := t.servers[serverID]
	if s == nil || s.run != run || !s.actions || s.loginRequested || s.auth != v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_OUT {
		t.mu.Unlock()
		return
	}
	s.loginRequested = true
	s.noteLogin(t.now())
	t.mu.Unlock()
	t.sendAsync(serverID, LoginCommand)
}

func (t *Tracker) sendAsync(serverID, command string) {
	t.mu.Lock()
	cmd, console := t.cmd, t.console
	t.mu.Unlock()
	if cmd == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := cmd.Run(ctx, serverID, command, false); err != nil {
			if t.log != nil {
				t.log.Warn("hytale: %s on %s failed: %v", command, serverID, err)
			}
			if console != nil {
				console(serverID, "could not run "+command+" ("+err.Error()+"), run it in the console or use Sign in on the Overview tab")
			}
		}
	}()
}

// Reports whether a console command starts a device login
func IsLoginCommand(command string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(command), "/")))
	return len(fields) >= 2 && fields[0] == "auth" && fields[1] == "login"
}

// Notes a login an operator typed into the console, the device block that
// answers it may then be shown on the server page
func (t *Tracker) NoteConsoleCommand(serverID, command string) {
	if !IsLoginCommand(command) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.servers[serverID]; s != nil {
		s.noteLogin(t.now())
	}
}

// Operator asked for a fresh server sign-in
func (t *Tracker) RequestSignIn(ctx context.Context, serverID string) error {
	t.mu.Lock()
	cmd := t.cmd
	if s := t.servers[serverID]; s != nil {
		s.noteLogin(t.now())
		if s.auth == v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_IN {
			// Sign in again starts over, the old login stays until replaced
			s.auth = v1.HytaleAuthState_HYTALE_AUTH_STATE_UNSPECIFIED
		}
		// A new login must be persisted again
		s.persistRequested = false
		s.persistenceSeen = false
		s.actions = true
	}
	t.mu.Unlock()
	if cmd == nil {
		return errors.New("the server console is not available")
	}
	_, err := cmd.Run(ctx, serverID, LoginCommand, false)
	return err
}

// Copies one server's state, expired sign-ins are dropped
func (t *Tracker) Status(serverID string) Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.servers[serverID]
	if s == nil {
		return Status{}
	}
	if s.signIn != nil && s.signIn.ExpiresAt != nil && t.now().After(s.signIn.ExpiresAt.AsTime()) {
		if s.signIn.Step == 2 && s.auth == v1.HytaleAuthState_HYTALE_AUTH_STATE_WAITING {
			s.auth = v1.HytaleAuthState_HYTALE_AUTH_STATE_SIGNED_OUT
		}
		s.signIn = nil
		s.closeFlow()
	}
	st := Status{
		Booted:          s.booted,
		Auth:            s.auth,
		PersistenceSeen: s.persistenceSeen,
		PlayersKnown:    s.playersKnown,
	}
	if s.signIn != nil {
		st.SignIn = proto.Clone(s.signIn).(*v1.HytaleSignIn)
	}
	if s.playersKnown {
		st.Players = slices.Sorted(maps.Keys(s.players))
	}
	return st
}

// Reports whether the server logged its boot line this run
func (t *Tracker) Booted(serverID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.servers[serverID]
	return s != nil && s.booted
}

// Player count from console lines, known only after a join or leave matched
func (t *Tracker) PlayersOnline(serverID string) (int, []string, bool) {
	st := t.Status(serverID)
	return len(st.Players), st.Players, st.PlayersKnown
}

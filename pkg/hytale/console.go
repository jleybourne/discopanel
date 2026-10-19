package hytale

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Two sources of sign-in text are parsed here. Downloader output comes from
// its own one-shot container that runs nothing else, so it is matched as
// free text (DeviceFlow). Server console output mixes in player chat and
// plugin logging, so server lines are only trusted when the message, after
// the server's own log frame, starts with the exact text the server prints
// (ParseServerLine). The documented texts are the device flow lines and the
// auth messages; the boot and player lines are best guesses, so callers
// treat a miss as "unknown", never as a fact. Keep all patterns here so a
// real log can correct them in one place.
var (
	// Account domain links, or device and verify pages on any Hytale host
	signInURLPattern = regexp.MustCompile(`https://(?:(?:[A-Za-z0-9-]+\.)*accounts\.hytale\.com/[^\s"'<>)\]]*|(?:[A-Za-z0-9-]+\.)*hytale\.com/[^\s"'<>)\]]*(?:device|verify|activate|oauth)[^\s"'<>)\]]*)`)
	// user_code query parameter of a verification link
	userCodeParamPattern = regexp.MustCompile(`[?&]user_code=([A-Za-z0-9-]{4,32})`)
	// "Enter code: ABCD-1234", "Authorization code: ABCD1234", "code ABCD-1234"
	codeLinePattern = regexp.MustCompile(`(?i:\bcode\b)\s*[:=]?\s*([A-Z0-9]{4,12}(?:-[A-Z0-9]{2,12})?)\b`)
	// "Waiting for authorization (expires in 900 seconds)..."
	expiresPattern = regexp.MustCompile(`(?i)expires?\s+in\s+(\d{1,5})\s*(s|sec|secs|seconds?|m|min|mins|minutes?)\b`)
)

// Server log frame: an optional "[date time LEVEL]" or "date time LEVEL"
// prefix, then optional "[LoggerName]" tags. Anything else in front of the
// message, a chat "<Name>" or "Name:" included, makes the anchors below miss.
var (
	ansiPattern       = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	bracketTimeFrame  = regexp.MustCompile(`^\[[^\[\]]*\b(?:TRACE|DEBUG|FINE|FINER|FINEST|CONFIG|INFO|WARN|WARNING|ERROR|SEVERE|FATAL)\b[^\[\]]*\]\s*`)
	plainTimeFrame    = regexp.MustCompile(`^\d{4}[-/]\d{2}[-/]\d{2}[ T]\d{2}:\d{2}:\d{2}\S*\s+(?:TRACE|DEBUG|FINE|INFO|WARN|WARNING|ERROR|SEVERE|FATAL)\s+`)
	loggerTagFrame    = regexp.MustCompile(`^\[([A-Za-z][A-Za-z0-9_.$|-]{0,63})\]\s*`)
	untrustedLoggerRe = regexp.MustCompile(`(?i)chat|say|whisper|msg|message|player|broadcast`)
)

// Anchored server messages, matched against the text after the frame
var (
	deviceHeaderMsg = regexp.MustCompile(`^DEVICE AUTHORIZATION\s*$`)
	visitMsg        = regexp.MustCompile(`^(?:Or )?[Vv]isit:\s*(https://\S+)\s*$`)
	enterCodeMsg    = regexp.MustCompile(`^Enter code:\s*([A-Z0-9]{4,12}(?:-[A-Z0-9]{2,12})?)\s*$`)
	waitingMsg      = regexp.MustCompile(`^Waiting for authorization \(expires in (\d{1,5}) seconds\)`)

	authSuccessMsg     = regexp.MustCompile(`^Authentication successful\b`)
	unauthenticatedMsg = regexp.MustCompile(`^(?:Token validation failed\.?\s*)?(?:Server starting unauthenticated\b|No server tokens configured\b|Use /auth login\b)`)
	authFailedMsg      = regexp.MustCompile(`^(?:Device code|Authorization|Authentication)\b[^<>]*\b(?:expired|timed out|failed|denied)\b`)
	persistenceMsg     = regexp.MustCompile(`(?i)^(?:auth(?:entication)?\s+|credential\s+)?(?:store\s+|storage\s+)?persistence\b[^<>]*\bencrypted\b`)

	bootedMsg      = regexp.MustCompile(`^(?:Server started\b|Hytale Server Booted\b|Universe (?:is )?ready\b)`)
	playerJoinMsg  = regexp.MustCompile(`^([A-Za-z0-9_]{1,32}) (?:\(UUID: [0-9A-Fa-f-]{8,40}\) connected\b|(?:has )?joined the (?:game|server|world)\b)`)
	playerLeaveMsg = regexp.MustCompile(`^([A-Za-z0-9_]{1,32}) (?:\(UUID: [0-9A-Fa-f-]{8,40}\) disconnected\b|(?:has )?left the (?:game|server|world)\b)`)
)

// Strips the server's log frame, false for lines from chat-like loggers
func ServerMessage(line string) (string, bool) {
	msg := strings.TrimSpace(ansiPattern.ReplaceAllString(line, ""))
	if m := bracketTimeFrame.FindString(msg); m != "" {
		msg = msg[len(m):]
	} else if m := plainTimeFrame.FindString(msg); m != "" {
		msg = msg[len(m):]
	}
	for range 2 {
		m := loggerTagFrame.FindStringSubmatch(msg)
		if m == nil {
			break
		}
		if untrustedLoggerRe.MatchString(m[1]) {
			return "", false
		}
		msg = msg[len(m[0]):]
	}
	return strings.TrimSpace(msg), true
}

// Device codes last this long when the tool prints no expiry
const DefaultSignInTTL = 900 * time.Second

// A device sign-in assembled from successive output lines
type DeviceFlow struct {
	URL       string
	Code      string
	ExpiresIn time.Duration
}

// Ready once a link or a code has been seen
func (f *DeviceFlow) Ready() bool {
	return f.URL != "" || f.Code != ""
}

// Folds one downloader output line into the flow, free text since only the
// downloader writes to its container
func (f *DeviceFlow) Feed(line string) bool {
	changed := false
	if u := signInURLPattern.FindString(line); u != "" {
		changed = f.offerURL(strings.TrimRight(u, ".,;:"))
	} else if m := codeLinePattern.FindStringSubmatch(line); m != nil && f.Code == "" {
		f.Code = m[1]
		changed = true
	}
	if m := expiresPattern.FindStringSubmatch(line); m != nil {
		n, _ := strconv.Atoi(m[1])
		d := time.Duration(n) * time.Second
		if strings.HasPrefix(strings.ToLower(m[2]), "m") {
			d = time.Duration(n) * time.Minute
		}
		if d > 0 && d != f.ExpiresIn {
			f.ExpiresIn = d
			changed = true
		}
	}
	return changed
}

// First code wins, a link carrying a different code is ignored
func (f *DeviceFlow) offerURL(u string) bool {
	if !signInURLPattern.MatchString(u) {
		return false
	}
	code := ""
	if m := userCodeParamPattern.FindStringSubmatch(u); m != nil {
		code = strings.ToUpper(m[1])
	}
	if code != "" && f.Code != "" && !strings.EqualFold(code, f.Code) {
		return false
	}
	changed := false
	if f.URL == "" || (code != "" && !userCodeParamPattern.MatchString(f.URL)) {
		if u != f.URL {
			f.URL = u
			changed = true
		}
	}
	if code != "" && f.Code == "" {
		f.Code = code
		changed = true
	}
	return changed
}

// Folds one anchored server device flow message, see ParseServerLine
func (f *DeviceFlow) FeedServer(kind LineKind, value string, ttl time.Duration) bool {
	switch kind {
	case LineDeviceURL:
		return f.offerURL(value)
	case LineDeviceCode:
		if f.Code == "" {
			f.Code = value
			return true
		}
		// A second, different code never replaces the first
	case LineDeviceWaiting:
		if ttl > 0 && ttl != f.ExpiresIn {
			f.ExpiresIn = ttl
			return true
		}
	}
	return false
}

// Expiry to show, the documented default when none was printed
func (f *DeviceFlow) TTL() time.Duration {
	if f.ExpiresIn > 0 {
		return f.ExpiresIn
	}
	return DefaultSignInTTL
}

// What one server console line means to the panel
type LineKind int

const (
	LineOther LineKind = iota
	LineBooted
	LineUnauthenticated
	LineAuthSuccess
	LineAuthFailed
	LinePersistenceEncrypted
	LinePlayerJoin
	LinePlayerLeave
	LineDeviceHeader
	LineDeviceURL
	LineDeviceCode
	LineDeviceWaiting
)

// A classified server line, value carries the player, link or code
type ServerLine struct {
	Kind  LineKind
	Value string
	TTL   time.Duration
}

// Classifies a server console line. Only text that starts right after the
// server's own log frame counts, chat and plugin text echoing it does not.
func ParseServerLine(line string) ServerLine {
	msg, ok := ServerMessage(line)
	if !ok || msg == "" {
		return ServerLine{}
	}
	switch {
	case deviceHeaderMsg.MatchString(msg):
		return ServerLine{Kind: LineDeviceHeader}
	case authSuccessMsg.MatchString(msg):
		return ServerLine{Kind: LineAuthSuccess}
	case unauthenticatedMsg.MatchString(msg):
		return ServerLine{Kind: LineUnauthenticated}
	case authFailedMsg.MatchString(msg):
		return ServerLine{Kind: LineAuthFailed}
	case persistenceMsg.MatchString(msg):
		return ServerLine{Kind: LinePersistenceEncrypted}
	case bootedMsg.MatchString(msg):
		return ServerLine{Kind: LineBooted}
	}
	if m := visitMsg.FindStringSubmatch(msg); m != nil {
		if u := signInURLPattern.FindString(m[1]); u != "" && u == strings.TrimRight(m[1], ".,;:") {
			return ServerLine{Kind: LineDeviceURL, Value: u}
		}
		return ServerLine{}
	}
	if m := enterCodeMsg.FindStringSubmatch(msg); m != nil {
		return ServerLine{Kind: LineDeviceCode, Value: m[1]}
	}
	if m := waitingMsg.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return ServerLine{Kind: LineDeviceWaiting, TTL: time.Duration(n) * time.Second}
	}
	if m := playerLeaveMsg.FindStringSubmatch(msg); m != nil {
		return ServerLine{Kind: LinePlayerLeave, Value: m[1]}
	}
	if m := playerJoinMsg.FindStringSubmatch(msg); m != nil {
		return ServerLine{Kind: LinePlayerJoin, Value: m[1]}
	}
	return ServerLine{}
}

// Reports whether downloader output asks for a device sign-in
func WantsSignIn(line string) bool {
	return signInURLPattern.MatchString(line) || codeLinePattern.MatchString(line)
}

// Build ids look like 2026.01.17-4b0f30090
var versionPattern = regexp.MustCompile(`\b\d{4}\.\d{1,2}\.\d{1,2}(?:[-+.][0-9A-Za-z.-]+)?\b`)

// Other formats still need a number up front, "Unauthorized" is no version
var looseVersionPattern = regexp.MustCompile(`^v?\d+(?:[.-][0-9A-Za-z]+)+$`)

// Pulls the version from downloader -print-version output, empty when none
func ParseVersion(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if v := versionPattern.FindString(lines[i]); v != "" {
			return v
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if len(l) <= 64 && looseVersionPattern.MatchString(l) {
			return l
		}
		return ""
	}
	return ""
}

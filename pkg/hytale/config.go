package hytale

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// config.json keys the panel edits, everything else passes through untouched
const (
	KeyServerName    = "ServerName"
	KeyMOTD          = "MOTD"
	KeyPassword      = "Password"
	KeyMaxPlayers    = "MaxPlayers"
	KeyMaxViewRadius = "MaxViewRadius"
)

// Returned when a JSON file holds something other than an object
var ErrInvalidObject = errors.New("not a valid JSON object")

// A JSON object that keeps its key order and unknown values byte for byte
type Object struct {
	keys   []string
	values map[string]json.RawMessage
}

func NewObject() *Object {
	return &Object{values: map[string]json.RawMessage{}}
}

// Parses a top-level JSON object
func ParseObject(data []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	o := NewObject()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if _, dup := o.values[key]; !dup {
			o.keys = append(o.keys, key)
		}
		o.values[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON object")
	}
	return o, nil
}

// Ordered key list
func (o *Object) Keys() []string {
	return append([]string(nil), o.keys...)
}

// Raw value for a key
func (o *Object) Raw(key string) (json.RawMessage, bool) {
	v, ok := o.values[key]
	return v, ok
}

// Decodes a key into v, false when absent or of another type
func (o *Object) Get(key string, v any) bool {
	raw, ok := o.values[key]
	if !ok {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

// String value, empty when absent or not a string
func (o *Object) String(key string) string {
	var s string
	o.Get(key, &s)
	return s
}

// Integer value, 0 when absent or not a number
func (o *Object) Int(key string) int {
	var f float64
	if !o.Get(key, &f) {
		return 0
	}
	return int(f)
}

// Sets a key, new keys append at the end
func (o *Object) Set(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = raw
	return nil
}

// Copies every key of other over this object
func (o *Object) Merge(other *Object) {
	for _, k := range other.keys {
		if _, ok := o.values[k]; !ok {
			o.keys = append(o.keys, k)
		}
		o.values[k] = other.values[k]
	}
}

// Number of keys
func (o *Object) Len() int {
	return len(o.keys)
}

// Two space indented JSON in original key order
func (o *Object) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	if len(o.keys) == 0 {
		return []byte("{}\n"), nil
	}
	buf.WriteString("{\n")
	for i, k := range o.keys {
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.WriteString("  ")
		buf.Write(key)
		buf.WriteString(": ")
		if err := json.Indent(&buf, o.values[k], "  ", "  "); err != nil {
			return nil, fmt.Errorf("value of %s: %w", k, err)
		}
		if i < len(o.keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// Loads a JSON object file, nil without error when absent
func ReadObjectFile(path string) (*Object, error) {
	data, err := readRegular(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o, err := ParseObject(data)
	if err != nil {
		return nil, fmt.Errorf("%s is %w: %v", filepath.Base(path), ErrInvalidObject, err)
	}
	return o, nil
}

// Largest server JSON file the panel reads
const maxObjectFileBytes = 8 << 20

// Reads a regular file, refusing symlinks and oversized files
func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), ErrNotRegular)
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxObjectFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxObjectFileBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), maxObjectFileBytes)
	}
	return data, nil
}

// Writes a JSON object file atomically
func WriteObjectFile(path string, o *Object) error {
	data, err := o.Marshal()
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data, 0644)
}

// Editable config.json fields
type ServerConfig struct {
	ServerName    string
	MOTD          string
	Password      string
	MaxPlayers    int
	MaxViewRadius int
}

// Reads the editable fields from a parsed config.json
func ConfigFromObject(o *Object) ServerConfig {
	if o == nil {
		return ServerConfig{}
	}
	return ServerConfig{
		ServerName:    o.String(KeyServerName),
		MOTD:          o.String(KeyMOTD),
		Password:      o.String(KeyPassword),
		MaxPlayers:    o.Int(KeyMaxPlayers),
		MaxViewRadius: o.Int(KeyMaxViewRadius),
	}
}

// Limits the panel enforces before writing config.json
const (
	MaxPlayersLimit    = 10000
	MaxViewRadiusLimit = 64
	// Above this the server manual warns about memory use
	RecommendedViewRadius = 12
)

// Checks edited values before they reach config.json
func ValidateConfig(c ServerConfig) error {
	if c.MaxPlayers < 1 || c.MaxPlayers > MaxPlayersLimit {
		return fmt.Errorf("max players must be between 1 and %d", MaxPlayersLimit)
	}
	if c.MaxViewRadius < 1 || c.MaxViewRadius > MaxViewRadiusLimit {
		return fmt.Errorf("max view radius must be between 1 and %d chunks", MaxViewRadiusLimit)
	}
	if len(c.ServerName) > 128 || len(c.MOTD) > 512 || len(c.Password) > 128 {
		return errors.New("server name, MOTD or password is too long")
	}
	return nil
}

// Serializes pending edits per server, the rpc layer saves while the
// provisioner applies, a read-modify-write each
var (
	pendingMu    sync.Mutex
	pendingLocks = map[string]*sync.Mutex{}
)

func lockPending(privateDir string) func() {
	pendingMu.Lock()
	l, ok := pendingLocks[privateDir]
	if !ok {
		l = &sync.Mutex{}
		pendingLocks[privateDir] = l
	}
	pendingMu.Unlock()
	l.Lock()
	return l.Unlock
}

// Merges a config.json patch into the pending edits
func SavePending(privateDir string, patch *Object) error {
	defer lockPending(privateDir)()
	path := PendingPath(privateDir, ConfigFile)
	pending, err := ReadObjectFile(path)
	if err != nil {
		return err
	}
	if pending == nil {
		pending = NewObject()
	}
	pending.Merge(patch)
	if err := os.MkdirAll(privateDir, 0700); err != nil {
		return err
	}
	return WriteObjectFile(path, pending)
}

// Folds a whitelist change into the pending edits as additions and
// removals, so players the server added meanwhile survive the next start
func SavePendingWhitelist(privateDir string, enabled *bool, add, remove []string) error {
	defer lockPending(privateDir)()
	path := PendingPath(privateDir, WhitelistFile)
	pending, err := ReadObjectFile(path)
	if err != nil {
		return err
	}
	if pending == nil {
		pending = NewObject()
	}
	delta := whitelistDeltaFrom(pending)
	if enabled != nil {
		delta.Enabled = enabled
	}
	for _, e := range add {
		delta.Remove = withoutEntry(delta.Remove, e)
		if !hasEntry(delta.Add, e) {
			delta.Add = append(delta.Add, e)
		}
	}
	for _, e := range remove {
		delta.Add = withoutEntry(delta.Add, e)
		if !hasEntry(delta.Remove, e) {
			delta.Remove = append(delta.Remove, e)
		}
	}
	obj, err := delta.object()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(privateDir, 0700); err != nil {
		return err
	}
	return WriteObjectFile(path, obj)
}

// Pending config.json edits, nil when none
func ReadPending(privateDir string) (*Object, error) {
	return ReadObjectFile(PendingPath(privateDir, ConfigFile))
}

// Pending whitelist change, nil when none
func ReadPendingWhitelist(privateDir string) (*WhitelistDelta, error) {
	obj, err := ReadObjectFile(PendingPath(privateDir, WhitelistFile))
	if err != nil || obj == nil {
		return nil, err
	}
	d := whitelistDeltaFrom(obj)
	return &d, nil
}

// Reports whether any edit waits for the next start
func HasPending(privateDir string) bool {
	for _, target := range []string{ConfigFile, WhitelistFile} {
		if _, err := os.Lstat(PendingPath(privateDir, target)); err == nil {
			return true
		}
	}
	return false
}

// Applies pending edits to the server files, each file waits until the
// server wrote its own copy. Holds the per-server lock for the whole
// read, write and remove, so an edit saved meanwhile is never dropped.
func ApplyPending(dataDir, privateDir string) (applied []string, err error) {
	defer lockPending(privateDir)()
	for _, target := range []string{ConfigFile, WhitelistFile} {
		ok, aerr := applyPendingFile(dataDir, privateDir, target)
		if aerr != nil {
			err = errors.Join(err, fmt.Errorf("%s: %w", target, aerr))
		}
		if ok {
			applied = append(applied, target)
		}
	}
	return applied, err
}

func applyPendingFile(dataDir, privateDir, target string) (bool, error) {
	pendingPath := PendingPath(privateDir, target)
	pending, err := ReadObjectFile(pendingPath)
	if err != nil || pending == nil {
		return false, err
	}
	targetPath := filepath.Join(dataDir, target)
	current, err := ReadObjectFile(targetPath)
	if err != nil {
		return false, err
	}
	// The server writes complete files on first boot, a partial one
	// written by the panel could miss keys the server requires
	if current == nil {
		return false, nil
	}
	if target == WhitelistFile {
		w, err := ReadWhitelist(dataDir)
		if err != nil {
			return false, err
		}
		if w.Format != v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_RECOGNIZED {
			return false, errors.New("whitelist.json is in a format the panel does not edit, change it in Files")
		}
		delta := whitelistDeltaFrom(pending)
		patch, err := WhitelistPatch(delta.Enabled, delta.Apply(w.Entries))
		if err != nil {
			return false, err
		}
		pending = patch
	}
	current.Merge(pending)
	if err := WriteObjectFile(targetPath, current); err != nil {
		return false, err
	}
	return true, os.Remove(pendingPath)
}

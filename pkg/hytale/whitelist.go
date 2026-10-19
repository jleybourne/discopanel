package hytale

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// whitelist.json keys of the shape the panel knows how to edit
const (
	whitelistEnabledKey = "enabled"
	whitelistListKey    = "list"
)

// whitelist.json as read from disk
type Whitelist struct {
	Format  v1.HytaleWhitelistFormat
	Enabled bool
	Entries []string
	obj     *Object
}

// Reads whitelist.json without assuming a shape the server never wrote
func ReadWhitelist(dataDir string) (*Whitelist, error) {
	obj, err := ReadObjectFile(filepath.Join(dataDir, WhitelistFile))
	if err != nil {
		if errors.Is(err, ErrInvalidObject) {
			return &Whitelist{Format: v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_UNRECOGNIZED}, nil
		}
		return nil, err
	}
	if obj == nil {
		return &Whitelist{Format: v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_MISSING}, nil
	}
	w := &Whitelist{Format: v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_UNRECOGNIZED, obj: obj}
	rawList, ok := obj.Raw(whitelistListKey)
	if !ok {
		return w, nil
	}
	var entries []string
	if err := json.Unmarshal(rawList, &entries); err != nil {
		return w, nil
	}
	if rawEnabled, ok := obj.Raw(whitelistEnabledKey); ok {
		if err := json.Unmarshal(rawEnabled, &w.Enabled); err != nil {
			return w, nil
		}
	}
	w.Entries = entries
	if w.Entries == nil {
		w.Entries = []string{}
	}
	w.Format = v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_RECOGNIZED
	return w, nil
}

// Cleans user entered whitelist rows, dropping blanks and duplicates
func NormalizeWhitelist(entries []string) ([]string, error) {
	out := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if len(e) > 64 || strings.ContainsAny(e, "\"\\\n\r\t") {
			return nil, fmt.Errorf("invalid whitelist entry %q", e)
		}
		key := strings.ToLower(e)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out, nil
}

// Builds a whitelist.json patch in the recognized shape
func WhitelistPatch(enabled *bool, entries []string) (*Object, error) {
	patch := NewObject()
	if enabled != nil {
		if err := patch.Set(whitelistEnabledKey, *enabled); err != nil {
			return nil, err
		}
	}
	if entries != nil {
		if err := patch.Set(whitelistListKey, entries); err != nil {
			return nil, err
		}
	}
	return patch, nil
}

// Pending whitelist keys, a toggle plus entries to add and remove
const (
	deltaAddKey    = "add"
	deltaRemoveKey = "remove"
)

// A whitelist change relative to whatever the file holds when applied
type WhitelistDelta struct {
	Enabled *bool
	Add     []string
	Remove  []string
}

func whitelistDeltaFrom(o *Object) WhitelistDelta {
	var d WhitelistDelta
	var on bool
	if o.Get(whitelistEnabledKey, &on) {
		d.Enabled = &on
	}
	o.Get(deltaAddKey, &d.Add)
	o.Get(deltaRemoveKey, &d.Remove)
	return d
}

func (d WhitelistDelta) object() (*Object, error) {
	o := NewObject()
	if d.Enabled != nil {
		if err := o.Set(whitelistEnabledKey, *d.Enabled); err != nil {
			return nil, err
		}
	}
	if err := o.Set(deltaAddKey, nonNil(d.Add)); err != nil {
		return nil, err
	}
	if err := o.Set(deltaRemoveKey, nonNil(d.Remove)); err != nil {
		return nil, err
	}
	return o, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func hasEntry(list []string, e string) bool {
	for _, x := range list {
		if strings.EqualFold(x, e) {
			return true
		}
	}
	return false
}

func withoutEntry(list []string, e string) []string {
	out := list[:0:0]
	for _, x := range list {
		if !strings.EqualFold(x, e) {
			out = append(out, x)
		}
	}
	return out
}

// Applies the change on top of the current entries
func (d WhitelistDelta) Apply(entries []string) []string {
	out := make([]string, 0, len(entries)+len(d.Add))
	for _, e := range entries {
		if !hasEntry(d.Remove, e) && !hasEntry(out, e) {
			out = append(out, e)
		}
	}
	for _, e := range d.Add {
		if !hasEntry(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// Entries to add and remove to get from old to new, case-insensitive
func DiffWhitelist(old, next []string) (add, remove []string) {
	for _, e := range next {
		if !hasEntry(old, e) {
			add = append(add, e)
		}
	}
	for _, e := range old {
		if !hasEntry(next, e) {
			remove = append(remove, e)
		}
	}
	return add, remove
}

// Overlays a pending change on the file view
func (w *Whitelist) Overlay(d *WhitelistDelta) {
	if d == nil {
		return
	}
	if d.Enabled != nil {
		w.Enabled = *d.Enabled
	}
	w.Entries = d.Apply(w.Entries)
}

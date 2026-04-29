package server

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	stdsync "sync"
)

// validNameRE matches collection names containing only alphanumeric chars, hyphens, and underscores.
var validNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// CollectionRegistry maps collection names to .anki2 file paths.
// It also provides per-collection mutexes for concurrent sync isolation.
type CollectionRegistry struct {
	collections map[string]string
	locks       map[string]*stdsync.Mutex
	mu          stdsync.Mutex // protects the locks map
}

// NewCollectionRegistry creates a CollectionRegistry from a name→path map.
// All paths are validated to exist and be non-empty at construction time.
func NewCollectionRegistry(collections map[string]string) (*CollectionRegistry, error) {
	if len(collections) == 0 {
		return nil, fmt.Errorf("collections map cannot be empty")
	}
	for name, path := range collections {
		if name == "" {
			return nil, fmt.Errorf("collection name cannot be empty")
		}
		if !validNameRE.MatchString(name) {
			return nil, fmt.Errorf("collection name %q contains invalid characters (allowed: a-z, A-Z, 0-9, hyphen, underscore)", name)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("collection %q: %w", name, err)
		}
		if info.Size() == 0 {
			return nil, fmt.Errorf("collection %q: file is empty", name)
		}
	}
	cp := make(map[string]string, len(collections))
	for k, v := range collections {
		cp[k] = v
	}
	return &CollectionRegistry{
		collections: cp,
		locks:       make(map[string]*stdsync.Mutex),
	}, nil
}

// ParseCollections parses "name:path,name:path,..." into a name→path map.
// Paths may contain colons (only the first colon per entry is the delimiter).
func ParseCollections(s string) (map[string]string, error) {
	result := make(map[string]string)
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idx := strings.IndexByte(entry, ':')
		if idx < 0 {
			return nil, fmt.Errorf("invalid collection entry %q: expected name:path format", entry)
		}
		name := strings.TrimSpace(entry[:idx])
		path := strings.TrimSpace(entry[idx+1:])
		if name == "" || path == "" {
			return nil, fmt.Errorf("invalid collection entry %q: name and path cannot be empty", entry)
		}
		result[name] = path
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no collections specified")
	}
	return result, nil
}

// Resolve returns the file path for the given collection name.
// Returns an error if the name is not registered.
func (r *CollectionRegistry) Resolve(name string) (string, error) {
	path, ok := r.collections[name]
	if !ok {
		return "", fmt.Errorf("collection %q not found", name)
	}
	return path, nil
}

// Names returns the sorted list of registered collection names.
func (r *CollectionRegistry) Names() []string {
	names := make([]string, 0, len(r.collections))
	for name := range r.collections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LockCollection acquires the per-collection mutex for the given collection name.
// In multi-collection mode, this allows concurrent sync operations on different
// collections while serializing sync operations on the same collection.
// The mutex is lazily created on first use.
func (r *CollectionRegistry) LockCollection(name string) {
	r.mu.Lock()
	mtx, ok := r.locks[name]
	if !ok {
		mtx = &stdsync.Mutex{}
		r.locks[name] = mtx
	}
	r.mu.Unlock()
	mtx.Lock()
}

// UnlockCollection releases the per-collection mutex for the given collection name.
func (r *CollectionRegistry) UnlockCollection(name string) {
	r.mu.Lock()
	mtx, ok := r.locks[name]
	r.mu.Unlock()
	if ok {
		mtx.Unlock()
	}
}

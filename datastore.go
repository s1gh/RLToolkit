package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// pluginNamePattern restricts plugin identifiers to a safe character set.
// Anything outside this is rejected before touching the filesystem — we
// don't try to "sanitize" arbitrary input into a path.
var pluginNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// DataStore persists per-plugin key/value JSON to disk, one file per plugin.
// Each plugin gets its own mutex so an unrelated plugin's slow Write doesn't
// stall others. An in-memory cache eliminates the read amplification of
// loading the JSON file on every Get.
type DataStore struct {
	dir string

	mu     sync.Mutex // guards the shards map itself
	shards map[string]*pluginShard
}

type pluginShard struct {
	mu     sync.RWMutex
	loaded bool
	data   map[string]json.RawMessage
}

func NewDataStore(dir string) (*DataStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir %q: %w", dir, err)
	}
	return &DataStore{
		dir:    dir,
		shards: make(map[string]*pluginShard),
	}, nil
}

func (s *DataStore) safePath(plugin string) (string, error) {
	if !pluginNamePattern.MatchString(plugin) {
		return "", fmt.Errorf("invalid plugin name %q", plugin)
	}
	return filepath.Join(s.dir, plugin+".json"), nil
}

// shard returns the (lazily created) shard for plugin. The shards map lock
// is only held to look up / create the shard pointer; per-plugin work then
// uses the shard's own RWMutex.
func (s *DataStore) shard(plugin string) *pluginShard {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.shards[plugin]; ok {
		return sh
	}
	sh := &pluginShard{}
	s.shards[plugin] = sh
	return sh
}

// loadInto populates sh.data from disk if it hasn't been loaded yet. Caller
// must hold sh.mu (write).
func (s *DataStore) loadInto(plugin string, sh *pluginShard) error {
	if sh.loaded {
		return nil
	}
	path, err := s.safePath(plugin)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			sh.data = make(map[string]json.RawMessage)
			sh.loaded = true
			return nil
		}
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if m == nil {
		m = make(map[string]json.RawMessage)
	}
	sh.data = m
	sh.loaded = true
	return nil
}

// flushLocked persists sh.data to disk via write-to-temp + rename so a
// crash mid-write never corrupts the existing file. Caller must hold
// sh.mu (write).
func (s *DataStore) flushLocked(plugin string, sh *pluginShard) error {
	path, err := s.safePath(plugin)
	if err != nil {
		return err
	}
	data, err := json.Marshal(sh.data)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *DataStore) LoadAll(plugin string) (map[string]json.RawMessage, error) {
	sh := s.shard(plugin)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if err := s.loadInto(plugin, sh); err != nil {
		return nil, err
	}
	// Return a shallow copy so callers can't mutate the cache.
	out := make(map[string]json.RawMessage, len(sh.data))
	for k, v := range sh.data {
		out[k] = v
	}
	return out, nil
}

func (s *DataStore) Get(plugin, key string) (json.RawMessage, bool, error) {
	sh := s.shard(plugin)
	sh.mu.RLock()
	if sh.loaded {
		val, ok := sh.data[key]
		sh.mu.RUnlock()
		return val, ok, nil
	}
	sh.mu.RUnlock()

	sh.mu.Lock()
	defer sh.mu.Unlock()
	if err := s.loadInto(plugin, sh); err != nil {
		return nil, false, err
	}
	val, ok := sh.data[key]
	return val, ok, nil
}

func (s *DataStore) Set(plugin, key string, value json.RawMessage) error {
	sh := s.shard(plugin)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if err := s.loadInto(plugin, sh); err != nil {
		return err
	}
	sh.data[key] = value
	if err := s.flushLocked(plugin, sh); err != nil {
		// Roll back the in-memory change so cache and disk stay in sync.
		delete(sh.data, key)
		log.Printf("[data] write failed for %s/%s: %v", plugin, key, err)
		return err
	}
	return nil
}

func (s *DataStore) Delete(plugin, key string) error {
	sh := s.shard(plugin)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if err := s.loadInto(plugin, sh); err != nil {
		return err
	}
	if _, ok := sh.data[key]; !ok {
		return nil
	}
	prev := sh.data[key]
	delete(sh.data, key)
	if err := s.flushLocked(plugin, sh); err != nil {
		sh.data[key] = prev
		log.Printf("[data] write failed for %s/%s: %v", plugin, key, err)
		return err
	}
	return nil
}

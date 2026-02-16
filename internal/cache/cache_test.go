package cache

import (
	"testing"
)

func TestCacheKey_Deterministic(t *testing.T) {
	c := &Cache{}

	model := "gpt-4"
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)

	key1 := c.cacheKey(model, body)
	key2 := c.cacheKey(model, body)

	if key1 != key2 {
		t.Errorf("cache key not deterministic: %s != %s", key1, key2)
	}

	if key1 == "" {
		t.Error("cache key is empty")
	}
}

func TestCacheKey_DifferentInputs(t *testing.T) {
	c := &Cache{}

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)

	key1 := c.cacheKey("gpt-4", body)
	key2 := c.cacheKey("gpt-3.5", body)

	if key1 == key2 {
		t.Error("different models should produce different cache keys")
	}

	key3 := c.cacheKey("gpt-4", []byte(`{"messages":[{"role":"user","content":"world"}]}`))
	if key1 == key3 {
		t.Error("different bodies should produce different cache keys")
	}
}

func TestCacheKey_HasPrefix(t *testing.T) {
	c := &Cache{}
	key := c.cacheKey("gpt-4", []byte("test"))

	if len(key) < 7 || key[:7] != "llm-gw:" {
		t.Errorf("cache key should start with 'llm-gw:', got: %s", key)
	}
}

func TestParseInfoInt(t *testing.T) {
	info := "keyspace_hits:42\nkeyspace_misses:10\n"

	hits := parseInfoInt(info, "keyspace_hits")
	if hits != 42 {
		t.Errorf("expected 42, got %d", hits)
	}

	misses := parseInfoInt(info, "keyspace_misses")
	if misses != 10 {
		t.Errorf("expected 10, got %d", misses)
	}

	unknown := parseInfoInt(info, "nonexistent")
	if unknown != 0 {
		t.Errorf("expected 0 for unknown key, got %d", unknown)
	}
}

func TestParseInfoString(t *testing.T) {
	info := "used_memory_human:1.5M\r\nother:value\r\n"

	mem := parseInfoString(info, "used_memory_human")
	if mem != "1.5M" {
		t.Errorf("expected '1.5M', got '%s'", mem)
	}

	unknown := parseInfoString(info, "nonexistent")
	if unknown != "" {
		t.Errorf("expected empty for unknown key, got '%s'", unknown)
	}
}

func TestParseKeyspaceKeys(t *testing.T) {
	info := "# Keyspace\ndb0:keys=123,expires=45,avg_ttl=6789\n"

	keys := parseKeyspaceKeys(info)
	if keys != 123 {
		t.Errorf("expected 123, got %d", keys)
	}

	empty := parseKeyspaceKeys("# Keyspace\n")
	if empty != 0 {
		t.Errorf("expected 0 for empty keyspace, got %d", empty)
	}
}

func TestSplitLines(t *testing.T) {
	lines := splitLines("a\nb\nc")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "a" || lines[1] != "b" || lines[2] != "c" {
		t.Errorf("unexpected lines: %v", lines)
	}

	single := splitLines("hello")
	if len(single) != 1 || single[0] != "hello" {
		t.Errorf("single line: %v", single)
	}
}

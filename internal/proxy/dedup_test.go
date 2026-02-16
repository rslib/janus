package proxy

import (
	"sync"
	"testing"
	"time"
)

func TestDedupKey(t *testing.T) {
	k1 := DedupKey("gpt-4", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	k2 := DedupKey("gpt-4", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	k3 := DedupKey("gpt-4", []byte(`{"messages":[{"role":"user","content":"hello"}]}`))

	if k1 != k2 {
		t.Error("identical requests should produce the same key")
	}
	if k1 == k3 {
		t.Error("different requests should produce different keys")
	}
}

func TestDeduplicator_LeaderFollower(t *testing.T) {
	d := NewDeduplicator(5 * time.Second)
	defer d.Close()

	key := DedupKey("gpt-4", []byte(`test`))

	// First call is leader
	f1, isLeader := d.TryJoin(key)
	if !isLeader {
		t.Fatal("first caller should be leader")
	}

	// Second call with same key is follower
	f2, isLeader := d.TryJoin(key)
	if isLeader {
		t.Fatal("second caller should be follower")
	}
	if f1 != f2 {
		t.Fatal("follower should get same inflight entry")
	}

	// Complete the request
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-f2.done
		if string(f2.result) != "response" {
			t.Error("follower should receive leader's result")
		}
		if f2.statusCode != 200 {
			t.Error("follower should receive leader's status code")
		}
	}()

	d.Complete(key, []byte("response"), 200)
	wg.Wait()
}

func TestDeduplicator_DifferentKeys(t *testing.T) {
	d := NewDeduplicator(5 * time.Second)
	defer d.Close()

	_, isLeader1 := d.TryJoin("key1")
	_, isLeader2 := d.TryJoin("key2")

	if !isLeader1 || !isLeader2 {
		t.Error("different keys should both be leaders")
	}

	d.Complete("key1", []byte("r1"), 200)
	d.Complete("key2", []byte("r2"), 200)
}

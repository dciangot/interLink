package virtualkubelet

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodStore_BasicOperations(t *testing.T) {
	store := newPodStore()

	_, ok := store.get("uid-1")
	assert.False(t, ok)
	assert.Empty(t, store.list())

	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	store.set("uid-1", pod)

	got, ok := store.get("uid-1")
	assert.True(t, ok)
	assert.Same(t, pod, got)
	assert.Len(t, store.list(), 1)

	store.delete("uid-1")
	_, ok = store.get("uid-1")
	assert.False(t, ok)

	// Deleting a missing key must not panic.
	store.delete("uid-1")
}

func TestPodStore_ListReturnsSnapshot(t *testing.T) {
	store := newPodStore()
	store.set("uid-1", &v1.Pod{})

	snapshot := store.list()
	store.set("uid-2", &v1.Pod{})

	assert.Len(t, snapshot, 1)
	assert.Len(t, store.list(), 2)
}

// Meaningful when run with -race.
func TestPodStore_ConcurrentAccess(t *testing.T) {
	store := newPodStore()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uid := fmt.Sprintf("uid-%d", i)
			store.set(uid, &v1.Pod{})
			store.get(uid)
			store.list()
			if i%2 == 0 {
				store.delete(uid)
			}
		}(i)
	}
	wg.Wait()

	assert.Len(t, store.list(), 25)
}

func TestPodIPTracker_AddDeduplicates(t *testing.T) {
	tracker := newPodIPTracker()
	tracker.add("10.0.0.2")
	tracker.add("10.0.0.2")
	tracker.add("10.0.0.3")

	assert.Equal(t, []string{"10.0.0.2", "10.0.0.3"}, tracker.ips)
}

func TestPodIPTracker_ReplaceCopiesInput(t *testing.T) {
	tracker := newPodIPTracker()
	input := []string{"10.0.0.2"}
	tracker.replace(input)
	input[0] = "changed"

	assert.Equal(t, []string{"10.0.0.2"}, tracker.ips)
}

func TestPodIPTracker_AllocateMarksIPAsUsed(t *testing.T) {
	tracker := newPodIPTracker()
	pool := []string{"10.0.0.2", "10.0.0.3"}
	pick := func(used []string) string { return findFirstFreeIP(pool, used, 2, 250) }

	assert.Equal(t, "10.0.0.2", tracker.allocate(pick))
	assert.Equal(t, "10.0.0.3", tracker.allocate(pick))
	assert.Equal(t, "", tracker.allocate(pick), "pool should be exhausted")
}

// Concurrent allocators must never receive the same address. Meaningful when run with -race.
func TestPodIPTracker_ConcurrentAllocateIsUnique(t *testing.T) {
	const n = 100
	pool := make([]string, 0, n)
	for i := 2; i < n+2; i++ {
		pool = append(pool, fmt.Sprintf("10.0.0.%d", i))
	}

	tracker := newPodIPTracker()
	results := make(chan string, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- tracker.allocate(func(used []string) string {
				return findFirstFreeIP(pool, used, 2, 250)
			})
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[string]bool, n)
	for ip := range results {
		assert.NotEmpty(t, ip)
		assert.False(t, seen[ip], "IP %s allocated twice", ip)
		seen[ip] = true
	}
	assert.Len(t, seen, n)
}

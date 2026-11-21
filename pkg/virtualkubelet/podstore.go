package virtualkubelet

import (
	"sync"

	v1 "k8s.io/api/core/v1"
)

// podStore is a concurrency-safe registry of the pods handled by the provider, keyed by pod UID.
//
// It only protects the map itself: the *v1.Pod values are shared with the callers,
// so mutating a pod that is already stored still requires external coordination.
type podStore struct {
	mu   sync.RWMutex
	pods map[string]*v1.Pod
}

func newPodStore() *podStore {
	return &podStore{pods: make(map[string]*v1.Pod)}
}

// get returns the pod stored under the given UID, if any.
func (s *podStore) get(uid string) (*v1.Pod, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pod, ok := s.pods[uid]
	return pod, ok
}

// set stores the pod under the given UID, replacing any previous entry.
func (s *podStore) set(uid string, pod *v1.Pod) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pods[uid] = pod
}

// delete removes the pod stored under the given UID.
func (s *podStore) delete(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.pods, uid)
}

// list returns a snapshot of the stored pods. The returned slice is owned by the caller
// and can be iterated while other goroutines add or remove pods.
func (s *podStore) list() []*v1.Pod {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pods := make([]*v1.Pod, 0, len(s.pods))
	for _, pod := range s.pods {
		pods = append(pods, pod)
	}
	return pods
}

// podIPTracker keeps track of the IP addresses currently assigned to pods attached to the VPN.
type podIPTracker struct {
	mu  sync.Mutex
	ips []string
}

func newPodIPTracker() *podIPTracker {
	return &podIPTracker{}
}

// add records an IP as in use. Adding an IP that is already tracked is a no-op.
func (t *podIPTracker) add(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.addLocked(ip)
}

func (t *podIPTracker) addLocked(ip string) {
	for _, used := range t.ips {
		if used == ip {
			return
		}
	}
	t.ips = append(t.ips, ip)
}

// replace overwrites the tracked IPs with the given list.
func (t *podIPTracker) replace(ips []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ips = append([]string(nil), ips...)
}

// allocate picks a free IP and marks it as in use in a single critical section, so that
// concurrent callers can never be given the same address. pick receives a copy of the IPs
// currently in use and returns the chosen IP, or an empty string if none is available.
func (t *podIPTracker) allocate(pick func(used []string) string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	ip := pick(append([]string(nil), t.ips...))
	if ip != "" {
		t.addLocked(ip)
	}
	return ip
}

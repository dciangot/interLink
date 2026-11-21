package virtualkubelet

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	types "github.com/interlink-hq/interlink/pkg/interlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestGetLogs_UsesInterlinkClient(t *testing.T) {
	kubeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/namespaces/ns-a/pods/pod-a", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(testPod()))
	}))
	defer kubeAPI.Close()

	clientSet, err := kubernetes.NewForConfig(&rest.Config{Host: kubeAPI.URL})
	require.NoError(t, err)

	interlinkAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/getLogs", r.URL.Path)
		assert.Contains(t, r.Header.Get("InterLink-Http-Session"), "GetLogs#")

		var got types.LogStruct
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		assert.Equal(t, "uid-a", got.PodUID)
		assert.Equal(t, "pod-a", got.PodName)
		assert.Equal(t, "ns-a", got.Namespace)
		assert.Equal(t, "main", got.ContainerName)

		writeBody(t, w, "hello from the job\n")
	}))
	defer interlinkAPI.Close()

	p := &Provider{clientSet: clientSet, interlink: newTestInterlinkClient(interlinkAPI)}

	rc, err := p.GetLogs(context.Background(), "ns-a", "pod-a", "main", api.ContainerLogOpts{})
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "hello from the job\n", string(data))
}

func TestGetLogs_UnknownPodDoesNotCallInterlink(t *testing.T) {
	kubeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer kubeAPI.Close()

	clientSet, err := kubernetes.NewForConfig(&rest.Config{Host: kubeAPI.URL})
	require.NoError(t, err)

	interlinkAPI := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("interLink must not be called for a pod unknown to the cluster")
	}))
	defer interlinkAPI.Close()

	p := &Provider{clientSet: clientSet, interlink: newTestInterlinkClient(interlinkAPI)}

	_, err = p.GetLogs(context.Background(), "ns-a", "pod-a", "main", api.ContainerLogOpts{})
	assert.Error(t, err)
}

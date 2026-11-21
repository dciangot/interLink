package virtualkubelet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
)

func TestCheckPodsStatus_UsesInterlinkClient(t *testing.T) {
	t.Run("asks interLink for the status of the pod", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/status", r.URL.Path)

			var pods []v1.Pod
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&pods))
			if assert.Len(t, pods, 1) {
				assert.Equal(t, "pod-a", pods[0].Name)
			}

			writeBody(t, w, "[]")
		}))
		defer srv.Close()

		p := &Provider{interlink: newTestInterlinkClient(srv)}
		status, err := checkPodsStatus(context.Background(), p, testPod())
		require.NoError(t, err)
		assert.Empty(t, status)
	})

	t.Run("skips a status without a pod name", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeBody(t, w, `[{"UID":"uid-a"}]`)
		}))
		defer srv.Close()

		p := &Provider{interlink: newTestInterlinkClient(srv)}
		status, err := checkPodsStatus(context.Background(), p, testPod())
		require.NoError(t, err)
		assert.Empty(t, status)
	})

	t.Run("reports an error answered by interLink", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			writeBody(t, w, "plugin down")
		}))
		defer srv.Close()

		p := &Provider{interlink: newTestInterlinkClient(srv)}
		var statusErr *InterlinkStatusError
		_, err := checkPodsStatus(context.Background(), p, testPod())
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusBadGateway, statusErr.StatusCode)
		assert.Equal(t, "plugin down", statusErr.Body)
	})

	t.Run("a malformed answer is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeBody(t, w, "not json")
		}))
		defer srv.Close()

		p := &Provider{interlink: newTestInterlinkClient(srv)}
		_, err := checkPodsStatus(context.Background(), p, testPod())
		assert.Error(t, err)
	})
}

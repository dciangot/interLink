package virtualkubelet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteExecution_DeleteUsesInterlinkClient(t *testing.T) {
	t.Run("sends the delete request", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			assert.Equal(t, http.MethodDelete, r.Method)
			assert.Equal(t, "/delete", r.URL.Path)
			writeBody(t, w, "deleted")
		}))
		defer srv.Close()

		p := &Provider{interlink: newTestInterlinkClient(srv)}
		require.NoError(t, RemoteExecution(context.Background(), Config{}, p, testPod(), DELETE))
		assert.Equal(t, int32(1), calls.Load())
	})

	t.Run("does not call interLink for a pod that was never created", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			calls.Add(1)
		}))
		defer srv.Close()

		pod := testPod()
		pod.Status.Phase = PodPhaseInitialize

		p := &Provider{interlink: newTestInterlinkClient(srv)}
		require.NoError(t, RemoteExecution(context.Background(), Config{}, p, pod, DELETE))
		assert.Zero(t, calls.Load())
	})

	t.Run("reports an error answered by interLink", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		p := &Provider{interlink: newTestInterlinkClient(srv)}
		var statusErr *InterlinkStatusError
		err := RemoteExecution(context.Background(), Config{}, p, testPod(), DELETE)
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusInternalServerError, statusErr.StatusCode)
	})
}

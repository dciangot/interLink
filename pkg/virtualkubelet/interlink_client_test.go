package virtualkubelet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	types "github.com/interlink-hq/interlink/pkg/interlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// newTestInterlinkClient returns a client pointing at srv, bypassing the endpoint validation that rejects loopback hosts.
func newTestInterlinkClient(srv *httptest.Server) *InterlinkClient {
	return &InterlinkClient{
		endpoint:      srv.URL,
		httpClient:    srv.Client(),
		logHTTPClient: srv.Client(),
	}
}

func testPod() *v1.Pod {
	return &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "ns-a", UID: "uid-a"}}
}

func writeTokenFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// writeBody writes a test response body and fails the test if that is not possible.
func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	_, err := w.Write([]byte(body))
	assert.NoError(t, err)
}

func TestResolveInterlinkEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		port    string
		want    string
		wantErr bool
	}{
		{name: "http", url: "http://localhost", port: "3000", want: "http://localhost:3000"},
		{name: "https", url: "https://interlink-api.example.com", port: "8443", want: "https://interlink-api.example.com:8443"},
		{name: "http without port", url: "http://interlink-api.example.com", port: "", want: "http://interlink-api.example.com"},
		{name: "unix socket", url: "unix:///var/run/interlink.sock", port: "", want: "http://unix"},
		{name: "unsupported scheme", url: "ftp://example.com", port: "21", wantErr: true},
		{name: "empty", url: "", port: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveInterlinkEndpoint(tt.url, tt.port)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewInterlinkClient(t *testing.T) {
	ctx := context.Background()

	t.Run("accepts a remote http endpoint", func(t *testing.T) {
		client, err := NewInterlinkClient(ctx, Config{InterlinkURL: "http://interlink.example.com", InterlinkPort: "3000"}, nil)
		require.NoError(t, err)
		assert.Equal(t, "http://interlink.example.com:3000", client.endpoint)
		assert.Same(t, client.httpClient, client.logHTTPClient, "logs share the client when no transport is given")
	})

	t.Run("accepts a unix socket", func(t *testing.T) {
		client, err := NewInterlinkClient(ctx, Config{InterlinkURL: "unix:///var/run/interlink.sock"}, nil)
		require.NoError(t, err)
		assert.Equal(t, "http://unix", client.endpoint)
	})

	t.Run("keeps the token file and the job script builder", func(t *testing.T) {
		client, err := NewInterlinkClient(ctx, Config{
			InterlinkURL:        "http://interlink.example.com",
			VKTokenFile:         "/var/run/token",
			JobScriptBuilderURL: "http://builder.example.com",
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, "/var/run/token", client.tokenFile)
		assert.Equal(t, "http://builder.example.com", client.jobScriptBuilderURL)
	})

	t.Run("rejects an unsupported scheme", func(t *testing.T) {
		_, err := NewInterlinkClient(ctx, Config{InterlinkURL: "ftp://example.com"}, nil)
		assert.Error(t, err)
	})

	t.Run("accepts localhost", func(t *testing.T) {
		client, err := NewInterlinkClient(ctx, Config{InterlinkURL: "http://localhost", InterlinkPort: "3000"}, nil)
		require.NoError(t, err)
		assert.Equal(t, "http://localhost:3000", client.endpoint)
	})

	t.Run("reports TLS configuration errors", func(t *testing.T) {
		_, err := NewInterlinkClient(ctx, Config{
			InterlinkURL: "https://interlink.example.com",
			TLS:          TLSConfig{Enabled: true, CACertFile: "/non/existent/ca.crt"},
		}, nil)
		assert.ErrorContains(t, err, "TLS HTTP client")
	})

	t.Run("logs use a dedicated transport without keep-alives and the input is left untouched", func(t *testing.T) {
		transport := &http.Transport{}
		client, err := NewInterlinkClient(ctx, Config{InterlinkURL: "http://interlink.example.com"}, transport)
		require.NoError(t, err)

		logTransport, ok := client.logHTTPClient.Transport.(*http.Transport)
		require.True(t, ok)
		assert.True(t, logTransport.DisableKeepAlives)
		assert.NotSame(t, transport, logTransport)
		assert.False(t, transport.DisableKeepAlives)
	})
}

// testCall is a minimal call used to exercise the shared plumbing independently of any API operation.
func testCall() interlinkCall {
	return interlinkCall{
		op:       "test",
		spanName: "TestHttpCall",
		method:   http.MethodDelete,
		path:     "/test",
		session:  newSessionContext("Test"),
	}
}

func TestInterlinkClient_RequestShape(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/test", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer secret-token", r.Header.Get("Authorization"), "token must be trimmed")
		assert.True(t, strings.HasPrefix(r.Header.Get("InterLink-Http-Session"), "Test#"), r.Header.Get("InterLink-Http-Session"))

		var got map[string]string
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		assert.Equal(t, map[string]string{"name": "pod-a"}, got)

		writeBody(t, w, "ok")
	}))
	defer srv.Close()

	client := newTestInterlinkClient(srv)
	client.tokenFile = writeTokenFile(t, "secret-token\n")

	call := testCall()
	call.body = map[string]string{"name": "pod-a"}
	body, err := client.doJSON(context.Background(), call)

	require.NoError(t, err)
	assert.True(t, called.Load())
	assert.Equal(t, "ok", string(body), "the response body is returned")
}

func TestInterlinkClient_NoBodyWhenNotGiven(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Empty(t, body)
	}))
	defer srv.Close()

	_, err := newTestInterlinkClient(srv).doJSON(context.Background(), testCall())
	require.NoError(t, err)
}

func TestInterlinkClient_NoAuthorizationHeaderWithoutTokenFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := newTestInterlinkClient(srv).doJSON(context.Background(), testCall())
	require.NoError(t, err)
}

func TestInterlinkClient_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeBody(t, w, "plugin exploded")
	}))
	defer srv.Close()

	_, err := newTestInterlinkClient(srv).doJSON(context.Background(), testCall())
	require.Error(t, err)

	var statusErr *InterlinkStatusError
	require.True(t, errors.As(err, &statusErr))
	assert.Equal(t, http.StatusInternalServerError, statusErr.StatusCode)
	assert.Equal(t, "test", statusErr.Op)
	assert.Equal(t, "plugin exploded", statusErr.Body)
	assert.Contains(t, err.Error(), "plugin exploded")
	assert.Contains(t, err.Error(), "500")
}

func TestInterlinkClient_Ping(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/pinglink", r.URL.Path)
			assert.True(t, strings.HasPrefix(r.Header.Get("InterLink-Http-Session"), "PingInterLink#"))
			writeBody(t, w, "pong")
		}))
		defer srv.Close()

		code, body, err := newTestInterlinkClient(srv).Ping(context.Background())
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "pong", body)
	})

	t.Run("non-200 is reported through the status code", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			writeBody(t, w, "plugin down")
		}))
		defer srv.Close()

		code, body, err := newTestInterlinkClient(srv).Ping(context.Background())
		require.NoError(t, err)
		assert.Equal(t, http.StatusServiceUnavailable, code)
		assert.Equal(t, "plugin down", body)
	})

	t.Run("unreachable API is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		client := newTestInterlinkClient(srv)
		srv.Close()

		_, _, err := client.Ping(context.Background())
		assert.Error(t, err)
	})
}

func TestInterlinkClient_TokenHandling(t *testing.T) {
	t.Run("missing token file fails before any request is sent", func(t *testing.T) {
		var called atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called.Store(true)
		}))
		defer srv.Close()

		client := newTestInterlinkClient(srv)
		client.tokenFile = filepath.Join(t.TempDir(), "does-not-exist")

		_, _, err := client.Ping(context.Background())
		assert.ErrorContains(t, err, "token file")
		assert.False(t, called.Load())
	})

	t.Run("a rotated token is picked up", func(t *testing.T) {
		var (
			mu   sync.Mutex
			seen []string
		)
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, r.Header.Get("Authorization"))
		}))
		defer srv.Close()

		client := newTestInterlinkClient(srv)
		client.tokenFile = writeTokenFile(t, "first")

		_, _, err := client.Ping(context.Background())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(client.tokenFile, []byte("second"), 0o600))
		_, _, err = client.Ping(context.Background())
		require.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []string{"Bearer first", "Bearer second"}, seen)
	})

	t.Run("errors never contain the token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		client := newTestInterlinkClient(srv)
		srv.Close()
		client.tokenFile = writeTokenFile(t, "super-secret-token")

		_, _, err := client.Ping(context.Background())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "super-secret-token")
	})
}

func TestInterlinkClient_ContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := newTestInterlinkClient(srv).Ping(ctx)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), err)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestInterlinkClient_Operations(t *testing.T) {
	tests := []struct {
		name        string
		call        func(c *InterlinkClient) error
		method      string
		path        string
		sessionPref string
		checkBody   func(t *testing.T, body []byte)
	}{
		{
			name: "create",
			call: func(c *InterlinkClient) error {
				_, err := c.Create(context.Background(), types.PodCreateRequests{Pod: *testPod()})
				return err
			},
			method:      http.MethodPost,
			path:        "/create",
			sessionPref: "CreatePod#",
			checkBody: func(t *testing.T, body []byte) {
				var got types.PodCreateRequests
				require.NoError(t, json.Unmarshal(body, &got))
				assert.Equal(t, "pod-a", got.Pod.Name)
				assert.Equal(t, "http://builder.example.com", got.JobScriptBuilderURL)
			},
		},
		{
			name: "delete",
			call: func(c *InterlinkClient) error {
				_, err := c.Delete(context.Background(), testPod())
				return err
			},
			method:      http.MethodDelete,
			path:        "/delete",
			sessionPref: "DeletePod#",
			checkBody: func(t *testing.T, body []byte) {
				var got v1.Pod
				require.NoError(t, json.Unmarshal(body, &got))
				assert.Equal(t, "pod-a", got.Name)
			},
		},
		{
			name: "status",
			call: func(c *InterlinkClient) error {
				_, err := c.Status(context.Background(), []*v1.Pod{testPod()})
				return err
			},
			method:      http.MethodGet,
			path:        "/status",
			sessionPref: "GetStatus#",
			checkBody: func(t *testing.T, body []byte) {
				var got []v1.Pod
				require.NoError(t, json.Unmarshal(body, &got))
				require.Len(t, got, 1)
				assert.Equal(t, "pod-a", got[0].Name)
			},
		},
		{
			name: "update cache",
			call: func(c *InterlinkClient) error {
				return c.UpdateCache(context.Background(), testPod())
			},
			method:      http.MethodPost,
			path:        "/updateCache",
			sessionPref: "UpdateCache#",
			checkBody: func(t *testing.T, body []byte) {
				var got v1.Pod
				require.NoError(t, json.Unmarshal(body, &got))
				assert.Equal(t, "uid-a", string(got.UID))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called.Store(true)
				assert.Equal(t, tt.method, r.Method)
				assert.Equal(t, tt.path, r.URL.Path)
				assert.Equal(t, "Bearer secret-token", r.Header.Get("Authorization"))
				assert.True(t, strings.HasPrefix(r.Header.Get("InterLink-Http-Session"), tt.sessionPref), r.Header.Get("InterLink-Http-Session"))

				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				tt.checkBody(t, body)

				writeBody(t, w, "ok")
			}))
			defer srv.Close()

			client := newTestInterlinkClient(srv)
			client.tokenFile = writeTokenFile(t, "secret-token\n")
			client.jobScriptBuilderURL = "http://builder.example.com"

			require.NoError(t, tt.call(client))
			assert.True(t, called.Load())
		})
	}
}

func TestInterlinkClient_OperationsReturnResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, `{"PodUID":"uid-a","PodJID":"42"}`)
	}))
	defer srv.Close()

	body, err := newTestInterlinkClient(srv).Create(context.Background(), types.PodCreateRequests{Pod: *testPod()})
	require.NoError(t, err)
	assert.JSONEq(t, `{"PodUID":"uid-a","PodJID":"42"}`, string(body))
}

func TestInterlinkClient_OperationsReportStatusErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeBody(t, w, "plugin exploded")
	}))
	defer srv.Close()
	client := newTestInterlinkClient(srv)

	_, err := client.Delete(context.Background(), testPod())

	var statusErr *InterlinkStatusError
	require.True(t, errors.As(err, &statusErr))
	assert.Equal(t, "delete", statusErr.Op)
	assert.Equal(t, http.StatusInternalServerError, statusErr.StatusCode)

	assert.Error(t, client.UpdateCache(context.Background(), testPod()))
}

func TestInterlinkClient_Logs(t *testing.T) {
	logsRequest := types.LogStruct{PodName: "pod-a", Namespace: "ns-a", PodUID: "uid-a", ContainerName: "main"}

	t.Run("returns the body unread for the caller to consume", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/getLogs", r.URL.Path)
			assert.Equal(t, "GetLogs#7", r.Header.Get("InterLink-Http-Session"))

			var got types.LogStruct
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
			assert.Equal(t, "main", got.ContainerName)

			writeBody(t, w, "line 1\nline 2\n")
		}))
		defer srv.Close()

		rc, err := newTestInterlinkClient(srv).Logs(context.Background(), logsRequest, "GetLogs#7")
		require.NoError(t, err)
		defer rc.Close()

		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, "line 1\nline 2\n", string(data))
	})

	t.Run("non-200 returns a typed error and no body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			writeBody(t, w, "no such job")
		}))
		defer srv.Close()

		rc, err := newTestInterlinkClient(srv).Logs(context.Background(), logsRequest, "GetLogs#7")
		assert.Nil(t, rc)

		var statusErr *InterlinkStatusError
		require.True(t, errors.As(err, &statusErr))
		assert.Equal(t, http.StatusNotFound, statusErr.StatusCode)
		assert.Equal(t, "no such job", statusErr.Body)
	})

	t.Run("uses the dedicated log client", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()

		var used atomic.Bool
		client := newTestInterlinkClient(srv)
		client.logHTTPClient = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			used.Store(true)
			return srv.Client().Transport.RoundTrip(r)
		})}

		rc, err := client.Logs(context.Background(), logsRequest, "GetLogs#7")
		require.NoError(t, err)
		rc.Close()
		assert.True(t, used.Load())
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

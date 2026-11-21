package virtualkubelet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateTLSHTTPClient(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		tlsConfig TLSConfig
		expectTLS bool
		wantErr   bool
	}{
		{
			name: "TLS disabled",
			tlsConfig: TLSConfig{
				Enabled: false,
			},
			expectTLS: false,
			wantErr:   false,
		},
		{
			name: "TLS enabled without certs",
			tlsConfig: TLSConfig{
				Enabled: true,
			},
			expectTLS: true,
			wantErr:   false,
		},
		{
			name: "TLS enabled with non-existent CA cert",
			tlsConfig: TLSConfig{
				Enabled:    true,
				CACertFile: "/non/existent/ca.crt",
			},
			expectTLS: false,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := createTLSHTTPClient(ctx, tt.tlsConfig)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, client)

			if !tt.expectTLS {
				// Default client has no custom transport
				assert.Equal(t, http.DefaultClient, client)
			} else {
				// Custom client with TLS transport
				assert.NotEqual(t, http.DefaultClient, client)

				transport, ok := client.Transport.(*http.Transport)
				require.True(t, ok)
				assert.Equal(t, 120*time.Second, transport.ResponseHeaderTimeout)
				assert.Equal(t, 120*time.Second, transport.IdleConnTimeout)
			}
		})
	}
}

func TestAddSessionContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	sessionID := "test-session-123"

	AddSessionContext(req, sessionID)

	got := req.Header.Get("InterLink-Http-Session")
	assert.Equal(t, sessionID, got)
}

func TestGetSessionContextMessage(t *testing.T) {
	tests := []struct {
		name           string
		sessionContext string
		expected       string
	}{
		{
			name:           "normal session context",
			sessionContext: "CreatePod#12345",
			expected:       "HTTP InterLink session CreatePod#12345: ",
		},
		{
			name:           "empty session context",
			sessionContext: "",
			expected:       "HTTP InterLink session : ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetSessionContextMessage(tt.sessionContext)
			assert.Equal(t, tt.expected, got)
		})
	}
}

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port int    `yaml:"port"`
		Host string `yaml:"host"`
	} `yaml:"server"`
	OIDC struct {
		ClientID     string   `yaml:"client_id"`
		ClientSecret string   `yaml:"client_secret"`
		RedirectURL  string   `yaml:"redirect_url"`
		Issuer       string   `yaml:"issuer"`
		Scopes       []string `yaml:"scopes"`
	} `yaml:"oidc"`
	TestMode bool `yaml:"test_mode"`
}

type InterLinkConfig struct {
	VKName           string            `yaml:"kubelet_node_name"`
	Namespace        string            `yaml:"kubernetes_namespace"`
	InterLinkIP      string            `yaml:"interlink_ip"`
	InterLinkPort    int               `yaml:"interlink_port"`
	InterLinkVersion string            `yaml:"interlink_version"`
	VKLimits         Resources         `yaml:"node_limits"`
	OAUTH            OAuthStruct       `yaml:"oauth"`
	HTTPInsecure     bool              `yaml:"insecure_http"`
	CACert           string            `yaml:"ca_cert,omitempty"`
	DisableProjected bool              `yaml:"disable_projected_volumes"`
	NodeLabels       map[string]string `yaml:"node_labels,omitempty"`
	NodeTaints       []NodeTaint       `yaml:"node_taints,omitempty"`

	// mTLS configuration
	AuthMode         string             `yaml:"auth_mode,omitempty"` // "oauth" or authModeMTLS
	MTLSEnabled      bool               `yaml:"mtls_enabled"`
	MTLSCertificates *CertificateBundle `yaml:"-"` // Not serialized to YAML, used for generation

	// Tunneled deployment configuration
	DeploymentMode string          `yaml:"deployment_mode,omitempty"` // "edge-node" or "tunneled"
	SSHTunnel      SSHTunnelConfig `yaml:"ssh_tunnel,omitempty"`
}

type Resources struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
	Pods   string `yaml:"pods"`
}

type OAuthStruct struct {
	Provider      string   `yaml:"provider"`
	GrantType     string   `yaml:"grant_type"`
	Issuer        string   `yaml:"issuer,omitempty"`
	RefreshToken  string   `yaml:"refresh_token,omitempty"`
	Audience      string   `yaml:"audience,omitempty"`
	Group         string   `yaml:"group,omitempty"`
	GroupClaim    string   `yaml:"group_claim"`
	Scopes        []string `yaml:"scopes"`
	GitHubUser    string   `yaml:"github_user,omitempty"`
	TokenURL      string   `yaml:"token_url"`
	DeviceCodeURL string   `yaml:"device_code_url,omitempty"`
	ClientID      string   `yaml:"client_id"`
	ClientSecret  string   `yaml:"client_secret"`
}

type NodeTaint struct {
	Key    string `yaml:"key"`
	Value  string `yaml:"value"`
	Effect string `yaml:"effect"`
}

type SSHTunnelConfig struct {
	RemoteHost     string      `yaml:"remote_host"`
	RemotePort     int         `yaml:"remote_port"`
	RemoteUser     string      `yaml:"remote_user"`
	SSHPort        int         `yaml:"ssh_port"`
	PrivateKeyPath string      `yaml:"private_key_path"`
	HostKeyPath    string      `yaml:"host_key_path,omitempty"`
	LocalSocket    string      `yaml:"local_socket"`
	PluginPort     int         `yaml:"plugin_port"`
	SSHKeys        *SSHKeyPair `yaml:"-"` // Not serialized to YAML, used for generation
}

type SessionData struct {
	UserInfo   map[string]interface{}
	Tokens     *oauth2.Token
	ConfigData InterLinkConfig
}

type MonitorResult struct {
	URL        string    `json:"url"`
	Status     string    `json:"status"`
	StatusCode int       `json:"status_code"`
	Message    string    `json:"message"`
	Timestamp  time.Time `json:"timestamp"`
	Duration   string    `json:"duration"`
}

const (
	authModeMTLS       = "mtls"
	deploymentTunneled = "tunneled"
)

var (
	config         Config
	sessions       = make(map[string]*SessionData)
	oauth2Config   *oauth2.Config
	helmTemplate   *template.Template
	scriptTemplate *template.Template
)

const helmValuesTemplate = `nodeName: {{.VKName}}

interlink:
{{- if eq .DeploymentMode "tunneled"}}
  address: unix://{{.SSHTunnel.LocalSocket}}
  port: ""
{{- else}}
  address: https://{{.InterLinkIP}}
  port: {{.InterLinkPort}}
{{- end}}
  disableProjectedVolumes: {{.DisableProjected}}
{{- if eq .AuthMode "mtls"}}
  tls:
    enabled: true
    certFile: "/etc/interlink/certs/tls.crt"
    keyFile: "/etc/interlink/certs/tls.key"
    caCertFile: "/etc/interlink/certs/ca.crt"
{{- end}}

virtualNode:
  resources:
    CPUs: {{.VKLimits.CPU}}
    memGiB: {{.VKLimits.Memory}}
    pods: {{.VKLimits.Pods}}
  HTTPProxies:
    HTTP: null
    HTTPs: null
  HTTP:
{{- if eq .AuthMode "mtls"}}
    insecure: false
    CACert: "/etc/vk/certs/ca.crt"
{{- else}}
    CACert: {{ .CACert }}
    insecure: {{.HTTPInsecure}}
{{- end}}
  kubeletHTTP:
{{- if eq .AuthMode "mtls"}}
    insecure: false
{{- else}}
    insecure: true
{{- end}}
{{- if .NodeLabels}}
  nodeLabels:
{{- range $key, $value := .NodeLabels}}
    - "{{$key}}={{$value}}"
{{- end}}
{{- end}}
{{- if .NodeTaints}}
  nodeTaints:
{{- range .NodeTaints}}
    - key: "{{.Key}}"
      value: "{{.Value}}"
      effect: "{{.Effect}}"
{{- end}}
{{- end}}

{{- if eq .AuthMode "oauth"}}
OAUTH:
  enabled: {{if .OAUTH.ClientID}}true{{else}}false{{end}}
{{- if .OAUTH.ClientID}}
  TokenURL: {{.OAUTH.TokenURL}}
  ClientID: {{.OAUTH.ClientID}}
  ClientSecret: {{.OAUTH.ClientSecret}}
  RefreshToken: {{.OAUTH.RefreshToken}}
  GrantType: {{.OAUTH.GrantType}}
  Audience: {{.OAUTH.Audience}}
{{- end}}
{{- else}}
OAUTH:
  enabled: false
{{- end}}
`

const installScriptTemplate = `#!/bin/bash

# interLink Remote Installation Script
# Generated by interLink WebUI

set -e

INTERLINK_VERSION="{{.InterLinkVersion}}"
INTERLINK_PORT="{{.InterLinkPort}}"
INTERLINK_IP="{{.InterLinkIP}}"

install() {
    echo "Installing interLink API Server version $INTERLINK_VERSION..."
    
    # Download interLink binary
    curl -Lo interlink "https://github.com/interlink-hq/interlink/releases/download/v$INTERLINK_VERSION/interlink-linux-amd64"
    chmod +x interlink
    sudo mv interlink /usr/local/bin/
    
    # Create configuration directory
    sudo mkdir -p /etc/interlink
    
    # Create interLink configuration
    cat > /tmp/interLinkConfig.yaml << 'EOF'
InterlinkAddress: "0.0.0.0"
InterlinkPort: {{.InterLinkPort}}
SidecarURL: "http://localhost:4000"
SidecarEndpoint: "http://localhost:4000"
VerboseLogging: true
ErrorsOnlyLogging: false
ExportPodData: true
DataRootFolder: "/tmp/interlink"
{{- if .OAUTH.ClientID}}
Oauth:
  Provider: "{{.OAUTH.Provider}}"
  Issuer: "{{.OAUTH.Issuer}}"
  Audience: "{{.OAUTH.Audience}}"
  ClientId: "{{.OAUTH.ClientID}}"
  ClientSecret: "{{.OAUTH.ClientSecret}}"
  GroupClaim: "{{.OAUTH.GroupClaim}}"
  {{- if .OAUTH.Group}}
  Group: "{{.OAUTH.Group}}"
  {{- end}}
{{- end}}
EOF
    
    sudo mv /tmp/interLinkConfig.yaml /etc/interlink/
    
    # Create systemd service
    cat > /tmp/interlink.service << 'EOF'
[Unit]
Description=interLink API Server
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/interlink --config /etc/interlink/interLinkConfig.yaml
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF
    
    sudo mv /tmp/interlink.service /etc/systemd/system/
    sudo systemctl daemon-reload
    sudo systemctl enable interlink
    
    echo "interLink installed successfully!"
}

start() {
    echo "Starting interLink service..."
    sudo systemctl start interlink
    echo "interLink service started!"
    echo "Service status:"
    sudo systemctl status interlink --no-pager
}

stop() {
    echo "Stopping interLink service..."
    sudo systemctl stop interlink
    echo "interLink service stopped!"
}

case "$1" in
    install)
        install
        ;;
    start)
        start
        ;;
    stop)
        stop
        ;;
    *)
        echo "Usage: $0 {install|start|stop}"
        exit 1
        ;;
esac
`

func loadConfig() error {
	configFile := os.Getenv("WEBUI_CONFIG")
	if configFile == "" {
		configFile = "webui-config.yaml"
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	return nil
}

func initTemplates() error {
	var err error
	helmTemplate, err = template.New("helm").Parse(helmValuesTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse helm template: %w", err)
	}

	scriptTemplate, err = template.New("script").Parse(installScriptTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse script template: %w", err)
	}

	return nil
}

func generateState() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Printf("Failed to generate random state: %v", err)
		return ""
	}
	return base64.URLEncoding.EncodeToString(b)
}

func main() {
	if err := loadConfig(); err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	if err := initTemplates(); err != nil {
		log.Fatalf("Failed to initialize templates: %v", err)
	}

	// Only initialize OAuth2 config if not in test mode
	if !config.TestMode {
		oauth2Config = &oauth2.Config{
			ClientID:     config.OIDC.ClientID,
			ClientSecret: config.OIDC.ClientSecret,
			RedirectURL:  config.OIDC.RedirectURL,
			Scopes:       config.OIDC.Scopes,
			Endpoint: oauth2.Endpoint{
				AuthURL:  config.OIDC.Issuer + "/auth",
				TokenURL: config.OIDC.Issuer + "/token",
			},
		}
	}

	r := mux.NewRouter()

	// Static file serving
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir("./static/"))))

	// Routes
	r.HandleFunc("/", homeHandler).Methods("GET")
	r.HandleFunc("/auth/login", loginHandler).Methods("GET")
	r.HandleFunc("/auth/callback", callbackHandler).Methods("GET")
	r.HandleFunc("/auth/logout", logoutHandler).Methods("GET")
	r.HandleFunc("/configure", configureHandler).Methods("GET", "POST")
	r.HandleFunc("/generate/helm", generateHelmHandler).Methods("GET")
	r.HandleFunc("/generate/script", generateScriptHandler).Methods("GET")
	r.HandleFunc("/generate/mtls-manifest", generateMTLSManifestHandler).Methods("GET")
	r.HandleFunc("/generate/mtls-script", generateMTLSScriptHandler).Methods("GET")
	r.HandleFunc("/view/helm", viewHelmHandler).Methods("GET")
	r.HandleFunc("/view/script", viewScriptHandler).Methods("GET")
	r.HandleFunc("/view/mtls-manifest", viewMTLSManifestHandler).Methods("GET")
	r.HandleFunc("/view/mtls-script", viewMTLSScriptHandler).Methods("GET")
	r.HandleFunc("/monitor", monitorHandler).Methods("GET", "POST")
	r.HandleFunc("/pingLink", pingLinkHandler).Methods("POST")

	// Test mode routes
	if config.TestMode {
		r.HandleFunc("/auth/test-login", testLoginHandler).Methods("GET")
		r.HandleFunc("/test-cookie", testCookieHandler).Methods("GET")
	}

	addr := fmt.Sprintf("%s:%d", config.Server.Host, config.Server.Port)

	server := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("Starting webui server on %s", addr)
	log.Fatal(server.ListenAndServe())
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Home handler: All cookies: %v", r.Cookies())
	sessionID, err := r.Cookie("session_id")
	if err != nil {
		log.Printf("No session cookie found: %v", err)
		renderTemplate(w, "login", map[string]interface{}{
			"TestMode": config.TestMode,
		})
		return
	}

	if sessions[sessionID.Value] == nil {
		log.Printf("Session not found for ID: %s, available sessions: %d", sessionID.Value, len(sessions))
		renderTemplate(w, "login", map[string]interface{}{
			"TestMode": config.TestMode,
		})
		return
	}

	log.Printf("Session found for ID: %s", sessionID.Value)

	session := sessions[sessionID.Value]
	templateData := map[string]interface{}{
		"UserInfo":   session.UserInfo,
		"ConfigData": session.ConfigData,
		"Tokens":     session.Tokens,
		"TestMode":   config.TestMode,
	}
	renderTemplate(w, "home", templateData)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if config.TestMode {
		http.Redirect(w, r, "/auth/test-login", http.StatusTemporaryRedirect)
		return
	}

	if oauth2Config == nil {
		http.Error(w, "OAuth2 not configured", http.StatusInternalServerError)
		return
	}

	state := generateState()
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		MaxAge:   600,
		HttpOnly: true,
		Secure:   false,
	})

	url := oauth2Config.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func testLoginHandler(w http.ResponseWriter, r *http.Request) {
	if !config.TestMode {
		http.Error(w, "Test mode not enabled", http.StatusForbidden)
		return
	}

	// Create a dummy session with test user info
	sessionID := generateState()
	if sessionID == "" {
		http.Error(w, "Failed to generate session ID", http.StatusInternalServerError)
		return
	}

	log.Printf("Test login: Creating session with ID: %s", sessionID)
	sessions[sessionID] = &SessionData{
		UserInfo: map[string]interface{}{
			"name":  "Test User",
			"email": "test@example.com",
			"sub":   "test-user-123",
		},
		Tokens: nil, // No real tokens in test mode
		ConfigData: InterLinkConfig{
			VKName:           "test-vk-node",
			Namespace:        "interlink",
			InterLinkVersion: "0.5.1",
			VKLimits: Resources{
				CPU:    "10",
				Memory: "256",
				Pods:   "10",
			},
			HTTPInsecure:     true,
			DisableProjected: true,
			AuthMode:         "oauth",
			DeploymentMode:   "edge-node",
			SSHTunnel: SSHTunnelConfig{
				SSHPort:     22,
				PluginPort:  4000,
				LocalSocket: "/tmp/interlink.sock",
			},
		},
	}

	cookie := &http.Cookie{
		Name:     "session_id",
		Value:    sessionID,
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	}

	log.Printf("Test login: Setting cookie: %+v", cookie)
	http.SetCookie(w, cookie)

	log.Printf("Test login: Redirecting to /")
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func testCookieHandler(w http.ResponseWriter, _ *http.Request) {
	if !config.TestMode {
		http.Error(w, "Test mode not enabled", http.StatusForbidden)
		return
	}

	testCookie := &http.Cookie{
		Name:     "test_cookie",
		Value:    "test_value",
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: false, // Make it visible in browser dev tools
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	}

	log.Printf("Test cookie: Setting cookie: %+v", testCookie)
	http.SetCookie(w, testCookie)

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<html><body>
		<h1>Test Cookie Set</h1>
		<p>Cookie should be set. Check browser dev tools.</p>
		<p><a href="/">Go back to home</a></p>
		<script>
			console.log('Document cookies:', document.cookie);
		</script>
	</body></html>`)
}

func callbackHandler(w http.ResponseWriter, r *http.Request) {
	if config.TestMode {
		http.Error(w, "Callback not available in test mode", http.StatusForbidden)
		return
	}

	if oauth2Config == nil {
		http.Error(w, "OAuth2 not configured", http.StatusInternalServerError)
		return
	}

	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	token, err := oauth2Config.Exchange(context.Background(), code)
	if err != nil {
		http.Error(w, "Failed to exchange token", http.StatusInternalServerError)
		return
	}

	userInfo, err := getUserInfo(token)
	if err != nil {
		http.Error(w, "Failed to get user info", http.StatusInternalServerError)
		return
	}

	sessionID := generateState()
	sessions[sessionID] = &SessionData{
		UserInfo: userInfo,
		Tokens:   token,
		ConfigData: InterLinkConfig{
			VKName:           "my-vk-node",
			Namespace:        "interlink",
			InterLinkVersion: "0.5.1",
			VKLimits: Resources{
				CPU:    "10",
				Memory: "256",
				Pods:   "10",
			},
			HTTPInsecure:     true,
			DisableProjected: true,
			AuthMode:         "oauth",
			DeploymentMode:   "edge-node",
			SSHTunnel: SSHTunnelConfig{
				SSHPort:     22,
				PluginPort:  4000,
				LocalSocket: "/tmp/interlink.sock",
			},
		},
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_id",
		Value:    sessionID,
		MaxAge:   3600,
		HttpOnly: true,
		Secure:   false,
	})

	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err == nil {
		delete(sessions, sessionID.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_id",
		Value:    "",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   false,
	})

	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func configureHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	session := sessions[sessionID.Value]

	if r.Method == "POST" {
		// Parse form data and update configuration
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
			return
		}

		configData := &session.ConfigData
		configData.VKName = r.FormValue("vk_name")
		configData.Namespace = r.FormValue("namespace")
		configData.InterLinkIP = r.FormValue("interlink_ip")
		if port, err := strconv.Atoi(r.FormValue("interlink_port")); err == nil {
			configData.InterLinkPort = port
		}
		configData.InterLinkVersion = r.FormValue("interlink_version")
		configData.VKLimits.CPU = r.FormValue("cpu_limit")
		configData.VKLimits.Memory = r.FormValue("memory_limit")
		configData.VKLimits.Pods = r.FormValue("pods_limit")
		configData.HTTPInsecure = r.FormValue("http_insecure") == "on"
		configData.DisableProjected = r.FormValue("disable_projected") == "on"
		configData.CACert = r.FormValue("ca_cert")

		// Parse node labels
		nodeLabels := make(map[string]string)
		labelKeys := r.Form["label_key"]
		labelValues := r.Form["label_value"]
		for i, key := range labelKeys {
			if key != "" && i < len(labelValues) && labelValues[i] != "" {
				nodeLabels[key] = labelValues[i]
			}
		}
		if len(nodeLabels) > 0 {
			configData.NodeLabels = nodeLabels
		}

		// Parse node taints
		var nodeTaints []NodeTaint
		taintKeys := r.Form["taint_key"]
		taintValues := r.Form["taint_value"]
		taintEffects := r.Form["taint_effect"]
		for i, key := range taintKeys {
			if key != "" && i < len(taintValues) && i < len(taintEffects) {
				nodeTaints = append(nodeTaints, NodeTaint{
					Key:    key,
					Value:  taintValues[i],
					Effect: taintEffects[i],
				})
			}
		}
		configData.NodeTaints = nodeTaints

		// OAuth configuration
		configData.OAUTH.Provider = r.FormValue("oauth_provider")
		configData.OAUTH.GrantType = r.FormValue("oauth_grant_type")
		configData.OAUTH.Issuer = r.FormValue("oauth_issuer")
		configData.OAUTH.Audience = r.FormValue("oauth_audience")
		configData.OAUTH.Group = r.FormValue("oauth_group")
		configData.OAUTH.GroupClaim = r.FormValue("oauth_group_claim")
		configData.OAUTH.ClientID = r.FormValue("oauth_client_id")
		configData.OAUTH.ClientSecret = r.FormValue("oauth_client_secret")
		configData.OAUTH.TokenURL = r.FormValue("oauth_token_url")
		configData.OAUTH.DeviceCodeURL = r.FormValue("oauth_device_code_url")

		scopes := strings.Split(r.FormValue("oauth_scopes"), ",")
		for i, scope := range scopes {
			scopes[i] = strings.TrimSpace(scope)
		}
		configData.OAUTH.Scopes = scopes

		// Authentication mode and mTLS configuration
		configData.AuthMode = r.FormValue("auth_mode")
		configData.MTLSEnabled = r.FormValue("mtls_enabled") == "on"

		// Deployment mode and SSH tunnel configuration
		configData.DeploymentMode = r.FormValue("deployment_mode")

		// Force mTLS for tunneled deployments
		if configData.DeploymentMode == deploymentTunneled {
			configData.AuthMode = authModeMTLS
			configData.MTLSEnabled = true
			sshPort := 22
			if portStr := r.FormValue("ssh_port"); portStr != "" {
				if p, err := strconv.Atoi(portStr); err == nil {
					sshPort = p
				}
			}

			pluginPort := 4000
			if portStr := r.FormValue("ssh_plugin_port"); portStr != "" {
				if p, err := strconv.Atoi(portStr); err == nil {
					pluginPort = p
				}
			}

			localSocket := r.FormValue("ssh_local_socket")
			if localSocket == "" {
				localSocket = "/tmp/interlink.sock"
			}

			configData.SSHTunnel = SSHTunnelConfig{
				RemoteHost:     r.FormValue("ssh_remote_host"),
				RemoteUser:     r.FormValue("ssh_remote_user"),
				SSHPort:        sshPort,
				PrivateKeyPath: "/opt/interlink/.ssh/id_rsa",
				HostKeyPath:    "",
				LocalSocket:    localSocket,
				PluginPort:     pluginPort,
			}

			// Generate SSH keys automatically for tunneled deployment
			sshKeys, err := GenerateSSHKeyPair()
			if err != nil {
				log.Printf("Failed to generate SSH keys: %v", err)
				http.Error(w, "Failed to generate SSH keys", http.StatusInternalServerError)
				return
			}
			configData.SSHTunnel.SSHKeys = sshKeys
			log.Printf("Generated SSH key pair for tunneled deployment")
		}

		// Generate mTLS certificates if enabled
		if configData.AuthMode == authModeMTLS && configData.MTLSEnabled {
			commonName := "interlink-server"
			if configData.InterLinkIP != "" {
				// Generate certificates for the configured IP
				certs, err := GenerateInterLinkCertificates(commonName, configData.InterLinkIP)
				if err != nil {
					log.Printf("Failed to generate mTLS certificates: %v", err)
					http.Error(w, "Failed to generate mTLS certificates", http.StatusInternalServerError)
					return
				}
				configData.MTLSCertificates = certs
				log.Printf("Generated mTLS certificates for %s (%s)", commonName, configData.InterLinkIP)
			}
		}

		// Redirect to home page after saving configuration
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	templateData := map[string]interface{}{
		"UserInfo":   session.UserInfo,
		"ConfigData": session.ConfigData,
		"Tokens":     session.Tokens,
		"TestMode":   config.TestMode,
	}
	renderTemplate(w, "configure", templateData)
}

func generateHelmHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	session := sessions[sessionID.Value]

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", "attachment; filename=values.yaml")

	if err := helmTemplate.Execute(w, session.ConfigData); err != nil {
		http.Error(w, "Failed to generate helm values", http.StatusInternalServerError)
		return
	}
}

func generateScriptHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	session := sessions[sessionID.Value]

	w.Header().Set("Content-Type", "application/x-sh")
	w.Header().Set("Content-Disposition", "attachment; filename=interlink-install.sh")

	if err := scriptTemplate.Execute(w, session.ConfigData); err != nil {
		http.Error(w, "Failed to generate install script", http.StatusInternalServerError)
		return
	}
}

func viewHelmHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	session := sessions[sessionID.Value]

	var helmContent strings.Builder
	if err := helmTemplate.Execute(&helmContent, session.ConfigData); err != nil {
		http.Error(w, "Failed to generate helm values", http.StatusInternalServerError)
		return
	}

	renderTemplate(w, "file-view", map[string]interface{}{
		"Title":    "Helm Values (values.yaml)",
		"Content":  helmContent.String(),
		"Filename": "values.yaml",
		"Language": "yaml",
		"TestMode": config.TestMode,
	})
}

func viewScriptHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	session := sessions[sessionID.Value]

	var scriptContent strings.Builder
	if err := scriptTemplate.Execute(&scriptContent, session.ConfigData); err != nil {
		http.Error(w, "Failed to generate install script", http.StatusInternalServerError)
		return
	}

	renderTemplate(w, "file-view", map[string]interface{}{
		"Title":    "Installation Script (interlink-install.sh)",
		"Content":  scriptContent.String(),
		"Filename": "interlink-install.sh",
		"Language": "bash",
		"TestMode": config.TestMode,
	})
}

func generateMTLSManifestHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	session := sessions[sessionID.Value]

	if session.ConfigData.AuthMode != authModeMTLS || !session.ConfigData.MTLSEnabled || session.ConfigData.MTLSCertificates == nil {
		http.Error(w, "mTLS not configured or certificates not generated", http.StatusBadRequest)
		return
	}

	manifest := GenerateMTLSKubernetesManifest(session.ConfigData.MTLSCertificates, session.ConfigData.Namespace)

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", "attachment; filename=interlink-mtls-manifest.yaml")
	if _, err := w.Write([]byte(manifest)); err != nil {
		http.Error(w, "Failed to write manifest", http.StatusInternalServerError)
	}
}

func generateMTLSScriptHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	session := sessions[sessionID.Value]

	if session.ConfigData.AuthMode != authModeMTLS || !session.ConfigData.MTLSEnabled || session.ConfigData.MTLSCertificates == nil {
		http.Error(w, "mTLS not configured or certificates not generated", http.StatusBadRequest)
		return
	}

	var script string
	if session.ConfigData.DeploymentMode == deploymentTunneled {
		script = GenerateTunneledMTLSInstallScript(&session.ConfigData, session.ConfigData.MTLSCertificates)
	} else {
		script = GenerateMTLSInstallScript(&session.ConfigData, session.ConfigData.MTLSCertificates)
	}

	w.Header().Set("Content-Type", "application/x-sh")
	w.Header().Set("Content-Disposition", "attachment; filename=interlink-mtls-install.sh")
	if _, err := w.Write([]byte(script)); err != nil {
		http.Error(w, "Failed to write script", http.StatusInternalServerError)
	}
}

func viewMTLSManifestHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	session := sessions[sessionID.Value]

	if session.ConfigData.AuthMode != authModeMTLS || !session.ConfigData.MTLSEnabled || session.ConfigData.MTLSCertificates == nil {
		http.Error(w, "mTLS not configured or certificates not generated", http.StatusBadRequest)
		return
	}

	manifest := GenerateMTLSKubernetesManifest(session.ConfigData.MTLSCertificates, session.ConfigData.Namespace)

	renderTemplate(w, "file-view", map[string]interface{}{
		"Title":    "mTLS Kubernetes Manifest (interlink-mtls-manifest.yaml)",
		"Content":  manifest,
		"Filename": "interlink-mtls-manifest.yaml",
		"Language": "yaml",
		"TestMode": config.TestMode,
	})
}

func viewMTLSScriptHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	session := sessions[sessionID.Value]

	if session.ConfigData.AuthMode != authModeMTLS || !session.ConfigData.MTLSEnabled || session.ConfigData.MTLSCertificates == nil {
		http.Error(w, "mTLS not configured or certificates not generated", http.StatusBadRequest)
		return
	}

	var script string
	if session.ConfigData.DeploymentMode == deploymentTunneled {
		script = GenerateTunneledMTLSInstallScript(&session.ConfigData, session.ConfigData.MTLSCertificates)
	} else {
		script = GenerateMTLSInstallScript(&session.ConfigData, session.ConfigData.MTLSCertificates)
	}

	renderTemplate(w, "file-view", map[string]interface{}{
		"Title":    "mTLS Installation Script (interlink-mtls-install.sh)",
		"Content":  script,
		"Filename": "interlink-mtls-install.sh",
		"Language": "bash",
		"TestMode": config.TestMode,
	})
}

func getUserInfo(token *oauth2.Token) (map[string]interface{}, error) {
	if oauth2Config == nil {
		return nil, fmt.Errorf("OAuth2 not configured")
	}

	client := oauth2Config.Client(context.Background(), token)
	resp, err := client.Get(config.OIDC.Issuer + "/userinfo")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var userInfo map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}

	return userInfo, nil
}

func monitorHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	session := sessions[sessionID.Value]
	templateData := map[string]interface{}{
		"UserInfo":   session.UserInfo,
		"ConfigData": session.ConfigData,
		"Tokens":     session.Tokens,
		"TestMode":   config.TestMode,
	}

	renderTemplate(w, "monitor", templateData)
}

func pingLinkHandler(w http.ResponseWriter, r *http.Request) {
	sessionID, err := r.Cookie("session_id")
	if err != nil || sessions[sessionID.Value] == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	session := sessions[sessionID.Value]

	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	endpointURL := r.FormValue("endpoint_url")
	if endpointURL == "" {
		http.Error(w, "endpoint_url is required", http.StatusBadRequest)
		return
	}

	// Get auth token - prefer manual input, fallback to session token
	authToken := r.FormValue("auth_token")
	if authToken == "" && session.Tokens != nil && session.Tokens.AccessToken != "" {
		authToken = session.Tokens.AccessToken
	}

	// Validate URL format
	parsedURL, err := url.Parse(endpointURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		http.Error(w, "Invalid URL format", http.StatusBadRequest)
		return
	}

	// Ensure the URL ends with /pinglink if not already specified
	if !strings.HasSuffix(parsedURL.Path, "/pinglink") {
		if parsedURL.Path == "" || parsedURL.Path == "/" {
			parsedURL.Path = "/pinglink"
		} else {
			parsedURL.Path = strings.TrimSuffix(parsedURL.Path, "/") + "/pinglink"
		}
		endpointURL = parsedURL.String()
	}

	result := pingInterLinkEndpoint(endpointURL, authToken)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("Failed to encode ping result: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func pingInterLinkEndpoint(endpointURL string, authToken string) MonitorResult {
	start := time.Now()

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("POST", endpointURL, nil)
	if err != nil {
		return MonitorResult{
			URL:        endpointURL,
			Timestamp:  time.Now(),
			Duration:   time.Since(start).String(),
			Status:     "error",
			StatusCode: 0,
			Message:    fmt.Sprintf("Failed to create request: %v", err),
		}
	}

	req.Header.Set("Content-Type", "application/json")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := client.Do(req)
	duration := time.Since(start)

	result := MonitorResult{
		URL:       endpointURL,
		Timestamp: time.Now(),
		Duration:  duration.String(),
	}

	if err != nil {
		result.Status = "error"
		result.StatusCode = 0
		result.Message = fmt.Sprintf("Failed to connect: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.Status = "success"
		result.Message = "interLink endpoint is responding"
	} else {
		result.Status = "warning"
		result.Message = fmt.Sprintf("HTTP %d: Endpoint responded but with error status", resp.StatusCode)
	}

	return result
}

func renderTemplate(w http.ResponseWriter, templateName string, data interface{}) {
	templates := map[string]string{
		"login": `<!DOCTYPE html>
<html>
<head>
    <title>interLink WebUI - Login</title>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <style>
        :root {
            --primary-color: #ff6600;
            --primary-dark: #cc5200;
            --secondary-color: #2c3e50;
            --background: #f8f9fa;
            --surface: #ffffff;
            --text-primary: #2c3e50;
            --text-secondary: #6c757d;
            --border: #dee2e6;
            --success: #28a745;
            --warning: #ffc107;
            --danger: #dc3545;
        }
        
        * { box-sizing: border-box; }
        
        body { 
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; 
            max-width: 900px; 
            margin: 0 auto; 
            padding: 20px; 
            background: linear-gradient(135deg, var(--background) 0%, #e9ecef 100%);
            min-height: 100vh;
            color: var(--text-primary);
        }
        
        .header-logo {
            display: flex;
            align-items: center;
            justify-content: center;
            margin-bottom: 40px;
        }
        
        .header-logo img {
            height: 60px;
            width: auto;
            max-width: 120px;
            margin-right: 15px;
            object-fit: contain;
        }
        
        .header-logo h1 {
            color: var(--secondary-color);
            font-size: 2.5rem;
            margin: 0;
            font-weight: 300;
        }
        
        .container { 
            text-align: center; 
            margin-top: 40px; 
            background: var(--surface);
            padding: 40px;
            border-radius: 12px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
        }
        
        .subtitle {
            color: var(--text-secondary);
            font-size: 1.1rem;
            margin-bottom: 30px;
        }
        
        .btn { 
            background: var(--primary-color); 
            color: white; 
            padding: 12px 24px; 
            text-decoration: none; 
            border-radius: 8px; 
            display: inline-block; 
            margin: 8px; 
            font-weight: 500;
            border: none;
            cursor: pointer;
            transition: all 0.3s ease;
            font-size: 1rem;
        }
        
        .btn:hover { 
            background: var(--primary-dark); 
            transform: translateY(-1px);
            box-shadow: 0 4px 8px rgba(255, 102, 0, 0.3);
        }
        
        .btn-warning { 
            background: var(--warning); 
            color: var(--secondary-color); 
        }
        
        .btn-warning:hover { 
            background: #e0a800; 
            transform: translateY(-1px);
        }
        
        .test-mode-info { 
            background: linear-gradient(45deg, #fff3cd, #ffeaa7); 
            border: 1px solid var(--warning); 
            border-radius: 8px; 
            padding: 20px; 
            margin: 30px 0;
            box-shadow: 0 2px 4px rgba(255, 193, 7, 0.2);
        }
        
        .test-mode-info h3 {
            color: var(--secondary-color);
            margin-top: 0;
        }
        
        .feature-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
            gap: 20px;
            margin: 30px 0;
            text-align: left;
        }
        
        .feature-card {
            background: var(--surface);
            padding: 20px;
            border-radius: 8px;
            border-left: 4px solid var(--primary-color);
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.05);
        }
        
        .feature-card h4 {
            color: var(--secondary-color);
            margin-top: 0;
            margin-bottom: 10px;
        }
        
        .feature-card p {
            color: var(--text-secondary);
            margin: 0;
            font-size: 0.9rem;
        }
    </style>
</head>
<body>
		<script>
			htmx.logAll();
		</script>
    <div class="header-logo">
        <img src="/static/img/interlink_logo.png" alt="interLink Logo" onerror="this.src='data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iNDAiIGhlaWdodD0iNDAiIHZpZXdCb3g9IjAgMCA0MCA0MCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj48Y2lyY2xlIGN4PSIyMCIgY3k9IjIwIiByPSIyMCIgZmlsbD0iI2ZmNjYwMCIvPjx0ZXh0IHg9IjIwIiB5PSIyNSIgdGV4dC1hbmNob3I9Im1pZGRsZSIgZmlsbD0id2hpdGUiIGZvbnQtZmFtaWx5PSJBcmlhbCIgZm9udC1zaXplPSIxNiIgZm9udC13ZWlnaHQ9ImJvbGQiPmlMPC90ZXh0Pjwvc3ZnPg=='; this.style.width='40px'; this.style.height='40px';">
        <h1>interLink WebUI</h1>
    </div>
    
    <div class="container">
        <p class="subtitle">Configure and deploy interLink with an intuitive web interface</p>
        
        <div class="feature-grid">
            <div class="feature-card">
                <h4>🔐 Secure Authentication</h4>
                <p>OIDC-based authentication with support for multiple providers including GitHub and custom OIDC endpoints.</p>
            </div>
            <div class="feature-card">
                <h4>⚙️ Interactive Configuration</h4>
                <p>Web-based forms for configuring Virtual Kubelet nodes, resource limits, OAuth settings, and more.</p>
            </div>
            <div class="feature-card">
                <h4>📁 File Generation</h4>
                <p>Automatically generate Helm values and installation scripts based on your configuration.</p>
            </div>
        </div>
        
        <a href="/auth/login" class="btn">🚀 Login with OIDC</a>
        
        {{if .TestMode}}
        <div class="test-mode-info">
            <h3>🧪 Test Mode Enabled</h3>
            <p>You can use dummy authentication for development and testing purposes</p>
            <div style="margin-top: 15px;">
                <a href="/auth/test-login" class="btn btn-warning">Test Login (No OIDC)</a>
                <a href="/test-cookie" class="btn btn-warning">Test Cookie</a>
            </div>
        </div>
        {{end}}
    </div>
</body>
</html>`,
		"home": `<!DOCTYPE html>
<html>
<head>
    <title>interLink WebUI - Dashboard</title>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <style>
        :root {
            --primary-color: #ff6600;
            --primary-dark: #cc5200;
            --secondary-color: #2c3e50;
            --background: #f8f9fa;
            --surface: #ffffff;
            --text-primary: #2c3e50;
            --text-secondary: #6c757d;
            --border: #dee2e6;
            --success: #28a745;
            --warning: #ffc107;
            --danger: #dc3545;
        }
        
        * { box-sizing: border-box; }
        
        body { 
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; 
            max-width: 1200px; 
            margin: 0 auto; 
            padding: 20px; 
            background: linear-gradient(135deg, var(--background) 0%, #e9ecef 100%);
            min-height: 100vh;
            color: var(--text-primary);
        }
        
        .header { 
            display: flex; 
            justify-content: space-between; 
            align-items: center; 
            margin-bottom: 30px; 
            background: var(--surface);
            padding: 20px;
            border-radius: 12px;
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1);
        }
        
        .header-logo {
            display: flex;
            align-items: center;
        }
        
        .header-logo img {
            height: 40px;
            width: auto;
            max-width: 80px;
            margin-right: 12px;
            object-fit: contain;
        }
        
        .header-logo h1 {
            color: var(--secondary-color);
            font-size: 1.8rem;
            margin: 0;
            font-weight: 300;
        }
        
        .header-actions {
            display: flex;
            align-items: center;
            gap: 10px;
        }
        
        .test-mode-badge {
            background: linear-gradient(45deg, var(--warning), #ffeaa7);
            color: var(--secondary-color);
            padding: 6px 12px;
            border-radius: 20px;
            font-size: 0.85rem;
            font-weight: 500;
            border: 1px solid var(--warning);
        }
        
        .user-info { 
            background: linear-gradient(135deg, var(--surface) 0%, #f1f3f4 100%);
            padding: 20px; 
            border-radius: 12px; 
            margin-bottom: 25px;
            border-left: 4px solid var(--primary-color);
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.05);
        }
        
        .user-info strong {
            color: var(--secondary-color);
            font-size: 1.1rem;
        }
        
        .user-info p {
            margin: 5px 0 0 0;
            color: var(--text-secondary);
        }
        
        .btn { 
            background: var(--primary-color); 
            color: white; 
            padding: 12px 20px; 
            text-decoration: none; 
            border-radius: 8px; 
            border: none; 
            cursor: pointer;
            font-weight: 500;
            transition: all 0.3s ease;
            display: inline-flex;
            align-items: center;
            gap: 8px;
        }
        
        .btn:hover { 
            background: var(--primary-dark); 
            transform: translateY(-1px);
            box-shadow: 0 4px 8px rgba(255, 102, 0, 0.3);
        }
        
        .btn-secondary { 
            background: var(--secondary-color); 
        }
        
        .btn-secondary:hover { 
            background: #34495e; 
            box-shadow: 0 4px 8px rgba(44, 62, 80, 0.3);
        }
        
        .btn-success {
            background: var(--success);
        }
        
        .btn-success:hover {
            background: #218838;
            box-shadow: 0 4px 8px rgba(40, 167, 69, 0.3);
        }
        
        .card { 
            background: var(--surface); 
            border: 1px solid var(--border); 
            border-radius: 12px; 
            padding: 25px; 
            margin-bottom: 25px;
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.08);
            transition: transform 0.2s ease, box-shadow 0.2s ease;
        }
        
        .card:hover {
            transform: translateY(-2px);
            box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
        }
        
        .card h2 {
            color: var(--secondary-color);
            margin-top: 0;
            margin-bottom: 12px;
            font-size: 1.4rem;
            font-weight: 600;
        }
        
        .card p {
            color: var(--text-secondary);
            margin-bottom: 20px;
            line-height: 1.5;
        }
        
        .actions { 
            display: flex; 
            flex-wrap: wrap;
            gap: 12px; 
            margin-top: 20px; 
        }
        
        .deployment-steps {
            background: linear-gradient(135deg, #e3f2fd 0%, #f3e5f5 100%);
            border-radius: 12px;
            padding: 25px;
            margin-bottom: 25px;
            border-left: 4px solid var(--primary-color);
        }
        
        .deployment-steps h3 {
            color: var(--secondary-color);
            margin-top: 0;
            margin-bottom: 15px;
        }
        
        .step-list {
            list-style: none;
            padding: 0;
            margin: 0;
        }
        
        .step-list li {
            padding: 10px 0;
            border-bottom: 1px solid rgba(44, 62, 80, 0.1);
            color: var(--text-secondary);
        }
        
        .step-list li:last-child {
            border-bottom: none;
        }
        
        .step-number {
            background: var(--primary-color);
            color: white;
            width: 24px;
            height: 24px;
            border-radius: 50%;
            display: inline-flex;
            align-items: center;
            justify-content: center;
            font-size: 0.85rem;
            font-weight: 600;
            margin-right: 12px;
        }
    </style>
</head>
<body>
		<script>
			htmx.logAll();
		</script>
    <div class="header">
        <div class="header-logo">
            <img src="/static/img/interlink_logo.png" alt="interLink Logo" onerror="this.src='data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iNDAiIGhlaWdodD0iNDAiIHZpZXdCb3g9IjAgMCA0MCA0MCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj48Y2lyY2xlIGN4PSIyMCIgY3k9IjIwIiByPSIyMCIgZmlsbD0iI2ZmNjYwMCIvPjx0ZXh0IHg9IjIwIiB5PSIyNSIgdGV4dC1hbmNob3I9Im1pZGRsZSIgZmlsbD0id2hpdGUiIGZvbnQtZmFtaWx5PSJBcmlhbCIgZm9udC1zaXplPSIxNiIgZm9udC13ZWlnaHQ9ImJvbGQiPmlMPC90ZXh0Pjwvc3ZnPg=='; this.style.width='40px'; this.style.height='40px';">
            <h1>interLink WebUI Dashboard</h1>
        </div>
        <div class="header-actions">
            {{if .TestMode}}<span class="test-mode-badge">🧪 Test Mode</span>{{end}}
            <a href="/auth/logout" class="btn btn-secondary">Logout</a>
        </div>
    </div>
    
    {{if .UserInfo}}
    <div class="user-info">
        <strong>👋 Welcome, {{.UserInfo.name}}!</strong>
        <p>📧 {{.UserInfo.email}}</p>
    </div>
    {{end}}
    
    <div class="deployment-steps">
        <h3>🚀 Deployment Workflow</h3>
        <ul class="step-list">
            <li><span class="step-number">1</span><strong>Configure:</strong> Set up your interLink deployment parameters below</li>
            <li><span class="step-number">2</span><strong>Generate:</strong> Download Helm values and installation scripts</li>
            <li><span class="step-number">3</span><strong>Deploy:</strong> Use Helm to deploy Virtual Kubelet to your cluster</li>
            <li><span class="step-number">4</span><strong>Install:</strong> Run the script on your remote server to set up interLink API</li>
        </ul>
    </div>
    
    <div class="card">
        <h2>⚙️ Configuration</h2>
        <p>Configure your interLink deployment settings including Virtual Kubelet node parameters, resource limits, OAuth settings, and node labels/taints. All configuration is session-based and secure.</p>
        <div class="actions">
            <a href="/configure" class="btn">🔧 Configure Deployment</a>
        </div>
    </div>
    
    <div class="card">
        <h2>📁 Generate Files</h2>
        <p>Generate deployment files based on your configuration. Download files directly or view them with syntax highlighting and copy functionality.</p>
        
        <h4 style="color: var(--secondary-color); margin: 20px 0 10px 0;">Helm Chart Values</h4>
        {{if eq .ConfigData.DeploymentMode "tunneled"}}
        <p style="font-size: 0.9rem; color: var(--text-secondary); margin-bottom: 15px;">
            🚇 <strong>Tunneled Deployment:</strong> Virtual Kubelet configuration for Unix socket communication with automatic mTLS certificate mounting and SSH tunnel connectivity.
        </p>
        {{else}}
        <p style="font-size: 0.9rem; color: var(--text-secondary); margin-bottom: 15px;">
            Kubernetes deployment configuration for Virtual Kubelet with your custom settings.
        </p>
        {{end}}
        <div class="actions">
            <a href="/generate/helm" class="btn btn-success" download="values.yaml">📥 Download values.yaml</a>
            <a href="/view/helm" class="btn btn-secondary">👁️ View & Copy</a>
        </div>
        
        <h4 style="color: var(--secondary-color); margin: 20px 0 10px 0;">Installation Script</h4>
        <p style="font-size: 0.9rem; color: var(--text-secondary); margin-bottom: 15px;">
            Bash script to install and configure interLink API server on your remote infrastructure.
        </p>
        <div class="actions">
            <a href="/generate/script" class="btn btn-success" download="interlink-install.sh">📥 Download interlink-install.sh</a>
            <a href="/view/script" class="btn btn-secondary">👁️ View & Copy</a>
        </div>
    </div>
    
    {{if and (eq .ConfigData.AuthMode "mtls") .ConfigData.MTLSEnabled .ConfigData.MTLSCertificates}}
    <div class="card">
        <h2>🔐 mTLS Certificate Files</h2>
        <p>Download the automatically generated mTLS certificates and deployment files for secure interLink communication.</p>
        
        <h4 style="color: var(--secondary-color); margin: 20px 0 10px 0;">Kubernetes Manifest with Certificates</h4>
        <p style="font-size: 0.9rem; color: var(--text-secondary); margin-bottom: 15px;">
            Complete Kubernetes manifest including namespace, secrets with certificates, and configuration for mTLS deployment.
        </p>
        <div class="actions">
            <a href="/generate/mtls-manifest" class="btn btn-success" download="interlink-mtls-manifest.yaml">📥 Download mTLS Manifest</a>
            <a href="/view/mtls-manifest" class="btn btn-secondary">👁️ View & Copy</a>
        </div>
        
        {{if eq .ConfigData.DeploymentMode "tunneled"}}
        <h4 style="color: var(--secondary-color); margin: 20px 0 10px 0;">🚇 Tunneled mTLS Installation Script</h4>
        <p style="font-size: 0.9rem; color: var(--text-secondary); margin-bottom: 15px;">
            Complete tunneled deployment script with automatic SSH key generation, mTLS certificates, systemd services, and secure SSH tunnel setup.
        </p>
        <div class="actions">
            <a href="/generate/mtls-script" class="btn btn-success" download="interlink-tunneled-mtls-install.sh">📥 Download Tunneled Script</a>
            <a href="/view/mtls-script" class="btn btn-secondary">👁️ View & Copy</a>
        </div>
        
        <div style="background: linear-gradient(45deg, #fff3cd, #ffeaa7); border: 1px solid #fd7e14; border-radius: 8px; padding: 15px; margin: 15px 0;">
            <h5 style="color: #e07500; margin-top: 0;">🚇 Tunneled Deployment Features</h5>
            <ul style="margin: 10px 0; color: #e07500;">
                <li><strong>🔑 Automatic SSH Key Generation:</strong> 4096-bit RSA keys generated automatically - no manual setup required!</li>
                <li><strong>🔐 mTLS Security:</strong> Certificates and SSH keys work together for maximum security</li>
                <li><strong>🔧 Complete systemd Integration:</strong> SSH tunnel, API server, and monitoring services</li>
                <li><strong>📋 Clear Setup Instructions:</strong> Exact commands for remote server SSH key installation</li>
                <li><strong>🌐 Secure Remote Connectivity:</strong> Encrypted tunnel through SSH with certificate authentication</li>
            </ul>
            <div style="background: #fff; border-left: 4px solid #fd7e14; padding: 10px; margin-top: 15px; border-radius: 4px;">
                <strong>📋 SSH Setup:</strong> The script includes automatically generated SSH keys and provides exact instructions for installing the public key on your remote server.
            </div>
        </div>
        {{else}}
        <h4 style="color: var(--secondary-color); margin: 20px 0 10px 0;">mTLS Installation Script</h4>
        <p style="font-size: 0.9rem; color: var(--text-secondary); margin-bottom: 15px;">
            Automated script that creates certificates, Kubernetes secrets, and deploys interLink with mTLS authentication.
        </p>
        <div class="actions">
            <a href="/generate/mtls-script" class="btn btn-success" download="interlink-mtls-install.sh">📥 Download mTLS Script</a>
            <a href="/view/mtls-script" class="btn btn-secondary">👁️ View & Copy</a>
        </div>
        
        <div style="background: linear-gradient(45deg, #e8f5e8, #d4edda); border: 1px solid #28a745; border-radius: 8px; padding: 15px; margin: 15px 0;">
            <h5 style="color: #155724; margin-top: 0;">🔐 Security Benefits of mTLS</h5>
            <ul style="margin: 10px 0; color: #155724;">
                <li><strong>Strong Authentication:</strong> Cryptographic client and server verification</li>
                <li><strong>No External Dependencies:</strong> Works without internet connectivity or external identity providers</li>
                <li><strong>Automatic Certificate Generation:</strong> 4096-bit RSA keys with 1-year validity</li>
                <li><strong>Zero Trust Architecture:</strong> Every connection is verified and encrypted</li>
            </ul>
        </div>
        {{end}}
    </div>
    {{end}}
    
    <div class="card">
        <h2>🔍 Monitor Deployment</h2>
        <p>After deploying interLink, monitor your endpoints to verify everything is running correctly and troubleshoot any connectivity issues.</p>
        <div class="actions">
            <a href="/monitor" class="btn">📊 Monitor interLink Endpoints</a>
        </div>
    </div>
    
    <div class="card">
        <h2>📚 Quick Start Guide</h2>
        <p>New to interLink? Follow these steps to get started:</p>
        
        <div style="background: #f8f9fa; padding: 15px; border-radius: 8px; margin: 15px 0;">
            <h4 style="margin-top: 0; color: var(--secondary-color);">What is interLink?</h4>
            <p style="margin-bottom: 0; font-size: 0.9rem; color: var(--text-secondary);">
                interLink is a CNCF project that provides an abstraction layer for executing Kubernetes pods on remote resources like HPC clusters, batch systems, or cloud providers while maintaining the standard Kubernetes API interface.
            </p>
        </div>
        
        <h4 style="color: var(--secondary-color); margin: 15px 0 10px 0;">Deployment Commands</h4>
        <div style="background: #2c3e50; color: #ecf0f1; padding: 15px; border-radius: 8px; font-family: 'Courier New', monospace; font-size: 0.85rem; margin: 10px 0;">
            <div style="margin-bottom: 10px;"># Deploy Virtual Kubelet with Helm</div>
            <div style="margin-bottom: 10px;">helm upgrade --install --create-namespace \\</div>
            <div style="margin-bottom: 10px;">&nbsp;&nbsp;-n &lt;namespace&gt; &lt;node-name&gt; \\</div>
            <div style="margin-bottom: 10px;">&nbsp;&nbsp;oci://ghcr.io/interlink-hq/interlink-helm-chart/interlink \\</div>
            <div>&nbsp;&nbsp;--values values.yaml</div>
        </div>
        
        <div style="background: #2c3e50; color: #ecf0f1; padding: 15px; border-radius: 8px; font-family: 'Courier New', monospace; font-size: 0.85rem; margin: 10px 0;">
            <div style="margin-bottom: 10px;"># Install interLink on remote server</div>
            <div style="margin-bottom: 10px;">chmod +x interlink-install.sh</div>
            <div style="margin-bottom: 10px;">./interlink-install.sh install</div>
            <div>./interlink-install.sh start</div>
        </div>
    </div>
</body>
</html>`,
		"configure": `<!DOCTYPE html>
<html>
<head>
    <title>interLink WebUI - Configure</title>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <style>
        :root {
            --primary-color: #ff6600;
            --primary-dark: #cc5200;
            --secondary-color: #2c3e50;
            --background: #f8f9fa;
            --surface: #ffffff;
            --text-primary: #2c3e50;
            --text-secondary: #6c757d;
            --border: #dee2e6;
            --success: #28a745;
            --warning: #ffc107;
            --danger: #dc3545;
            --info: #17a2b8;
        }
        
        * { box-sizing: border-box; }
        
        body { 
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; 
            max-width: 1400px; 
            margin: 0 auto; 
            padding: 20px; 
            background: linear-gradient(135deg, var(--background) 0%, #e9ecef 100%);
            min-height: 100vh;
            color: var(--text-primary);
        }
        
        .header { 
            display: flex; 
            justify-content: space-between; 
            align-items: center; 
            margin-bottom: 30px; 
            background: var(--surface);
            padding: 20px;
            border-radius: 12px;
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1);
        }
        
        .header-logo {
            display: flex;
            align-items: center;
        }
        
        .header-logo img {
            height: 40px;
            width: auto;
            max-width: 80px;
            margin-right: 12px;
            object-fit: contain;
        }
        
        .header-logo h1 {
            color: var(--secondary-color);
            font-size: 1.8rem;
            margin: 0;
            font-weight: 300;
        }
        
        .header-actions {
            display: flex;
            align-items: center;
            gap: 10px;
        }
        
        .test-mode-badge {
            background: linear-gradient(45deg, var(--warning), #ffeaa7);
            color: var(--secondary-color);
            padding: 6px 12px;
            border-radius: 20px;
            font-size: 0.85rem;
            font-weight: 500;
            border: 1px solid var(--warning);
        }
        
        .form-group { 
            margin-bottom: 20px; 
        }
        
        .form-group label { 
            display: block; 
            margin-bottom: 8px; 
            font-weight: 600; 
            color: var(--secondary-color);
            font-size: 0.95rem;
        }
        
        .form-group input, .form-group select, .form-group textarea { 
            width: 100%; 
            padding: 12px; 
            border: 2px solid var(--border); 
            border-radius: 8px; 
            font-size: 0.95rem;
            transition: border-color 0.3s ease, box-shadow 0.3s ease;
        }
        
        .form-group input:focus, .form-group select:focus, .form-group textarea:focus {
            outline: none;
            border-color: var(--primary-color);
            box-shadow: 0 0 0 3px rgba(255, 102, 0, 0.1);
        }
        
        .form-group textarea { 
            height: 120px; 
            resize: vertical;
            font-family: 'Courier New', monospace;
        }
        
        .field-help {
            font-size: 0.85rem;
            color: var(--text-secondary);
            margin-top: 5px;
            line-height: 1.4;
        }
        
        .field-help code {
            background: #f8f9fa;
            padding: 2px 6px;
            border-radius: 3px;
            font-family: 'Courier New', monospace;
            color: var(--secondary-color);
        }
        
        .btn { 
            background: var(--primary-color); 
            color: white; 
            padding: 12px 20px; 
            border: none; 
            border-radius: 8px; 
            cursor: pointer;
            font-weight: 500;
            transition: all 0.3s ease;
            font-size: 0.95rem;
        }
        
        .btn:hover { 
            background: var(--primary-dark); 
            transform: translateY(-1px);
            box-shadow: 0 4px 8px rgba(255, 102, 0, 0.3);
        }
        
        .btn-secondary { 
            background: var(--secondary-color); 
        }
        
        .btn-secondary:hover { 
            background: #34495e; 
            box-shadow: 0 4px 8px rgba(44, 62, 80, 0.3);
        }
        
        .section { 
            background: var(--surface); 
            border: 1px solid var(--border); 
            border-radius: 12px; 
            padding: 25px; 
            margin-bottom: 25px;
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.08);
        }
        
        .section h3 { 
            margin-top: 0; 
            margin-bottom: 20px;
            color: var(--secondary-color);
            font-size: 1.3rem;
            font-weight: 600;
            border-bottom: 2px solid var(--primary-color);
            padding-bottom: 10px;
        }
        
        .section-description {
            background: linear-gradient(135deg, #e3f2fd 0%, #f3e5f5 100%);
            padding: 15px;
            border-radius: 8px;
            margin-bottom: 20px;
            border-left: 4px solid var(--info);
        }
        
        .section-description p {
            margin: 0;
            color: var(--text-secondary);
            font-size: 0.9rem;
            line-height: 1.5;
        }
        
        .row { 
            display: flex; 
            gap: 25px; 
        }
        
        .col { 
            flex: 1; 
        }
        
        .dynamic-list { 
            border: 2px solid var(--border); 
            padding: 15px; 
            border-radius: 8px; 
            margin-top: 15px;
            background: #f8f9fa;
        }
        
        .dynamic-item { 
            display: flex; 
            gap: 10px; 
            margin-bottom: 12px; 
            align-items: center; 
        }
        
        .dynamic-item input, .dynamic-item select { 
            flex: 1; 
            margin-bottom: 0;
        }
        
        .btn-small { 
            padding: 8px 12px; 
            font-size: 0.8rem; 
        }
        
        .btn-danger { 
            background: var(--danger); 
        }
        
        .btn-danger:hover { 
            background: #c82333; 
            box-shadow: 0 4px 8px rgba(220, 53, 69, 0.3);
        }
        
        .save-section {
            background: linear-gradient(135deg, var(--success) 0%, #20c997 100%);
            color: white;
            text-align: center;
            padding: 20px;
            border-radius: 12px;
            margin-top: 30px;
        }
        
        .save-section .btn {
            background: white;
            color: var(--success);
            font-weight: 600;
            font-size: 1.1rem;
            padding: 15px 30px;
        }
        
        .save-section .btn:hover {
            background: #f8f9fa;
            transform: translateY(-2px);
            box-shadow: 0 6px 12px rgba(0, 0, 0, 0.2);
        }
        
        @media (max-width: 768px) {
            .row {
                flex-direction: column;
                gap: 0;
            }
            
            .header {
                flex-direction: column;
                gap: 15px;
                text-align: center;
            }
        }
    </style>
</head>
<body>
    <div class="header">
        <div class="header-logo">
            <img src="/static/img/interlink_logo.png" alt="interLink Logo" onerror="this.src='data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iNDAiIGhlaWdodD0iNDAiIHZpZXdCb3g9IjAgMCA0MCA0MCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj48Y2lyY2xlIGN4PSIyMCIgY3k9IjIwIiByPSIyMCIgZmlsbD0iI2ZmNjYwMCIvPjx0ZXh0IHg9IjIwIiB5PSIyNSIgdGV4dC1hbmNob3I9Im1pZGRsZSIgZmlsbD0id2hpdGUiIGZvbnQtZmFtaWx5PSJBcmlhbCIgZm9udC1zaXplPSIxNiIgZm9udC13ZWlnaHQ9ImJvbGQiPmlMPC90ZXh0Pjwvc3ZnPg=='; this.style.width='40px'; this.style.height='40px';">
            <h1>Configure Deployment</h1>
        </div>
        <div class="header-actions">
            {{if .TestMode}}<span class="test-mode-badge">🧪 Test Mode</span>{{end}}
            <a href="/" class="btn btn-secondary">Back to Dashboard</a>
            <a href="/auth/logout" class="btn btn-secondary">Logout</a>
        </div>
    </div>
    
    <form hx-post="/configure" hx-trigger="submit" hx-target="body">
        <div class="section">
            <h3>⚙️ Basic Configuration</h3>
            <div class="section-description">
                <p><strong>Core settings for your interLink deployment.</strong> These parameters define how your Virtual Kubelet node will appear in Kubernetes and where the interLink API server will be deployed.</p>
            </div>
            <div class="row">
                <div class="col">
                    <div class="form-group">
                        <label for="vk_name">Virtual Kubelet Node Name:</label>
                        <input type="text" id="vk_name" name="vk_name" value="{{.ConfigData.VKName}}" required>
                        <div class="field-help">
                            The name that will appear in <code>kubectl get nodes</code>. Must be unique within your cluster.
                            <br><strong>Example:</strong> <code>my-hpc-node</code>, <code>cloud-bursting-node</code>
                        </div>
                    </div>
                    <div class="form-group">
                        <label for="namespace">Kubernetes Namespace:</label>
                        <input type="text" id="namespace" name="namespace" value="{{.ConfigData.Namespace}}" required>
                        <div class="field-help">
                            Kubernetes namespace where the Virtual Kubelet will be deployed. Will be created if it doesn't exist.
                            <br><strong>Default:</strong> <code>interlink</code>
                        </div>
                    </div>
                    <div class="form-group">
                        <label for="interlink_version">interLink Version:</label>
                        <input type="text" id="interlink_version" name="interlink_version" value="{{.ConfigData.InterLinkVersion}}" required>
                    </div>
                </div>
                <div class="col">
                    <div class="form-group">
                        <label for="interlink_ip">interLink IP Address:</label>
                        <input type="text" id="interlink_ip" name="interlink_ip" value="{{.ConfigData.InterLinkIP}}" required>
                    </div>
                    <div class="form-group">
                        <label for="interlink_port">interLink Port:</label>
                        <input type="number" id="interlink_port" name="interlink_port" value="{{.ConfigData.InterLinkPort}}" required>
                    </div>
                </div>
            </div>
        </div>
        
        <div class="section">
            <h3>Resource Limits</h3>
            <div class="row">
                <div class="col">
                    <div class="form-group">
                        <label for="cpu_limit">CPU Cores:</label>
                        <input type="text" id="cpu_limit" name="cpu_limit" value="{{.ConfigData.VKLimits.CPU}}" required>
                    </div>
                </div>
                <div class="col">
                    <div class="form-group">
                        <label for="memory_limit">Memory (GiB):</label>
                        <input type="text" id="memory_limit" name="memory_limit" value="{{.ConfigData.VKLimits.Memory}}" required>
                    </div>
                </div>
                <div class="col">
                    <div class="form-group">
                        <label for="pods_limit">Max Pods:</label>
                        <input type="text" id="pods_limit" name="pods_limit" value="{{.ConfigData.VKLimits.Pods}}" required>
                    </div>
                </div>
            </div>
        </div>
        
        <div class="section">
            <h3>HTTP/TLS Configuration</h3>
            <div class="row">
                <div class="col">
                    <div class="form-group">
                        <label>
                            <input type="checkbox" name="http_insecure" {{if .ConfigData.HTTPInsecure}}checked{{end}}> Allow Insecure HTTP
                        </label>
                    </div>
                    <div class="form-group">
                        <label>
                            <input type="checkbox" name="disable_projected" {{if .ConfigData.DisableProjected}}checked{{end}}> Disable Projected Volumes
                        </label>
                    </div>
                </div>
                <div class="col">
                    <div class="form-group">
                        <label for="ca_cert">CA Certificate (optional):</label>
                        <textarea id="ca_cert" name="ca_cert" placeholder="-----BEGIN CERTIFICATE-----">{{.ConfigData.CACert}}</textarea>
                    </div>
                </div>
            </div>
        </div>
        
        <div class="section">
            <h3>Node Labels</h3>
            <div id="node-labels">
                {{range $key, $value := .ConfigData.NodeLabels}}
                <div class="dynamic-item">
                    <input type="text" name="label_key" value="{{$key}}" placeholder="Key">
                    <input type="text" name="label_value" value="{{$value}}" placeholder="Value">
                    <button type="button" class="btn btn-danger btn-small" onclick="this.parentElement.remove()">Remove</button>
                </div>
                {{end}}
            </div>
            <button type="button" class="btn btn-secondary" onclick="addNodeLabel()">Add Label</button>
        </div>
        
        <div class="section">
            <h3>Node Taints</h3>
            <div id="node-taints">
                {{range .ConfigData.NodeTaints}}
                <div class="dynamic-item">
                    <input type="text" name="taint_key" value="{{.Key}}" placeholder="Key">
                    <input type="text" name="taint_value" value="{{.Value}}" placeholder="Value">
                    <select name="taint_effect">
                        <option value="NoSchedule" {{if eq .Effect "NoSchedule"}}selected{{end}}>NoSchedule</option>
                        <option value="PreferNoSchedule" {{if eq .Effect "PreferNoSchedule"}}selected{{end}}>PreferNoSchedule</option>
                        <option value="NoExecute" {{if eq .Effect "NoExecute"}}selected{{end}}>NoExecute</option>
                    </select>
                    <button type="button" class="btn btn-danger btn-small" onclick="this.parentElement.remove()">Remove</button>
                </div>
                {{end}}
            </div>
            <button type="button" class="btn btn-secondary" onclick="addNodeTaint()">Add Taint</button>
        </div>
        
        <div class="section">
            <h3>OAuth Configuration</h3>
            <div class="row">
                <div class="col">
                    <div class="form-group">
                        <label for="oauth_provider">Provider:</label>
                        <select id="oauth_provider" name="oauth_provider">
                            <option value="">None</option>
                            <option value="oidc" {{if eq .ConfigData.OAUTH.Provider "oidc"}}selected{{end}}>OIDC</option>
                            <option value="github" {{if eq .ConfigData.OAUTH.Provider "github"}}selected{{end}}>GitHub</option>
                        </select>
                    </div>
                    <div class="form-group">
                        <label for="oauth_grant_type">Grant Type:</label>
                        <select id="oauth_grant_type" name="oauth_grant_type">
                            <option value="authorization_code" {{if eq .ConfigData.OAUTH.GrantType "authorization_code"}}selected{{end}}>Authorization Code</option>
                            <option value="client_credentials" {{if eq .ConfigData.OAUTH.GrantType "client_credentials"}}selected{{end}}>Client Credentials</option>
                        </select>
                    </div>
                    <div class="form-group">
                        <label for="oauth_client_id">Client ID:</label>
                        <input type="text" id="oauth_client_id" name="oauth_client_id" value="{{.ConfigData.OAUTH.ClientID}}">
                    </div>
                    <div class="form-group">
                        <label for="oauth_client_secret">Client Secret:</label>
                        <input type="password" id="oauth_client_secret" name="oauth_client_secret" value="{{.ConfigData.OAUTH.ClientSecret}}">
                    </div>
                </div>
                <div class="col">
                    <div class="form-group">
                        <label for="oauth_issuer">Issuer URL:</label>
                        <input type="url" id="oauth_issuer" name="oauth_issuer" value="{{.ConfigData.OAUTH.Issuer}}">
                    </div>
                    <div class="form-group">
                        <label for="oauth_token_url">Token URL:</label>
                        <input type="url" id="oauth_token_url" name="oauth_token_url" value="{{.ConfigData.OAUTH.TokenURL}}">
                    </div>
                    <div class="form-group">
                        <label for="oauth_device_code_url">Device Code URL:</label>
                        <input type="url" id="oauth_device_code_url" name="oauth_device_code_url" value="{{.ConfigData.OAUTH.DeviceCodeURL}}">
                    </div>
                    <div class="form-group">
                        <label for="oauth_audience">Audience:</label>
                        <input type="text" id="oauth_audience" name="oauth_audience" value="{{.ConfigData.OAUTH.Audience}}">
                    </div>
                </div>
            </div>
            <div class="row">
                <div class="col">
                    <div class="form-group">
                        <label for="oauth_group">Required Group:</label>
                        <input type="text" id="oauth_group" name="oauth_group" value="{{.ConfigData.OAUTH.Group}}">
                    </div>
                </div>
                <div class="col">
                    <div class="form-group">
                        <label for="oauth_group_claim">Group Claim:</label>
                        <input type="text" id="oauth_group_claim" name="oauth_group_claim" value="{{.ConfigData.OAUTH.GroupClaim}}" placeholder="groups">
                    </div>
                </div>
            </div>
            <div class="form-group">
                <label for="oauth_scopes">Scopes (comma-separated):</label>
                <input type="text" id="oauth_scopes" name="oauth_scopes" value="{{range $i, $scope := .ConfigData.OAUTH.Scopes}}{{if $i}}, {{end}}{{$scope}}{{end}}" placeholder="openid, email, profile">
            </div>
        </div>
        
        <div class="section">
            <h3>Authentication Mode</h3>
            <div class="form-group">
                <label for="auth_mode">Authentication Method:</label>
                <select id="auth_mode" name="auth_mode" onchange="toggleAuthSections()">
                    <option value="oauth" {{if eq .ConfigData.AuthMode "oauth"}}selected{{end}}>OAuth/OIDC Authentication</option>
                    <option value="mtls" {{if eq .ConfigData.AuthMode "mtls"}}selected{{end}}>mTLS Certificate Authentication</option>
                </select>
                <div class="field-help">
                    Choose between OAuth/OIDC authentication or mutual TLS (mTLS) certificate-based authentication.
                    <br><strong>OAuth:</strong> Uses external identity provider for authentication
                    <br><strong>mTLS:</strong> Uses client certificates for authentication (more secure, no external dependencies)
                </div>
            </div>
            
            <div id="mtls-section" style="display: none;">
                <h4>🔐 mTLS Certificate Configuration</h4>
                <div class="form-group">
                    <input type="checkbox" id="mtls_enabled" name="mtls_enabled" {{if .ConfigData.MTLSEnabled}}checked{{end}}>
                    <label for="mtls_enabled">Enable mTLS and generate certificates automatically</label>
                    <div class="field-help">
                        When enabled, certificates will be automatically generated for the interLink server and client authentication.
                        This provides strong cryptographic authentication without requiring an external identity provider.
                    </div>
                </div>
                
                <div class="mtls-info" style="background: linear-gradient(45deg, #e8f5e8, #d4edda); border: 1px solid #28a745; border-radius: 8px; padding: 15px; margin: 15px 0;">
                    <h5 style="color: #155724; margin-top: 0;">📋 mTLS Certificate Information</h5>
                    <ul style="margin: 10px 0; color: #155724;">
                        <li><strong>CA Certificate:</strong> Root certificate authority for signing</li>
                        <li><strong>Server Certificate:</strong> For interLink API server TLS</li>
                        <li><strong>Client Certificate:</strong> For Virtual Kubelet authentication</li>
                        <li><strong>Automatic Generation:</strong> All certificates generated with 1-year validity</li>
                        <li><strong>Security:</strong> 4096-bit RSA keys with SHA-256 signatures</li>
                    </ul>
                    <p style="margin: 10px 0; color: #155724;"><strong>Note:</strong> Certificates will be included in the generated Helm values and Kubernetes secrets.</p>
                </div>
            </div>
        </div>
        
        <div class="section">
            <h3>Deployment Architecture</h3>
            <div class="form-group">
                <label for="deployment_mode">Deployment Mode:</label>
                <select id="deployment_mode" name="deployment_mode" onchange="toggleDeploymentSections()">
                    <option value="edge-node" {{if eq .ConfigData.DeploymentMode "edge-node"}}selected{{end}}>Edge Node (Remote)</option>
                    <option value="tunneled" {{if eq .ConfigData.DeploymentMode "tunneled"}}selected{{end}}>Tunneled (Local + SSH)</option>
                </select>
                <div class="field-help">
                    Choose your deployment architecture pattern.
                    <br><strong>Edge Node:</strong> All components (VK, API, Plugin) deployed remotely on edge infrastructure (supports OAuth or mTLS)
                    <br><strong>Tunneled:</strong> VK and API local, Plugin remote via SSH tunnel (requires mTLS, most secure)
                </div>
            </div>
            
            <div id="tunneled-section" style="display: none;">
                <h4>🚇 SSH Tunnel Configuration</h4>
                <div style="background: linear-gradient(45deg, #e8f5e8, #d4edda); border: 1px solid #28a745; border-radius: 8px; padding: 10px; margin: 10px 0;">
                    <p style="margin: 0; color: #155724;"><strong>🔐 Security Features:</strong></p>
                    <ul style="margin: 5px 0 0 20px; color: #155724;">
                        <li>Automatically uses mTLS authentication for enhanced security</li>
                        <li>SSH key pair will be generated automatically (4096-bit RSA)</li>
                        <li>All certificates and keys included in generated scripts</li>
                    </ul>
                </div>
                <div class="row">
                    <div class="col">
                        <div class="form-group">
                            <label for="ssh_remote_host">Remote Host:</label>
                            <input type="text" id="ssh_remote_host" name="ssh_remote_host" value="{{.ConfigData.SSHTunnel.RemoteHost}}" placeholder="hpc-cluster.example.com">
                            <div class="field-help">
                                Hostname or IP address of the remote system where the plugin runs.
                            </div>
                        </div>
                        <div class="form-group">
                            <label for="ssh_remote_user">Remote User:</label>
                            <input type="text" id="ssh_remote_user" name="ssh_remote_user" value="{{.ConfigData.SSHTunnel.RemoteUser}}" placeholder="hpc-user">
                            <div class="field-help">
                                Username for SSH authentication on the remote system.
                            </div>
                        </div>
                        <div class="form-group">
                            <label for="ssh_port">SSH Port:</label>
                            <input type="number" id="ssh_port" name="ssh_port" value="{{if .ConfigData.SSHTunnel.SSHPort}}{{.ConfigData.SSHTunnel.SSHPort}}{{else}}22{{end}}" min="1" max="65535">
                            <div class="field-help">
                                SSH server port on the remote system (usually 22).
                            </div>
                        </div>
                    </div>
                    <div class="col">
                        <div class="form-group">
                            <label for="ssh_plugin_port">Plugin Port:</label>
                            <input type="number" id="ssh_plugin_port" name="ssh_plugin_port" value="{{if .ConfigData.SSHTunnel.PluginPort}}{{.ConfigData.SSHTunnel.PluginPort}}{{else}}4000{{end}}" min="1" max="65535">
                            <div class="field-help">
                                Port where the remote plugin listens for connections.
                            </div>
                        </div>
                    </div>
                </div>
                <div class="form-group">
                    <label for="ssh_local_socket">Local Unix Socket Path:</label>
                    <input type="text" id="ssh_local_socket" name="ssh_local_socket" value="{{if .ConfigData.SSHTunnel.LocalSocket}}{{.ConfigData.SSHTunnel.LocalSocket}}{{else}}/tmp/interlink.sock{{end}}">
                    <div class="field-help">
                        Path to local Unix socket for communication between VK and API server.
                    </div>
                </div>
                
                <div class="tunneled-info" style="background: linear-gradient(45deg, #e3f2fd, #bbdefb); border: 1px solid #2196f3; border-radius: 8px; padding: 15px; margin: 15px 0;">
                    <h5 style="color: #0d47a1; margin-top: 0;">🚇 Tunneled Deployment Benefits</h5>
                    <ul style="margin: 10px 0; color: #0d47a1;">
                        <li><strong>NAT/Firewall Friendly:</strong> Only outbound SSH connection required</li>
                        <li><strong>Local Control:</strong> Virtual Kubelet runs in your Kubernetes cluster</li>
                        <li><strong>Secure Communication:</strong> All traffic encrypted through SSH tunnel</li>
                        <li><strong>Flexible Networking:</strong> Works with complex network topologies</li>
                        <li><strong>Plugin Isolation:</strong> Remote plugin runs in target environment</li>
                    </ul>
                </div>
            </div>
        </div>
        
        <div class="section">
            <button type="submit" class="btn">Save Configuration</button>
        </div>
    </form>
    
    <script>
        function addNodeLabel() {
            const container = document.getElementById('node-labels');
            const div = document.createElement('div');
            div.className = 'dynamic-item';
            div.innerHTML = '<input type="text" name="label_key" placeholder="Key">' +
                           '<input type="text" name="label_value" placeholder="Value">' +
                           '<button type="button" class="btn btn-danger btn-small" onclick="this.parentElement.remove()">Remove</button>';
            container.appendChild(div);
        }
        
        function addNodeTaint() {
            const container = document.getElementById('node-taints');
            const div = document.createElement('div');
            div.className = 'dynamic-item';
            div.innerHTML = '<input type="text" name="taint_key" placeholder="Key">' +
                           '<input type="text" name="taint_value" placeholder="Value">' +
                           '<select name="taint_effect">' +
                               '<option value="NoSchedule">NoSchedule</option>' +
                               '<option value="PreferNoSchedule">PreferNoSchedule</option>' +
                               '<option value="NoExecute">NoExecute</option>' +
                           '</select>' +
                           '<button type="button" class="btn btn-danger btn-small" onclick="this.parentElement.remove()">Remove</button>';
            container.appendChild(div);
        }
        
        function toggleAuthSections() {
            const authMode = document.getElementById('auth_mode').value;
            const oauthSection = document.querySelector('.section h3[textContent*="OAuth"]')?.parentElement;
            const mtlsSection = document.getElementById('mtls-section');
            
            if (authMode === 'mtls') {
                if (mtlsSection) mtlsSection.style.display = 'block';
                // Find OAuth section by looking for the heading
                const sections = document.querySelectorAll('.section');
                sections.forEach(section => {
                    const heading = section.querySelector('h3');
                    if (heading && heading.textContent.includes('OAuth Configuration')) {
                        section.style.display = 'none';
                    }
                });
            } else {
                if (mtlsSection) mtlsSection.style.display = 'none';
                const sections = document.querySelectorAll('.section');
                sections.forEach(section => {
                    const heading = section.querySelector('h3');
                    if (heading && heading.textContent.includes('OAuth Configuration')) {
                        section.style.display = 'block';
                    }
                });
            }
        }
        
        function toggleDeploymentSections() {
            const deploymentMode = document.getElementById('deployment_mode').value;
            const tunneledSection = document.getElementById('tunneled-section');
            const authModeSelect = document.getElementById('auth_mode');
            
            if (deploymentMode === 'tunneled') {
                if (tunneledSection) tunneledSection.style.display = 'block';
                // Force mTLS for tunneled deployments
                if (authModeSelect) {
                    authModeSelect.value = 'mtls';
                    authModeSelect.disabled = true;
                }
                // Enable mTLS checkbox
                const mtlsCheckbox = document.getElementById('mtls_enabled');
                if (mtlsCheckbox) {
                    mtlsCheckbox.checked = true;
                }
            } else {
                if (tunneledSection) tunneledSection.style.display = 'none';
                // Re-enable auth mode selection for edge-node deployments
                if (authModeSelect) {
                    authModeSelect.disabled = false;
                }
            }
            
            // Update auth sections after changing auth mode
            toggleAuthSections();
        }
        
        // Initialize sections on page load
        document.addEventListener('DOMContentLoaded', function() {
            toggleAuthSections();
            toggleDeploymentSections();
        });
    </script>
</body>
</html>`,
		"file-view": `<!DOCTYPE html>
<html>
<head>
    <title>{{.Title}} - interLink WebUI</title>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <style>
        body { font-family: Arial, sans-serif; max-width: 1200px; margin: 0 auto; padding: 20px; background: #f5f5f5; }
        .header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 30px; }
        .btn { background: #007bff; color: white; padding: 10px 20px; text-decoration: none; border: none; border-radius: 5px; cursor: pointer; margin: 0 5px; }
        .btn:hover { background: #0056b3; }
        .btn-secondary { background: #6c757d; }
        .btn-secondary:hover { background: #545b62; }
        .btn-success { background: #28a745; }
        .btn-success:hover { background: #218838; }
        .content-container { background: white; border-radius: 8px; padding: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        .file-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; padding-bottom: 15px; border-bottom: 2px solid #e9ecef; }
        .file-title { color: #333; margin: 0; }
        .file-actions { display: flex; gap: 10px; }
        .code-container { position: relative; }
        .code-block { background: #f8f9fa; border: 1px solid #e9ecef; border-radius: 4px; padding: 15px; font-family: 'Courier New', monospace; font-size: 14px; line-height: 1.4; white-space: pre-wrap; overflow-x: auto; margin: 0; }
        .copy-btn { position: absolute; top: 10px; right: 10px; }
        .copy-success { background: #d4edda !important; color: #155724 !important; }
        .filename { background: #e9ecef; padding: 5px 10px; border-radius: 3px; font-family: monospace; font-size: 12px; color: #495057; margin-bottom: 10px; display: inline-block; }
    </style>
</head>
<body>
    <div class="header">
        <h1>{{.Title}}</h1>
        <div>
            {{if .TestMode}}<span style="background: #fff3cd; padding: 5px 10px; border-radius: 3px; margin-right: 10px;">🧪 Test Mode</span>{{end}}
            <a href="/" class="btn btn-secondary">Back to Dashboard</a>
            <a href="/auth/logout" class="btn btn-secondary">Logout</a>
        </div>
    </div>
    
    <div class="content-container">
        <div class="file-header">
            <h2 class="file-title">Generated File Content</h2>
            <div class="file-actions">
                <a href="/generate/{{if eq .Language "yaml"}}helm{{else}}script{{end}}" class="btn btn-success" download="{{.Filename}}">📥 Download File</a>
                <button id="copyBtn" class="btn btn-secondary" onclick="copyToClipboard()">📋 Copy to Clipboard</button>
            </div>
        </div>
        
        <div class="filename">{{.Filename}}</div>
        
        <div class="code-container">
            <pre class="code-block" id="codeContent">{{.Content}}</pre>
        </div>
    </div>
    
    <script>
        function copyToClipboard() {
            const content = document.getElementById('codeContent').textContent;
            const copyBtn = document.getElementById('copyBtn');
            
            navigator.clipboard.writeText(content).then(function() {
                const originalText = copyBtn.innerHTML;
                copyBtn.innerHTML = '✅ Copied!';
                copyBtn.classList.add('copy-success');
                
                setTimeout(function() {
                    copyBtn.innerHTML = originalText;
                    copyBtn.classList.remove('copy-success');
                }, 2000);
            }).catch(function(err) {
                console.error('Failed to copy: ', err);
                // Fallback for older browsers
                const textArea = document.createElement('textarea');
                textArea.value = content;
                document.body.appendChild(textArea);
                textArea.select();
                document.execCommand('copy');
                document.body.removeChild(textArea);
                
                const originalText = copyBtn.innerHTML;
                copyBtn.innerHTML = '✅ Copied!';
                copyBtn.classList.add('copy-success');
                
                setTimeout(function() {
                    copyBtn.innerHTML = originalText;
                    copyBtn.classList.remove('copy-success');
                }, 2000);
            });
        }
    </script>
</body>
</html>`,
		"monitor": `<!DOCTYPE html>
<html>
<head>
    <title>interLink Monitor - WebUI</title>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <style>
        :root {
            --primary-color: #ff6600;
            --primary-dark: #cc5200;
            --secondary-color: #2c3e50;
            --background: #f8f9fa;
            --surface: #ffffff;
            --text-primary: #2c3e50;
            --text-secondary: #6c757d;
            --border: #dee2e6;
            --success: #28a745;
            --warning: #ffc107;
            --danger: #dc3545;
            --info: #17a2b8;
        }
        
        * { box-sizing: border-box; }
        
        body { 
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; 
            max-width: 1200px; 
            margin: 0 auto; 
            padding: 20px; 
            background: linear-gradient(135deg, var(--background) 0%, #e9ecef 100%);
            min-height: 100vh;
            color: var(--text-primary);
        }
        
        .header { 
            display: flex; 
            justify-content: space-between; 
            align-items: center; 
            margin-bottom: 30px; 
            background: var(--surface);
            padding: 20px;
            border-radius: 12px;
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1);
        }
        
        .header-logo {
            display: flex;
            align-items: center;
        }
        
        .header-logo img {
            height: 40px;
            width: auto;
            max-width: 80px;
            margin-right: 12px;
            object-fit: contain;
        }
        
        .header-logo h1 {
            color: var(--secondary-color);
            font-size: 1.8rem;
            margin: 0;
            font-weight: 300;
        }
        
        .header-actions {
            display: flex;
            align-items: center;
            gap: 10px;
        }
        
        .test-mode-badge {
            background: linear-gradient(45deg, var(--warning), #ffeaa7);
            color: var(--secondary-color);
            padding: 6px 12px;
            border-radius: 20px;
            font-size: 0.85rem;
            font-weight: 500;
            border: 1px solid var(--warning);
        }
        
        .btn { 
            background: var(--primary-color); 
            color: white; 
            padding: 12px 20px; 
            text-decoration: none; 
            border-radius: 8px; 
            border: none; 
            cursor: pointer;
            font-weight: 500;
            transition: all 0.3s ease;
            display: inline-flex;
            align-items: center;
            gap: 8px;
        }
        
        .btn:hover { 
            background: var(--primary-dark); 
            transform: translateY(-1px);
            box-shadow: 0 4px 8px rgba(255, 102, 0, 0.3);
        }
        
        .btn-secondary { 
            background: var(--secondary-color); 
        }
        
        .btn-secondary:hover { 
            background: #34495e; 
            box-shadow: 0 4px 8px rgba(44, 62, 80, 0.3);
        }
        
        .card { 
            background: var(--surface); 
            border: 1px solid var(--border); 
            border-radius: 12px; 
            padding: 25px; 
            margin-bottom: 25px;
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.08);
        }
        
        .card h2 {
            color: var(--secondary-color);
            margin-top: 0;
            margin-bottom: 12px;
            font-size: 1.4rem;
            font-weight: 600;
        }
        
        .form-group { 
            margin-bottom: 20px; 
        }
        
        .form-group label { 
            display: block; 
            margin-bottom: 8px; 
            font-weight: 600; 
            color: var(--secondary-color);
            font-size: 0.95rem;
        }
        
        .form-group input { 
            width: 100%; 
            padding: 12px; 
            border: 2px solid var(--border); 
            border-radius: 8px; 
            font-size: 0.95rem;
            transition: border-color 0.3s ease, box-shadow 0.3s ease;
        }
        
        .form-group input:focus {
            outline: none;
            border-color: var(--primary-color);
            box-shadow: 0 0 0 3px rgba(255, 102, 0, 0.1);
        }
        
        .field-help {
            font-size: 0.85rem;
            color: var(--text-secondary);
            margin-top: 5px;
            line-height: 1.4;
        }
        
        .field-help code {
            background: #f8f9fa;
            padding: 2px 6px;
            border-radius: 3px;
            font-family: 'Courier New', monospace;
            color: var(--secondary-color);
        }
        
        .ping-form {
            display: flex;
            gap: 10px;
            align-items: end;
        }
        
        .ping-form .form-group {
            flex: 1;
            margin-bottom: 0;
        }
        
        .result-container {
            margin-top: 20px;
            padding: 15px;
            border-radius: 8px;
            display: none;
        }
        
        .result-success {
            background: linear-gradient(135deg, #d4edda, #c3e6cb);
            border: 1px solid var(--success);
            color: #155724;
        }
        
        .result-warning {
            background: linear-gradient(135deg, #fff3cd, #ffeaa7);
            border: 1px solid var(--warning);
            color: #856404;
        }
        
        .result-error {
            background: linear-gradient(135deg, #f8d7da, #f5c6cb);
            border: 1px solid var(--danger);
            color: #721c24;
        }
        
        .result-item {
            display: flex;
            justify-content: space-between;
            margin-bottom: 8px;
        }
        
        .result-item:last-child {
            margin-bottom: 0;
        }
        
        .result-label {
            font-weight: 600;
        }
        
        .spinner {
            border: 3px solid #f3f3f3;
            border-top: 3px solid var(--primary-color);
            border-radius: 50%;
            width: 20px;
            height: 20px;
            animation: spin 1s linear infinite;
            display: none;
            margin-left: 10px;
        }
        
        @keyframes spin {
            0% { transform: rotate(0deg); }
            100% { transform: rotate(360deg); }
        }
        
        .usage-info {
            background: linear-gradient(135deg, #e3f2fd 0%, #f3e5f5 100%);
            padding: 20px;
            border-radius: 12px;
            margin-bottom: 25px;
            border-left: 4px solid var(--info);
        }
        
        .usage-info h3 {
            color: var(--secondary-color);
            margin-top: 0;
            margin-bottom: 15px;
        }
        
        .usage-info p {
            margin: 10px 0;
            color: var(--text-secondary);
        }
        
        .example-urls {
            background: #f8f9fa;
            padding: 15px;
            border-radius: 8px;
            margin: 15px 0;
        }
        
        .example-urls code {
            display: block;
            background: #2c3e50;
            color: #ecf0f1;
            padding: 8px 12px;
            border-radius: 4px;
            margin: 5px 0;
            font-family: 'Courier New', monospace;
        }
    </style>
</head>
<body>
    <div class="header">
        <div class="header-logo">
            <img src="/static/img/interlink_logo.png" alt="interLink Logo" onerror="this.src='data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iNDAiIGhlaWdodD0iNDAiIHZpZXdCb3g9IjAgMCA0MCA0MCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj48Y2lyY2xlIGN4PSIyMCIgY3k9IjIwIiByPSIyMCIgZmlsbD0iI2ZmNjYwMCIvPjx0ZXh0IHg9IjIwIiB5PSIyNSIgdGV4dC1hbmNob3I9Im1pZGRsZSIgZmlsbD0id2hpdGUiIGZvbnQtZmFtaWx5PSJBcmlhbCIgZm9udC1zaXplPSIxNiIgZm9udC13ZWlnaHQ9ImJvbGQiPmlMPC90ZXh0Pjwvc3ZnPg=='; this.style.width='40px'; this.style.height='40px';">
            <h1>Monitor interLink</h1>
        </div>
        <div class="header-actions">
            {{if .TestMode}}<span class="test-mode-badge">🧪 Test Mode</span>{{end}}
            <a href="/" class="btn btn-secondary">Back to Dashboard</a>
            <a href="/auth/logout" class="btn btn-secondary">Logout</a>
        </div>
    </div>
    
    <div class="usage-info">
        <h3>🔍 Endpoint Monitoring</h3>
        <p>Monitor your deployed interLink API endpoints to verify they are running correctly. This tool will ping the <code>/pinglink</code> endpoint and show you the response status.</p>
        
        <div class="example-urls">
            <strong>Example URLs:</strong>
            <code>https://your-interlink-server.com:8080</code>
            <code>http://192.168.1.100:8080</code>
            <code>https://interlink.example.com/api</code>
        </div>
        
        <p><strong>Note:</strong> The URL will automatically append <code>/pinglink</code> if not already present.</p>
    </div>
    
    <div class="card">
        <h2>🔗 Test interLink Endpoint</h2>
        <p>Enter the base URL of your interLink API server to test connectivity and verify it's responding correctly.</p>
        
        <form id="pingForm" hx-post="/pingLink" hx-target="#result" hx-indicator="#spinner">
            <div class="ping-form">
                <div class="form-group">
                    <label for="endpoint_url">interLink API Endpoint URL:</label>
                    <input type="url" id="endpoint_url" name="endpoint_url" 
                           placeholder="https://your-interlink-server.com:8080" 
                           value="{{if .ConfigData.InterLinkIP}}{{if eq .ConfigData.InterLinkPort 0}}https://{{.ConfigData.InterLinkIP}}{{else}}https://{{.ConfigData.InterLinkIP}}:{{.ConfigData.InterLinkPort}}{{end}}{{end}}"
                           required>
                    <div class="field-help">
                        Full URL to your interLink API server including protocol and port.
                        <br><strong>Will test:</strong> <code id="ping-url-preview">URL/pinglink</code>
                    </div>
                </div>
                
                <div class="form-group">
                    <label for="auth_token">OIDC Bearer Token (Optional):</label>
                    <input type="password" id="auth_token" name="auth_token" 
                           placeholder="Leave empty to use session token if available">
                    <div class="field-help">
                        Bearer token for authorization. If empty, will try to use your current session token.
                        {{if .Tokens}}
                        <br><span style="color: var(--success);">✅ Session token available</span>
                        {{else}}
                        <br><span style="color: var(--warning);">⚠️ No session token - manual token required</span>
                        {{end}}
                    </div>
                </div>
                <button type="submit" class="btn">
                    🔍 Test Connection
                    <div id="spinner" class="spinner"></div>
                </button>
            </div>
        </form>
        
        <div id="result" class="result-container"></div>
    </div>
    
    <div class="card">
        <h2>📊 How to Use</h2>
        <ol>
            <li><strong>Deploy interLink:</strong> Use the configuration and generated files to deploy your interLink setup</li>
            <li><strong>Get the URL:</strong> Note the IP address and port where your interLink API is running</li>
            <li><strong>Test Connection:</strong> Enter the URL above and click "Test Connection"</li>
            <li><strong>Verify Status:</strong> Check that you get a successful response</li>
        </ol>
        
        <h3>Expected Responses</h3>
        <ul>
            <li><strong>✅ Success:</strong> HTTP 200 - interLink is running and responding correctly</li>
            <li><strong>⚠️ Warning:</strong> HTTP 4xx/5xx - interLink is reachable but may have configuration issues</li>
            <li><strong>❌ Error:</strong> Connection failed - Check network, firewall, or if interLink is running</li>
        </ul>
    </div>
    
    <script>
        // Update ping URL preview
        document.getElementById('endpoint_url').addEventListener('input', function() {
            const url = this.value;
            const preview = document.getElementById('ping-url-preview');
            if (url) {
                try {
                    const parsedUrl = new URL(url);
                    const pingUrl = parsedUrl.origin + parsedUrl.pathname.replace(/\/$/, '') + '/ping';
                    preview.textContent = pingUrl;
                } catch (e) {
                    preview.textContent = url + '/ping';
                }
            } else {
                preview.textContent = 'URL/ping';
            }
        });
        
        // Handle ping response
        document.body.addEventListener('htmx:afterRequest', function(evt) {
            if (evt.detail.elt.id === 'pingForm') {
                const result = document.getElementById('result');
                result.style.display = 'block';
                
                if (evt.detail.xhr.status === 200) {
                    try {
                        const data = JSON.parse(evt.detail.xhr.responseText);
                        let className = 'result-' + data.status;
                        let icon = data.status === 'success' ? '✅' : 
                                  data.status === 'warning' ? '⚠️' : '❌';
                        
                        result.className = 'result-container ' + className;
                        result.innerHTML = '<h3>' + icon + ' Ping Result</h3>' +
                            '<div class="result-item"><span class="result-label">URL:</span><span>' + data.url + '</span></div>' +
                            '<div class="result-item"><span class="result-label">Status:</span><span>' + data.status.toUpperCase() + '</span></div>' +
                            '<div class="result-item"><span class="result-label">HTTP Code:</span><span>' + data.status_code + '</span></div>' +
                            '<div class="result-item"><span class="result-label">Response Time:</span><span>' + data.duration + '</span></div>' +
                            '<div class="result-item"><span class="result-label">Message:</span><span>' + data.message + '</span></div>' +
                            '<div class="result-item"><span class="result-label">Timestamp:</span><span>' + new Date(data.timestamp).toLocaleString() + '</span></div>';
                    } catch (e) {
                        result.className = 'result-container result-error';
                        result.innerHTML = '<h3>❌ Error</h3><p>Failed to parse response</p>';
                    }
                } else {
                    result.className = 'result-container result-error';
                    result.innerHTML = '<h3>❌ Request Failed</h3><p>' + evt.detail.xhr.responseText + '</p>';
                }
            }
        });
        
        // Trigger initial URL preview update
        document.getElementById('endpoint_url').dispatchEvent(new Event('input'));
    </script>
</body>
</html>`,
	}

	tmpl := template.Must(template.New(templateName).Parse(templates[templateName]))
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		log.Printf("Template error: %v", err)
	}
}

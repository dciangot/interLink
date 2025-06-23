# interLink WebUI

A web-based user interface for configuring and deploying interLink with OIDC authentication.

## Features

- **OIDC Authentication**: Secure login using OpenID Connect providers
- **Interactive Configuration**: Web-based form for configuring interLink deployments
- **Dynamic Generation**: Real-time generation of Helm values and installation scripts
- **HTMX Integration**: Responsive UI with minimal JavaScript
- **Resource Management**: Configure node limits, labels, and taints
- **OAuth Integration**: Support for various OAuth providers (OIDC, GitHub)

## Quick Start

1. **Configuration**: Copy and modify the example configuration:
   ```bash
   cp webui-config.yaml.example webui-config.yaml
   # Edit webui-config.yaml with your OIDC provider details
   ```

2. **Build**: Build the webui binary:
   ```bash
   go build -o webui ./cmd/webui
   ```

3. **Run**: Start the web server:
   ```bash
   ./webui
   ```

4. **Access**: Open your browser to `http://localhost:8080`

## Test Mode (Development)

For development and testing, you can run the WebUI without OIDC configuration:

```bash
# Use the test configuration
cp webui-config-test.yaml webui-config.yaml

# Or set test mode in your config
echo "test_mode: true" >> webui-config.yaml

# Run the webui
./webui
```

In test mode:
- No OIDC provider configuration required
- Login page shows a "Test Login" button
- Dummy user credentials are automatically created
- Test mode indicator appears in the UI

## Configuration

The WebUI requires a YAML configuration file. Set the `WEBUI_CONFIG` environment variable to specify a custom path, or place `webui-config.yaml` in the current directory.

### Configuration Options

```yaml
server:
  host: "0.0.0.0"          # Server bind address
  port: 8080               # Server port

# Set to true to enable test mode (no OIDC required)
test_mode: false           # Enable dummy authentication for testing

oidc:
  client_id: "..."         # OIDC client ID
  client_secret: "..."     # OIDC client secret
  redirect_url: "..."      # OAuth redirect URL
  issuer: "..."            # OIDC issuer URL
  scopes:                  # OAuth scopes
    - "openid"
    - "profile"
    - "email"
```

### OIDC Provider Setup

Configure your OIDC provider with:
- **Redirect URI**: `http://your-domain:8080/auth/callback`
- **Scopes**: `openid`, `profile`, `email`, `offline_access`
- **Grant Types**: `authorization_code`

## Usage

1. **Login**: Access the WebUI and authenticate via OIDC
2. **Configure**: Fill out the deployment configuration form:
   - Basic settings (node name, namespace, IP/port)
   - Resource limits (CPU, memory, pods)
   - HTTP/TLS settings
   - Node labels and taints
   - OAuth configuration for interLink
3. **Generate**: Download generated files:
   - `values.yaml`: Helm chart values for Kubernetes deployment
   - `interlink-install.sh`: Installation script for remote servers

## Deployment Workflow

1. **Configure** your deployment using the WebUI
2. **Download** the generated `values.yaml` file
3. **Deploy** Virtual Kubelet using Helm:
   ```bash
   helm upgrade --install --create-namespace -n <namespace> <node-name> \
     oci://ghcr.io/interlink-hq/interlink-helm-chart/interlink \
     --values values.yaml
   ```
4. **Install** interLink API on remote server:
   ```bash
   # Copy and run the generated script on your remote server
   ./interlink-install.sh install
   ./interlink-install.sh start
   ```

## Architecture

The WebUI provides the same functionality as the CLI installer but through a web interface:

- **Authentication**: OIDC-based user authentication
- **Session Management**: Secure session handling with cookies
- **Template Generation**: Go templates for Helm values and install scripts
- **HTMX Integration**: Dynamic form interactions without page reloads
- **Configuration Persistence**: Session-based configuration storage

## Security

- Sessions are stored in memory (not persistent across restarts)
- OIDC tokens are used for authentication
- No sensitive data is logged or stored permanently
- HTTPS recommended for production deployments

## Development

### Dependencies

The WebUI uses standard Go libraries and:
- `gorilla/mux` for HTTP routing
- `golang.org/x/oauth2` for OIDC authentication
- `gopkg.in/yaml.v3` for YAML handling
- HTMX (loaded via CDN) for dynamic interactions

### File Structure

```
cmd/webui/
├── main.go                    # Main application
├── webui-config.yaml.example  # Example configuration
└── README.md                  # This file
```

### Building

```bash
# Build for current platform
go build -o webui ./cmd/webui

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -o webui-linux ./cmd/webui
```
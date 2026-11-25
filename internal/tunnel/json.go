package tunnel

import "github.com/devXpro/homie/internal/wire"

// MCPPath is the hub's one MCP endpoint, the whole home (core.SingleResource{At: "/mcp", ID: "home"}).
const MCPPath = "/mcp"

// StatusJSON is TunnelStatus as /api/home/tunnel and the first-run page send it.
type StatusJSON struct {
	Mode      string `json:"mode"`
	State     string `json:"state"`
	URL       string `json:"url,omitempty"`
	MCPURL    string `json:"mcpUrl,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Since     string `json:"since,omitempty"`
	LastError string `json:"lastError,omitempty"`
}

// JSON is s on the wire.
func (s Status) JSON() StatusJSON {
	out := StatusJSON{Mode: s.Mode, State: s.State, URL: s.URL, Hostname: s.Hostname, Since: wire.OptTime(s.Since),
		LastError: s.LastError}
	if s.URL != "" {
		out.MCPURL = s.URL + MCPPath
	}
	return out
}

// ConnectJSON is ConnectJob as the first-run page sends it.
type ConnectJSON struct {
	Hostname  string `json:"hostname"`
	State     string `json:"state"`
	Step      string `json:"step"`
	LoginURL  string `json:"loginUrl,omitempty"`
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
}

// JSON is j on the wire.
func (j ConnectJob) JSON() ConnectJSON {
	return ConnectJSON{Hostname: j.Hostname, State: j.State, Step: j.Step, LoginURL: j.LoginURL, Error: j.Error,
		StartedAt: wire.Time(j.StartedAt), EndedAt: wire.OptTime(j.EndedAt)}
}

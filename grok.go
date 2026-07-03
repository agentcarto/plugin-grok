// Package grok implements an agentcarto plugin that reads, scans, and forks
// Grok CLI sessions stored on disk.
package grok

import (
	"fmt"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
	"github.com/agentcarto/core/plugin"
	"gopkg.in/yaml.v3"
)

// Options holds the plugin configuration decoded from the host YAML node.
type Options struct {
	SessionsDir           string `yaml:"sessions_dir"`
	ActiveSessionsFile    string `yaml:"active_sessions_file"`
	Executable            string `yaml:"executable"`
	UserEventMeansRunning bool   `yaml:"user_event_means_running"`
}

// Factory is the exported entry point the host uses to construct the plugin.
type Factory struct{}

func (Factory) Descriptor() plugin.Descriptor {
	// ParserVersion=5: user events now carry the normalized Prompt field
	// (agent-specific pseudo-prompt vocabulary moved out of core).
	// ParserVersion=6: tool calls carry ToolArg (rendering moved out of the host).
	return plugin.Descriptor{Type: "grok", DisplayName: "Grok", ParserVersion: "6", Capabilities: domain.Capabilities{Scan: true, Conversation: true, Active: true, Resume: true, Rewind: true, Relocate: true}}
}

func (Factory) New(id string, n *yaml.Node) (any, error) {
	o := Options{SessionsDir: "~/.grok/sessions", ActiveSessionsFile: "~/.grok/active_sessions.json", Executable: "grok"}
	if e := common.DecodeOptions(n, &o); e != nil {
		return nil, e
	}
	o.SessionsDir = common.ExpandHome(o.SessionsDir)
	o.ActiveSessionsFile = common.ExpandHome(o.ActiveSessionsFile)
	return &Plugin{id, o}, nil
}

// Plugin is the runtime instance bound to a single configured Grok install.
type Plugin struct {
	id string
	o  Options
}

func (p *Plugin) Executable() string { return p.o.Executable }

func (p *Plugin) ResumeCommand(s domain.Session) (domain.Command, error) {
	if s.Status != "" {
		return domain.Command{}, fmt.Errorf("session is active")
	}
	return domain.Command{Executable: p.o.Executable, Args: []string{"--resume", s.SessionID}, WorkingDirectory: s.CWD}, nil
}

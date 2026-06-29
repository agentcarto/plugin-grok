// Command agentcarto-plugin-grok serves the AgentCarto Grok plugin as a subprocess.
package main

import (
	"github.com/agentcarto/core/plugin"
	"github.com/agentcarto/plugin-grok"
)

func main() {
	plugin.Serve(grok.Factory{})
}

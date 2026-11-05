package claude

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// agentServers gates an agent whose frontmatter defines MCP servers inline (under any key equal
// to "mcpServers" ignoring case): Claude Code starts them when the agent starts. A string item
// names a server the user already configured and needs no consent. The servers are read from
// text, the agent as rendered, so consent covers what Claude Code reads; they join consent,
// the agent's consent for its frontmatter hooks, or start one for settings. ok is false when
// the agent is skipped.
func agentServers(id, name string, doc module.Doc, text string, consent *plan.HookConsent, ctx target.PlanContext, settings string) (*plan.HookConsent, []target.Finding, bool, error) {
	isServers := func(k string) bool { return strings.EqualFold(k, "mcpServers") }
	if !slices.ContainsFunc(slices.Collect(maps.Keys(doc.Frontmatter)), isServers) {
		return consent, nil, true, nil
	}
	rendered, err := module.ParseDoc([]byte(text))
	if err != nil {
		sk := newSkipper(ctx, target.Finding{Target: id, Kind: "agent", Artifact: name})
		sk.skip("frontmatter-invalid", "the frontmatter does not parse once ${PLUGIN_ROOT} and ${PLUGIN_DATA} are expanded: "+
			untrusted.Text(err.Error(), untrusted.MaxName), "quote frontmatter values that hold these variables", false)
		return nil, sk.finds, false, nil
	}
	var servers []any
	for _, k := range slices.Sorted(maps.Keys(rendered.Frontmatter)) {
		if !isServers(k) {
			continue
		}
		items, isList := rendered.Frontmatter[k].([]any)
		if !isList {
			// Loading refuses this; gating whatever the rendering produced fails closed.
			items = []any{rendered.Frontmatter[k]}
		}
		for _, item := range items {
			if _, ref := item.(string); !ref {
				servers = append(servers, item)
			}
		}
	}
	if len(servers) == 0 {
		return consent, nil, true, nil
	}
	raw, err := json.Marshal(servers)
	if err != nil {
		return nil, nil, false, fmt.Errorf("agent %s: frontmatter mcpServers: %w", name, err)
	}
	if consent == nil {
		consent = &plan.HookConsent{Settings: settings, Kind: "agent", Name: name}
	}
	consent.MCPServers = raw
	return consent, nil, true, nil
}

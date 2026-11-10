package mech

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// servers sets m's MCP servers as members of the JSON object at env.Entry.Pointer in
// env.Entry.File, each in the entry's shape. The file is resolved only when a server will be
// written, so a module without servers, or whose servers are all refused, never reads the
// filesystem.
func servers(env Env, m *module.Module) ([]plan.Intent, []target.Finding, error) {
	var out []plan.Intent
	var finds []target.Finding
	file := ""
	for _, s := range m.MCPServers {
		if !module.Supports(s.Targets, env.ID) {
			continue
		}
		_, f, ok := target.Admit(env.ID, env.Caps, env.Ctx, "mcp", s.Name, module.Doc{})
		if finds = append(finds, f...); !ok {
			continue
		}
		var v map[string]any
		switch env.Entry.Shape {
		case "claude":
			var sf []target.Finding
			v, sf = claudeMCP(env.ID, s, env.Ctx)
			finds = append(finds, sf...)
		case "gemini":
			v = geminiServer(s, env.Ctx)
		case "opencode":
			var sf []target.Finding
			v, sf = opencodeServer(env.ID, s, env.Ctx)
			finds = append(finds, sf...)
		default:
			return nil, nil, fmt.Errorf("%s: no server shape %s", env.ID, env.Entry.Shape)
		}
		value, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		if file == "" {
			if file, err = Resolve(env.ID, env.Entry.File, env.Ctx.Root); err != nil {
				return nil, nil, err
			}
		}
		out = append(out, plan.Intent{Target: env.ID, Kind: "mcp", Op: plan.OpSetJSON, Path: file, Pointer: env.Entry.Pointer + "/" + s.Name, Value: value})
	}
	return out, finds, nil
}

// geminiServer renders one server in Gemini's settings.json shape: stdio {command,args,env,cwd};
// http {httpUrl,headers}; sse {url,headers}.
func geminiServer(s module.MCPServer, ctx target.PlanContext) map[string]any {
	v := map[string]any{}
	switch s.Transport {
	case module.TransportStdio:
		v["command"] = target.Expand(s.Command, ctx)
		if len(s.Args) > 0 {
			v["args"] = target.ExpandAll(s.Args, ctx)
		}
		if len(s.Env) > 0 {
			v["env"] = target.ExpandMap(s.Env, ctx)
		}
		if s.Cwd != "" {
			v["cwd"] = target.Expand(s.Cwd, ctx)
		}
		return v
	case module.TransportHTTP:
		v["httpUrl"] = target.Expand(s.URL, ctx)
	default:
		v["url"] = target.Expand(s.URL, ctx)
	}
	if len(s.Headers) > 0 {
		v["headers"] = target.ExpandMap(s.Headers, ctx)
	}
	return v
}

// envRef is a ${VAR} or ${VAR:-default} environment reference.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-[^}]*)?\}`)

// opencodeServer renders one server in opencode.json's shape: local
// {type:"local",command:[cmd,…args],environment}; remote {type:"remote",url,headers}.
// ${VAR} becomes {env:VAR}; ${VAR:-default} loses its default.
func opencodeServer(id string, s module.MCPServer, ctx target.PlanContext) (map[string]any, []target.Finding) {
	var finds []target.Finding
	at := target.Finding{Target: id, Kind: "mcp", Artifact: s.Name, Class: target.ClassDetail}
	env := func(v string) string {
		return envRef.ReplaceAllStringFunc(target.Expand(v, ctx), func(ref string) string {
			sub := envRef.FindStringSubmatch(ref)
			to := "{env:" + sub[1] + "}"
			f := at
			if sub[2] == "" {
				f.Severity, f.Code, f.Text = target.SeverityInfo, id+"/env-reference", ref+" becomes "+to
			} else {
				f.Severity, f.Code = target.SeverityWarn, id+"/env-default-dropped"
				f.Text = fmt.Sprintf("%s has no defaults for environment references; %s becomes %s, dropping %q", id, ref, to, sub[2][2:])
				f.Hint = "set " + sub[1] + " in the environment " + id + " runs in"
			}
			finds = append(finds, f)
			return to
		})
	}
	envMap := func(in map[string]string) map[string]string {
		out := make(map[string]string, len(in))
		for _, k := range slices.Sorted(maps.Keys(in)) {
			out[k] = env(in[k])
		}
		return out
	}
	v := map[string]any{}
	if s.Transport == module.TransportStdio {
		command := []string{env(s.Command)}
		for _, a := range s.Args {
			command = append(command, env(a))
		}
		v["type"], v["command"] = "local", command
		if len(s.Env) > 0 {
			v["environment"] = envMap(s.Env)
		}
		if s.Cwd != "" {
			f := at
			f.Severity, f.Code, f.Text = target.SeverityWarn, "mcp-cwd-dropped", fmt.Sprintf("%s has no working-directory setting; cwd %q dropped", id, s.Cwd)
			finds = append(finds, f)
		}
		return v, finds
	}
	v["type"], v["url"] = "remote", env(s.URL)
	if len(s.Headers) > 0 {
		v["headers"] = envMap(s.Headers)
	}
	return v, finds
}

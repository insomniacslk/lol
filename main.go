package main

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

var (
	flagListen  = pflag.StringP("listen", "l", "localhost:8182", "Listen host:port")
	flagConfig  = pflag.StringP("config", "c", "", "Path to config file")
	flagBaseURL = pflag.StringP("base-url", "u", "", "Base URL for search site, e.g. https://example.org")
	flagProfile = pflag.StringP("profile", "p", "", "Profile to serve (empty = common commands only)")
)

//go:embed command_list.template
var cmdDirectoryTemplate string

//go:embed lol.png
var iconBytes []byte

var opensearchTemplate = `<OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/"
      xmlns:moz="http://www.mozilla.org/2006/browser/search/">
  <ShortName>LOL{{ if .Profile }} ({{ .Profile }}){{ end }}</ShortName>
  <Description>LOL shortcuts{{ if .Profile }} ({{ .Profile }} profile){{ end }}</Description>
  <Image width="16" height="16" type="image/x-icon">{{ .BaseURL }}{{ .IconPath }}</Image>
  <Url type="text/html" template="{{ .BaseURL }}/?q={searchTerms}"/>
  <moz:SearchForm>/</moz:SearchForm>
</OpenSearchDescription>

`

// Config is the on-disk configuration. Top-level `commands` is preserved for
// backward compatibility and is treated as part of the common command set.
type Config struct {
	Maintainers []string                  `yaml:"maintainers,omitempty"`
	Common      ProfileSection            `yaml:"common,omitempty"`
	Profiles    map[string]ProfileSection `yaml:"profiles,omitempty"`
	Commands    []Command                 `yaml:"commands,omitempty"`
}

// ProfileSection describes either an inline set of commands or a pointer to a
// separate YAML file. Both may be combined: the external file is loaded first,
// and inline commands override or extend by name.
type ProfileSection struct {
	File     string    `yaml:"file,omitempty"`
	Commands []Command `yaml:"commands,omitempty"`
}

type Command struct {
	Name string `yaml:"name"`
	// Default must be set to true for exactly one command per resolved
	// profile. When true, this command is used when no command is specified.
	Default       bool     `yaml:"default"`
	Aliases       []string `yaml:"aliases,omitempty"`
	URL           string   `yaml:"url"`
	URLWithParams string   `yaml:"url_with_params"`
	Description   string   `yaml:"description,omitempty"`
	Usage         string   `yaml:"usage,omitempty"`
}

// ResolvedProfile is the post-merge view of a profile: common commands plus
// the profile's overrides, ready to serve.
type ResolvedProfile struct {
	Name       string
	Commands   []Command
	cmdMap     map[string]*Command
	defaultCmd *Command
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	return &cfg, nil
}

func loadProfileSection(path string) (ProfileSection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProfileSection{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var ps ProfileSection
	if err := yaml.Unmarshal(data, &ps); err != nil {
		return ProfileSection{}, fmt.Errorf("failed to unmarshal %s: %w", path, err)
	}
	return ps, nil
}

// mergeByName combines two command lists, with later entries replacing earlier
// ones by name. The resulting slice preserves no particular order.
func mergeByName(lists ...[]Command) []Command {
	byName := map[string]Command{}
	order := []string{}
	for _, l := range lists {
		for _, c := range l {
			if _, seen := byName[c.Name]; !seen {
				order = append(order, c.Name)
			}
			byName[c.Name] = c
		}
	}
	out := make([]Command, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}

// resolveProfiles loads any external profile files referenced by cfg and
// returns a map of profile name to ResolvedProfile. The empty-string key is
// the fallback profile (common commands only), selected when -p/--profile
// is unset.
func resolveProfiles(cfg *Config, configDir string) (map[string]*ResolvedProfile, error) {
	commonCmds, err := materializeSection(cfg.Common, configDir)
	if err != nil {
		return nil, fmt.Errorf("common: %w", err)
	}
	// Top-level commands are treated as additional common commands.
	commonCmds = mergeByName(commonCmds, cfg.Commands)

	resolved := map[string]*ResolvedProfile{}
	rp, err := buildResolved("", commonCmds, nil)
	if err != nil {
		return nil, err
	}
	resolved[""] = rp

	for name, ps := range cfg.Profiles {
		profileCmds, err := materializeSection(ps, configDir)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		rp, err := buildResolved(name, commonCmds, profileCmds)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		resolved[name] = rp
	}
	return resolved, nil
}

func materializeSection(ps ProfileSection, configDir string) ([]Command, error) {
	var external []Command
	if ps.File != "" {
		path := ps.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(configDir, path)
		}
		ext, err := loadProfileSection(path)
		if err != nil {
			return nil, err
		}
		external = ext.Commands
	}
	return mergeByName(external, ps.Commands), nil
}

func buildResolved(name string, common, profile []Command) (*ResolvedProfile, error) {
	if err := checkNoDupes(common); err != nil {
		return nil, fmt.Errorf("common commands: %w", err)
	}
	if err := checkNoDupes(profile); err != nil {
		return nil, fmt.Errorf("profile commands: %w", err)
	}

	// Index common by name so we can demote its default if the profile sets
	// its own.
	commonByName := map[string]Command{}
	for _, c := range common {
		commonByName[c.Name] = c
	}

	profileHasDefault := false
	replacedByProfile := map[string]bool{}
	for _, c := range profile {
		replacedByProfile[c.Name] = true
		if c.Default {
			profileHasDefault = true
		}
	}
	if profileHasDefault {
		for n, c := range commonByName {
			if c.Default && !replacedByProfile[n] {
				c.Default = false
				commonByName[n] = c
			}
		}
	}

	// Reconstruct the common list (post-demotion) preserving original order.
	commonFixed := make([]Command, 0, len(common))
	for _, c := range common {
		commonFixed = append(commonFixed, commonByName[c.Name])
	}
	merged := mergeByName(commonFixed, profile)

	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })

	cmdMap := map[string]*Command{}
	var defaultCmd *Command
	for i := range merged {
		c := &merged[i]
		if c.Default {
			if defaultCmd != nil {
				return nil, fmt.Errorf("multiple default commands: %q and %q", defaultCmd.Name, c.Name)
			}
			defaultCmd = c
		}
		if _, dup := cmdMap[c.Name]; dup {
			return nil, fmt.Errorf("duplicate command name %q", c.Name)
		}
		cmdMap[c.Name] = c
		for _, a := range c.Aliases {
			if _, dup := cmdMap[a]; dup {
				return nil, fmt.Errorf("alias %q for command %q collides with another name or alias", a, c.Name)
			}
			cmdMap[a] = c
		}
	}

	return &ResolvedProfile{
		Name:       name,
		Commands:   merged,
		cmdMap:     cmdMap,
		defaultCmd: defaultCmd,
	}, nil
}

func checkNoDupes(cmds []Command) error {
	seen := map[string]bool{}
	for _, c := range cmds {
		if seen[c.Name] {
			return fmt.Errorf("duplicate command %q", c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

func iconHandler(w http.ResponseWriter, r *http.Request) {
	if _, err := w.Write(iconBytes); err != nil {
		log.Printf("Failed to write icon: %v", err)
	}
}

func requestBaseURL(r *http.Request) string {
	if *flagBaseURL != "" {
		return *flagBaseURL
	}
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func makeOpensearchHandler(profile string) http.HandlerFunc {
	tpl := template.Must(template.New("opensearch").Parse(opensearchTemplate))
	return func(w http.ResponseWriter, r *http.Request) {
		data := struct {
			BaseURL  string
			IconPath string
			Profile  string
		}{
			BaseURL:  requestBaseURL(r),
			IconPath: "/icon.png",
			Profile:  profile,
		}
		var out bytes.Buffer
		if err := tpl.Execute(&out, data); err != nil {
			log.Printf("Failed to generate opensearch.xml: %v", err)
			if _, err := fmt.Fprintf(w, "Failed to generate page, check logs"); err != nil {
				log.Printf("Failed to write HTML reply: %v", err)
			}
			return
		}
		if _, err := fmt.Fprint(w, out.String()); err != nil {
			log.Printf("Failed to write opensearch reply: %v", err)
		}
	}
}

func makeHandler(rp *ResolvedProfile, maintainers []string) http.HandlerFunc {
	tpl := template.Must(template.New("cmdDirectory").Funcs(
		template.FuncMap{
			"join": func(a []string, d string) string {
				return strings.Join(a, d)
			},
		},
	).Parse(cmdDirectoryTemplate))

	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		parts := strings.SplitN(q, " ", 2)
		cmdName := strings.ToLower(parts[0])
		cmdArg := ""
		if len(parts) > 1 {
			cmdArg = parts[1]
		}
		if q == "" || q == "list" || q == "help" {
			iconBase64 := base64.StdEncoding.EncodeToString(iconBytes)
			data := struct {
				Profile     string
				Commands    []Command
				Maintainers []string
				Icon        string
			}{
				Profile:     rp.Name,
				Commands:    rp.Commands,
				Maintainers: maintainers,
				Icon:        iconBase64,
			}
			var html bytes.Buffer
			if err := tpl.Execute(&html, data); err != nil {
				log.Printf("Failed to generate page: template failed: %v", err)
				if _, err := fmt.Fprintf(w, "Failed to generate page, check logs"); err != nil {
					log.Printf("Failed to write HTML reply: %v", err)
				}
				return
			}
			if _, err := fmt.Fprint(w, html.String()); err != nil {
				log.Printf("Failed to write HTML reply: %v", err)
			}
			return
		}

		cmd, found := rp.cmdMap[cmdName]
		if !found {
			cmd = rp.defaultCmd
			cmdArg = q
		}
		if cmd == nil {
			log.Printf("[profile=%q] no command matches %q and no default is set", rp.Name, q)
			http.Error(w, "no matching command and no default configured", http.StatusNotFound)
			return
		}
		log.Printf("[profile=%q] Requested cmd '%s' with args '%s' by %s (raw: '%s')", rp.Name, cmd.Name, cmdArg, r.RemoteAddr, q)
		var u string
		if cmdArg != "" && cmd.URLWithParams != "" {
			u = fmt.Sprintf(cmd.URLWithParams, url.QueryEscape(cmdArg))
		} else {
			u = cmd.URL
		}
		log.Printf("Redirecting to '%s'", u)
		http.Redirect(w, r, u, http.StatusSeeOther)
	}
}

func main() {
	pflag.Parse()
	if *flagConfig == "" {
		log.Fatalf("Missing config file, see -c/--config")
	}
	config, err := loadConfig(*flagConfig)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	configDir := filepath.Dir(*flagConfig)
	profiles, err := resolveProfiles(config, configDir)
	if err != nil {
		log.Fatalf("Failed to resolve profiles: %v", err)
	}

	for name, rp := range profiles {
		if name == "" {
			log.Printf("Loaded %d common command(s)", len(rp.Commands))
			continue
		}
		log.Printf("Loaded profile %q with %d resolved command(s)", name, len(rp.Commands))
	}

	active, ok := profiles[*flagProfile]
	if !ok {
		available := make([]string, 0, len(profiles))
		for n := range profiles {
			if n != "" {
				available = append(available, n)
			}
		}
		sort.Strings(available)
		log.Fatalf("Unknown profile %q; configured profiles: %v", *flagProfile, available)
	}
	if active.Name == "" {
		log.Printf("Serving common commands (no profile selected; pass -p/--profile to choose one)")
	} else {
		log.Printf("Serving profile %q", active.Name)
	}

	http.HandleFunc("/", makeHandler(active, config.Maintainers))
	http.HandleFunc("/icon.png", iconHandler)
	http.HandleFunc("/opensearch.xml", makeOpensearchHandler(active.Name))
	log.Printf("Listening on %s", *flagListen)
	log.Fatal(http.ListenAndServe(*flagListen, nil))
}

package main

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/pflag"
)

var (
	flagListen  = pflag.StringP("listen", "l", "localhost:8182", "Listen host:port")
	flagConfig  = pflag.StringP("config", "c", "", "Path to config file")
	flagBaseURL = pflag.StringP("base-url", "u", "", "Base URL for search site, e.g. https://example.org")
)

//go:embed command_list.template
var cmdDirectoryTemplate string

//go:embed lol.png
var iconBytes []byte

var opensearchTemplate = `<OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/"
      xmlns:moz="http://www.mozilla.org/2006/browser/search/">
  <ShortName>LOL</ShortName>
  <Description>LOL shortcuts</Description>
  <Image width="16" height="16" type="image/x-icon">{{ .BaseURL }}{{ .IconPath }}</Image>
  <Url type="text/html" template="{{ .BaseURL }}/?q={searchTerms}"/>
  <moz:SearchForm>/</moz:SearchForm>
</OpenSearchDescription>

`

type Config struct {
	Maintainers []string
	Commands    []Command `json:"commands"`
}

type Command struct {
	Name string `json:"name"`
	// Default must be set to true for exactly one command. When true, this
	// command is used when no command is specified.
	Default       bool     `json:"default"`
	Aliases       []string `json:"aliases,omitempty"`
	URL           string   `json:"url"`
	URLWithParams string   `json:"url_with_params,omitempty"`
	Description   string   `json:"description,omitempty"`
	Usage         string   `json:"usage,omitempty"`
}

func iconHandler(w http.ResponseWriter, r *http.Request) {
	if _, err := w.Write(iconBytes); err != nil {
		log.Printf("Failed to write icon: %v", err)
	}
}

func opensearchHandler(w http.ResponseWriter, r *http.Request) {
	data := struct {
		BaseURL  string
		IconPath string
	}{
		BaseURL: *flagBaseURL,
		// IconPath has to match the icon path passed to the icon
		// HTTP handler
		IconPath: "/icon.png",
	}
	tpl := template.Must(template.New("opensearch").Parse(opensearchTemplate))
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

	if _, err := w.Write(iconBytes); err != nil {
		log.Printf("Failed to write icon: %v", err)
	}
}

func makeHandler(cfg *Config) (func(http.ResponseWriter, *http.Request), error) {
	// build command map
	cmdMap := make(map[string]*Command)
	var defaultCmd *Command
	for _, cmd := range cfg.Commands {
		if cmd.Default {
			if defaultCmd != nil {
				return nil, fmt.Errorf("found more than one default command in configuration")
			}
			tmp := cmd
			defaultCmd = &tmp
		}
		c := cmd
		cmdMap[c.Name] = &c
		for _, alias := range c.Aliases {
			cmdMap[alias] = &c
		}
	}
	// parse command directory template
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
			// show command list
			sort.Slice(cfg.Commands, func(i, j int) bool { return cfg.Commands[i].Name < cfg.Commands[j].Name })
			iconBase64 := base64.StdEncoding.EncodeToString(iconBytes)
			var html bytes.Buffer
			data := struct {
				Commands    []Command
				Maintainers []string
				Icon        string
			}{
				Commands:    cfg.Commands,
				Maintainers: cfg.Maintainers,
				Icon:        iconBase64,
			}
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

		var u string
		cmd, found := cmdMap[cmdName]
		if !found {
			cmd = defaultCmd
			cmdArg = q
		}
		log.Printf("Requested cmd '%s' with args '%s' by %s (raw request: '%s')", cmd.Name, cmdArg, r.RemoteAddr, q)
		if cmdArg != "" && cmd.URLWithParams != "" {
			// FIXME(insomniacslk) check that URLWithParams contains a %s and
			// just one.
			u = fmt.Sprintf(cmd.URLWithParams, url.QueryEscape(cmdArg))
		} else {
			u = cmd.URL
		}
		log.Printf("Redirecting to '%s'", u)
		http.Redirect(w, r, u, http.StatusSeeOther)
	}, nil
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	return &cfg, nil
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
	log.Printf("Loaded %d terms", len(config.Commands))

	cmdHandler, err := makeHandler(config)
	if err != nil {
		log.Fatalf("Failed to make handler: %v", err)
	}
	http.HandleFunc("/", cmdHandler)
	http.HandleFunc("/icon.png", iconHandler)
	http.HandleFunc("/opensearch.xml", opensearchHandler)
	log.Printf("Listening on %s", *flagListen)
	log.Fatal(http.ListenAndServe(*flagListen, nil))
}

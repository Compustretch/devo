package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "time/tzdata"
)

const (
	serverName         = "devo-toolserver"
	serverVersion      = "0.1.0"
	defaultListen      = "0.0.0.0:8000"
	defaultGrafanaBin  = "/app/mcp-grafana"
	grafanaPrefix      = "grafana."
	grafanaTools       = "search,datasource,incident,prometheus,loki,alerting,dashboard,folder,oncall,asserts,pyroscope,navigation,annotations,rendering,snapshot,plugin,api,config,provisioning,docs,user,sql"
)

type DayInput struct {
	Timezone string JSON_TAG_TIMEZONE
}

type DaySummary struct {
	Date    string JSON_TAG_DATE
	Weekday string JSON_TAG_WEEKDAY
}

type DayOutput struct {
	Date      string JSON_TAG_DATE
	Weekday   string JSON_TAG_WEEKDAY
	Timezone  string JSON_TAG_TIMEZONE_SIMPLE
	Tomorrow  DaySummary JSON_TAG_TOMORROW
	Yesterday DaySummary JSON_TAG_YESTERDAY
	Text      string JSON_TAG_TEXT
}

// dayAt interprets now in the caller's timezone, then performs date arithmetic
// on date-only values. This avoids assuming the container's timezone and avoids
// 24-hour duration arithmetic across daylight-saving transitions.
func dayAt(now time.Time, timezone string) (DayOutput, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return DayOutput{}, errors.New("timezone is required; provide the caller's IANA timezone")
	}
	if timezone == "Local" {
		return DayOutput{}, errors.New("timezone must be an IANA timezone, not Local")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return DayOutput{}, fmt.Errorf("invalid IANA timezone %q: %w", timezone, err)
	}

	localNow := now.In(location)
	// A UTC date anchor preserves Gregorian calendar arithmetic independently
	// of offset changes in the caller's timezone.
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 12, 0, 0, 0, time.UTC)
	summary := func(date time.Time) DaySummary {
		return DaySummary{Date: date.Format("2006-01-02"), Weekday: date.Weekday().String()}
	}

	todaySummary := summary(today)
	return DayOutput{
		Date:      todaySummary.Date,
		Weekday:   todaySummary.Weekday,
		Timezone:  timezone,
		Tomorrow:  summary(today.AddDate(0, 0, 1)),
		Yesterday: summary(today.AddDate(0, 0, -1)),
		Text:      "Today is " + todaySummary.Weekday,
	}, nil
}

type toolCatalog struct {
	owners map[string]string
}

func newToolCatalog() *toolCatalog {
	return &toolCatalog{owners: make(map[string]string)}
}

func (c *toolCatalog) reserve(name, owner string) error {
	if previous, exists := c.owners[name]; exists {
		return fmt.Errorf("MCP tool name %q is registered by both %s and %s", name, previous, owner)
	}
	c.owners[name] = owner
	return nil
}

func registerDayTool(server *mcp.Server, catalog *toolCatalog) error {
	if err := catalog.reserve("day.get", "native tools"); err != nil {
		return err
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "day.get",
		Description: "Get today's date and weekday, plus yesterday and tomorrow, in the calling client's IANA timezone. The timezone argument is required; the tool never assumes the container's timezone.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input DayInput) (*mcp.CallToolResult, DayOutput, error) {
		output, err := dayAt(time.Now(), input.Timezone)
		return nil, output, err
	})
	return nil
}

func validateInputSchema(schema any) error {
	if schema == nil {
		return errors.New("provider tool has no input schema")
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshal provider input schema: %w", err)
	}
	var value struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode provider input schema: %w", err)
	}
	if value.Type != "object" {
		return fmt.Errorf("provider input schema root type is %q, want object", value.Type)
	}
	return nil
}

// registerProviderTools publishes an upstream MCP server's tools through the
// shared catalog. Prefixes preserve ownership and prevent provider name clashes.
func registerProviderTools(
	ctx context.Context,
	server *mcp.Server,
	catalog *toolCatalog,
	providerName string,
	prefix string,
	session *mcp.ClientSession,
) (int, error) {
	var tools []*mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return 0, fmt.Errorf("list tools from %s provider: %w", providerName, err)
		}
		if tool != nil {
			tools = append(tools, tool)
		}
	}

	for _, tool := range tools {
		qualifiedName := prefix + tool.Name
		if err := validateInputSchema(tool.InputSchema); err != nil {
			return 0, fmt.Errorf("%s tool %q: %w", providerName, tool.Name, err)
		}
		if err := catalog.reserve(qualifiedName, providerName); err != nil {
			return 0, err
		}
	}

	for _, upstreamTool := range tools {
		originalName := upstreamTool.Name
		qualifiedTool := *upstreamTool
		qualifiedTool.Name = prefix + originalName

		server.AddTool(&qualifiedTool, func(callCtx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := session.CallTool(callCtx, &mcp.CallToolParams{
				Name:      originalName,
				Arguments: request.Params.Arguments,
			})
			if err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{
						Text: fmt.Sprintf("%s provider tool %s failed: %v", providerName, originalName, err),
					}},
					IsError: true,
				}, nil
			}
			return result, nil
		})
	}
	return len(tools), nil
}

func connectGrafana(ctx context.Context) (*mcp.ClientSession, error) {
	binary := os.Getenv("GRAFANA_MCP_BIN")
	if binary == "" {
		binary = defaultGrafanaBin
	}
	command := exec.Command(binary,
		"--transport", "stdio",
		"--enabled-tools", grafanaTools,
		"--disable-write",
	)
	command.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{
		Name: serverName + "-grafana-upstream", Version: serverVersion,
	}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to bundled Grafana MCP provider: %w", err)
	}
	return session, nil
}

func run(ctx context.Context) error {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: "Native tools are registered directly. Tools prefixed with grafana. are forwarded to the bundled Grafana MCP provider."},
	)
	catalog := newToolCatalog()
	if err := registerDayTool(mcpServer, catalog); err != nil {
		return err
	}

	grafanaSession, err := connectGrafana(ctx)
	if err != nil {
		// Native tools remain available if an optional upstream cannot start.
		log.Printf("warning: Grafana MCP provider unavailable: %v", err)
	} else {
		count, registerErr := registerProviderTools(ctx, mcpServer, catalog, "Grafana", grafanaPrefix, grafanaSession)
		if registerErr != nil {
			_ = grafanaSession.Close()
			log.Printf("warning: Grafana MCP tools were not registered: %v", registerErr)
		} else {
			defer func() {
				if closeErr := grafanaSession.Close(); closeErr != nil {
					log.Printf("close Grafana MCP provider: %v", closeErr)
				}
			}()
			log.Printf("registered %d Grafana MCP tools", count)
		}
	}

	address := os.Getenv("TOOLSERVER_ADDR")
	if address == "" {
		address = defaultListen
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("invalid TOOLSERVER_ADDR %q: %w", address, err)
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok
"))
	})

	httpServer := &http.Server{
		Addr: address, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("MCP toolserver listening on %s", address)
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			return fmt.Errorf("shut down MCP HTTP server: %w", err)
		}
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve MCP HTTP endpoint: %w", err)
	}
}

func healthcheck() error {
	url := os.Getenv("TOOLSERVER_HEALTH_URL")
	if url == "" {
		url = "http://127.0.0.1:8000/healthz"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			log.Printf("toolserver healthcheck failed: %v", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		log.Printf("usage: devo-toolserver [healthcheck]")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Printf("toolserver failed: %v", err)
		os.Exit(1)
	}
}

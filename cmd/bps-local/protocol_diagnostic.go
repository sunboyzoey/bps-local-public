package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/sunboyzoey/GPT-BPS/internal/proxy"
)

type protocolDiagnosticFlags struct {
	Enabled bool
	Model   string
	Effort  string
	Output  string
}

func registerProtocolDiagnosticFlags(fs *flag.FlagSet) *protocolDiagnosticFlags {
	options := &protocolDiagnosticFlags{}
	fs.BoolVar(&options.Enabled, "protocol-diagnostic", false, "observe two short BPS WebSocket requests through the explicit proxy, then exit")
	fs.StringVar(&options.Model, "diagnostic-model", "gpt-6-astra", "requested model for the protocol diagnostic")
	fs.StringVar(&options.Effort, "diagnostic-effort", "medium", "requested reasoning effort for the protocol diagnostic")
	fs.StringVar(&options.Output, "diagnostic-output", "bps-protocol-diagnostic.json", "new diagnostic JSON report path; existing files are never overwritten")
	return options
}

type protocolDiagnosticConflicts struct {
	Login     bool
	Check     bool
	Transport string
	ARCText   bool
	Version   bool
	AuthPath  bool
	ProxyURL  string
}

func validateProtocolDiagnosticFlags(options *protocolDiagnosticFlags, other protocolDiagnosticConflicts) error {
	if other.Login || other.Check || other.ARCText || other.Version || other.AuthPath {
		return errors.New("--protocol-diagnostic cannot be combined with --login, --check, --arc-text-session, --version, or --auth-path")
	}
	if other.Transport != "ws" {
		return errors.New("--protocol-diagnostic requires --upstream-transport ws")
	}
	if strings.TrimSpace(options.Output) == "" {
		return errors.New("--diagnostic-output must specify a new report file")
	}
	if strings.TrimSpace(other.ProxyURL) == "" {
		return errors.New("--protocol-diagnostic requires --proxy or BPS_LOCAL_PROXY; direct connections are disabled")
	}
	u, err := url.Parse(other.ProxyURL)
	if err != nil || u.Host == "" || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("protocol diagnostic proxy must be a valid http, https, socks5, or socks5h URL")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	default:
		return errors.New("protocol diagnostic proxy must use http, https, socks5, or socks5h")
	}
}

func runProtocolDiagnosticCLI(ctx context.Context, cfg proxy.Config, store proxy.CredentialSource, options *protocolDiagnosticFlags) error {
	// Refuse an existing report before sending requests. O_EXCL below also closes
	// the race with another process creating the path during the diagnostic.
	if _, err := os.Lstat(options.Output); err == nil {
		return errors.New("diagnostic output already exists; choose a new --diagnostic-output path")
	} else if !os.IsNotExist(err) {
		return errors.New("diagnostic output path cannot be inspected")
	}

	report, runErr := proxy.RunProtocolDiagnostic(ctx, cfg, store, proxy.ProtocolDiagnosticOptions{
		Model: options.Model, ReasoningEffort: options.Effort,
	})
	if err := saveProtocolDiagnosticReport(options.Output, report); err != nil {
		return err
	}
	fmt.Printf("Protocol diagnostic report: %s\n仅观测，不改变账号状态\n", options.Output)
	if runErr != nil {
		// Upstream errors may contain credentials, proxy userinfo, or response
		// bodies. Only the runner's allowlisted report is safe to publish.
		return errors.New("protocol diagnostic did not complete successfully; inspect the saved report for observations")
	}
	return nil
}

func saveProtocolDiagnosticReport(path string, report any) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.New("protocol diagnostic report could not be encoded")
	}
	raw = append(raw, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return errors.New("diagnostic output already exists; choose a new --diagnostic-output path")
		}
		return errors.New("protocol diagnostic report could not be created")
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return errors.New("protocol diagnostic report could not be written completely")
	}
	if err := file.Close(); err != nil {
		return errors.New("protocol diagnostic report could not be closed successfully")
	}
	return nil
}

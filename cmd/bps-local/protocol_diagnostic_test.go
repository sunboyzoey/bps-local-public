package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sunboyzoey/GPT-BPS/internal/proxy"
)

func TestProtocolDiagnosticFlags(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		options := registerProtocolDiagnosticFlags(fs)
		if err := fs.Parse([]string{"--protocol-diagnostic"}); err != nil {
			t.Fatal(err)
		}
		if !options.Enabled || options.Model != "gpt-6-astra" || options.Effort != "medium" || options.Output != "bps-protocol-diagnostic.json" {
			t.Fatalf("unexpected defaults: %+v", options)
		}
	})
	t.Run("overrides", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		options := registerProtocolDiagnosticFlags(fs)
		if err := fs.Parse([]string{"--protocol-diagnostic", "--diagnostic-model", "test-model", "--diagnostic-effort=high", "--diagnostic-output", "new-report.json"}); err != nil {
			t.Fatal(err)
		}
		if !options.Enabled || options.Model != "test-model" || options.Effort != "high" || options.Output != "new-report.json" {
			t.Fatalf("unexpected parsed flags: %+v", options)
		}
	})
	t.Run("invalid boolean", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		registerProtocolDiagnosticFlags(fs)
		if err := fs.Parse([]string{"--protocol-diagnostic=maybe"}); err == nil {
			t.Fatal("invalid boolean flag was accepted")
		}
	})
}

func TestProtocolDiagnosticFlagConflicts(t *testing.T) {
	options := &protocolDiagnosticFlags{Output: "report.json"}
	for _, field := range []string{"login", "check", "arc", "version", "auth-path", "sse", "unknown transport"} {
		t.Run(field, func(t *testing.T) {
			other := protocolDiagnosticConflicts{Transport: "ws", ProxyURL: "http://127.0.0.1:7890"}
			switch field {
			case "login":
				other.Login = true
			case "check":
				other.Check = true
			case "arc":
				other.ARCText = true
			case "version":
				other.Version = true
			case "auth-path":
				other.AuthPath = true
			case "sse":
				other.Transport = "sse"
			default:
				other.Transport = "other"
			}
			if err := validateProtocolDiagnosticFlags(options, other); err == nil {
				t.Fatal("conflicting flags were accepted")
			}
		})
	}
	if err := validateProtocolDiagnosticFlags(&protocolDiagnosticFlags{Output: " "}, protocolDiagnosticConflicts{Transport: "ws", ProxyURL: "http://localhost:7890"}); err == nil {
		t.Fatal("empty output path was accepted")
	}
}

func TestProtocolDiagnosticProxyValidation(t *testing.T) {
	options := &protocolDiagnosticFlags{Output: "report.json"}
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			if err := validateProtocolDiagnosticFlags(options, protocolDiagnosticConflicts{Transport: "ws", ProxyURL: scheme + "://proxy-user:proxy-secret@127.0.0.1:7890"}); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, address := range []string{"", " ", "localhost:7890", "file:///tmp/proxy-secret", "ftp://proxy-user:proxy-secret@localhost:7890", "http://proxy-user:proxy-secret@", "http://proxy-user:proxy-secret@localhost:bad", "http://localhost:7890/path", "http://localhost:7890?secret=proxy-secret", "http://localhost:7890#proxy-secret"} {
		err := validateProtocolDiagnosticFlags(options, protocolDiagnosticConflicts{Transport: "ws", ProxyURL: address})
		if err == nil {
			t.Fatalf("invalid proxy was accepted: %q", address)
		}
		if strings.Contains(err.Error(), "proxy-secret") || strings.Contains(err.Error(), "proxy-user") {
			t.Fatal("proxy credentials leaked into validation error")
		}
	}
}

func TestSaveProtocolDiagnosticReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := saveProtocolDiagnosticReport(path, map[string]any{"version": 1, "quality_assessment": "not_assessed"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) || !strings.HasSuffix(string(raw), "\n") || !strings.Contains(string(raw), "\n  ") {
		t.Fatalf("expected formatted JSON with trailing newline: %q", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("report permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestSaveProtocolDiagnosticReportRefusesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth.json")
	original := []byte("existing private OAuth data")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveProtocolDiagnosticReport(path, map[string]bool{"replaced": true}); err == nil {
		t.Fatal("existing OAuth file was accepted as output")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("existing file was changed")
	}
}

func TestSaveProtocolDiagnosticReportRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "missing-target.json")
	path := filepath.Join(dir, "report.json")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := saveProtocolDiagnosticReport(path, map[string]bool{"written": true}); err == nil {
		t.Fatal("symlink output was accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("symlink target unexpectedly created: %v", err)
	}
}

func TestSaveProtocolDiagnosticReportEncodingFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := saveProtocolDiagnosticReport(path, make(chan int)); err == nil {
		t.Fatal("unsupported JSON value was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("encoding failure created output: %v", err)
	}
}

type unusedDiagnosticCredentials struct{}

func (unusedDiagnosticCredentials) Credentials() (string, string, error) {
	panic("existing output must be rejected before reading credentials or sending requests")
}

func (unusedDiagnosticCredentials) Expiry() time.Time {
	panic("existing output must be rejected before reading credentials or sending requests")
}

func TestProtocolDiagnosticRefusesExistingOutputBeforeRunner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runProtocolDiagnosticCLI(context.Background(), proxy.Config{}, unusedDiagnosticCredentials{}, &protocolDiagnosticFlags{Output: path})
	if err == nil {
		t.Fatal("existing output did not stop the diagnostic")
	}
}

type rejectedDiagnosticCredentials struct{}

func (rejectedDiagnosticCredentials) Credentials() (string, string, error) {
	return "", "", errors.New("private-credential-error")
}

func (rejectedDiagnosticCredentials) Expiry() time.Time {
	return time.Time{}
}

func TestProtocolDiagnosticSavesReportOnRunnerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	err := runProtocolDiagnosticCLI(context.Background(), proxy.Config{
		ProxyURL: "http://proxy-user:proxy-secret@127.0.0.1:1", UpstreamTransport: "ws",
	}, rejectedDiagnosticCredentials{}, &protocolDiagnosticFlags{Output: path})
	if err == nil {
		t.Fatal("credential error did not fail the diagnostic")
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !json.Valid(raw) {
		t.Fatalf("runner error report is not JSON: %q", raw)
	}
	for _, private := range []string{"private-credential-error", "proxy-user", "proxy-secret"} {
		if strings.Contains(err.Error(), private) || strings.Contains(string(raw), private) {
			t.Fatal("private data leaked into runner error or report")
		}
	}
}

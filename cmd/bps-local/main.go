// Command bps-local runs a loopback Responses-API bridge that lets Codex talk to
// the ChatGPT Excel (Basispoints) gateway using your own ChatGPT login.
//
// It reads an independent BPS OAuth file and writes new credentials only during
// explicit login. The bridge listens only on loopback and sends prompts
// only to bps.openai.com.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sunboyzoey/GPT-BPS/internal/auth"
	"github.com/sunboyzoey/GPT-BPS/internal/basispoints"
	"github.com/sunboyzoey/GPT-BPS/internal/gateway"
	"github.com/sunboyzoey/GPT-BPS/internal/proxy"
)

var version = "0.4.0"

func main() {
	log.SetFlags(0)
	log.SetPrefix("[bps-local] ")

	var (
		login         = flag.Bool("login", false, "perform BPS login and save a new independent OAuth file")
		loginMethod   = flag.String("login-method", "", "login method: device (default) or browser; requires --login")
		browserPath   = flag.String("browser-path", "", "Chrome, Chromium, or Edge executable for --login --login-method browser")
		noBrowser     = flag.Bool("no-browser", false, "device login only: print login URL without opening a browser")
		addr          = flag.String("addr", "127.0.0.1:8790", "loopback address to listen on")
		authPath      = flag.String("auth", "", "BPS OAuth file (default: $BPS_AUTH_FILE or ~/.bps-local/oauth.json)")
		showAuthPath  = flag.Bool("auth-path", false, "print selected OAuth file path without reading credentials")
		proxyURL      = flag.String("proxy", os.Getenv("BPS_LOCAL_PROXY"), "outbound proxy URL, e.g. http://127.0.0.1:7890")
		transport     = flag.String("upstream-transport", "ws", "official BPS upstream transport: ws only (local API stays HTTP/SSE)")
		wsIdle        = flag.Duration("ws-idle-timeout", 2*time.Minute, "maximum wait for the next upstream WebSocket event")
		maxConcurrent = flag.Int("max-concurrent", gateway.DefaultLimits().PerAccount, "maximum active requests and fixed task slots per account (1-10000)")
		maxPending    = flag.Int("max-pending", 64, "maximum requests waiting for a task slot; 0 disables queuing")
		queueWait     = flag.Duration("queue-timeout", 30*time.Second, "total admission and upstream-start wait budget (does not limit generation time)")
		stateDir      = flag.String("state-dir", os.Getenv("BPS_LOCAL_STATE_DIR"), "private task state directory (default ~/.bps-local/local-state); preserve across restarts")
		upstreamRPS   = flag.Int("upstream-rps", 0, "per-account response.create starts in any rolling second; 0 disables (maximum 10000)")
		rateWait      = flag.Duration("rate-wait-timeout", 30*time.Second, "maximum wait for an upstream start slot")
		ratePending   = flag.Int("rate-max-pending", 64, "maximum pending upstream starts per account; 0 uses the default 64")
		toolsVersion  = flag.String("bps-tools-version", "", "optional BPS tool version ID; empty uses the server default")
		arcText       = flag.Bool("arc-text-session", false, "experimental ARC initialization with no Excel tools; text-only requests")
		showVersion   = flag.Bool("version", false, "print version and exit")
		check         = flag.Bool("check", false, "inspect local credential metadata (does not verify BPS access)")
	)
	diagnostic := registerProtocolDiagnosticFlags(flag.CommandLine)
	flag.Parse()
	if err := validateLoginFlags(*login, *loginMethod, *browserPath, *noBrowser); err != nil {
		log.Fatal(err)
	}
	if *transport != "ws" {
		log.Fatal("official BPS requires --upstream-transport ws; legacy POST/SSE is unsupported (local API stays HTTP/SSE)")
	}

	if diagnostic.Enabled {
		if err := validateProtocolDiagnosticFlags(diagnostic, protocolDiagnosticConflicts{
			Login: *login, Check: *check, Transport: *transport, ARCText: *arcText,
			Version: *showVersion, AuthPath: *showAuthPath, ProxyURL: *proxyURL,
		}); err != nil {
			log.Fatal(err)
		}
	}

	if *showVersion {
		fmt.Println("bps-local", version)
		return
	}
	if *showAuthPath {
		fmt.Println(authPathOrDefault(*authPath))
		return
	}

	if *login {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runLogin(ctx, loginOptions{
			Method: *loginMethod, BrowserPath: *browserPath, NoBrowser: *noBrowser,
			AuthPath: authPathOrDefault(*authPath), ProxyURL: *proxyURL,
		}, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}

	store, err := auth.Load(*authPath)
	if err != nil {
		if diagnostic.Enabled {
			log.Fatal("protocol diagnostic could not load the independent BPS OAuth file; no diagnostic requests were sent")
		}
		log.Fatalf("credentials: %v\n\nProvide independent BPS OAuth credentials with --auth or BPS_AUTH_FILE. Codex credentials are not loaded automatically; run --login (device) or --login --login-method browser; automatic refresh is not configured.", err)
	}

	if diagnostic.Enabled {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runProtocolDiagnosticCLI(ctx, proxy.Config{
			Upstream:          basispoints.ResponsesURL,
			ProxyURL:          *proxyURL,
			UpstreamTransport: *transport,
			WSIdleTimeout:     *wsIdle,
			UpstreamRPS:       *upstreamRPS,
			RateWaitTimeout:   *rateWait,
			RateQueueLimit:    *ratePending,
			ToolsVersion:      *toolsVersion,
		}, store, diagnostic); err != nil {
			log.Fatal(err)
		}
		return
	}

	token, accountID, err := store.Credentials()
	if err != nil {
		log.Fatalf("credentials: %v", err)
	}
	if accountID == "" {
		accountID = auth.AccountIDFromToken(token)
	}

	if *check {
		fmt.Printf("auth.json      : %s\n", authPathOrDefault(*authPath))
		fmt.Printf("account id     : %s\n", maskID(accountID))
		if plan := auth.PlanType(token); plan != "" {
			fmt.Printf("plan           : %s\n", plan)
		}
		if expiry := store.Expiry(); expiry.IsZero() {
			fmt.Println("token expires  : unknown")
		} else {
			fmt.Printf("token expires  : %s\n", expiry.Format(time.RFC3339))
		}
		info := auth.InspectToken(token)
		fmt.Printf("client id      : %s\n", maskID(info.ClientID))
		if info.ScopesKnown {
			fmt.Printf("scopes (%s)   : %s\n", info.ScopeClaim, strings.Join(info.Scopes, ", "))
		} else {
			fmt.Println("scopes         : unknown (missing or invalid claim)")
		}
		status := "local credentials loaded"
		if expiry := store.Expiry(); !expiry.IsZero() && !expiry.After(time.Now()) {
			status = "local token expired"
		}
		fmt.Printf("status         : %s\n", status)
		fmt.Println("BPS generation : not verified; decoded JWT claims are not authorization proof")
		fmt.Println("OAuth refresh  : disabled; stored refresh_token is not exchanged")
		return
	}

	if !proxy.IsLoopback(*addr) {
		log.Fatalf("refusing to bind %q: this bridge is loopback-only", *addr)
	}

	srv, err := proxy.New(proxy.Config{
		ARCTextSession:    *arcText,
		Addr:              *addr,
		Upstream:          basispoints.ResponsesURL,
		ProxyURL:          *proxyURL,
		UpstreamTransport: *transport,
		WSIdleTimeout:     *wsIdle,
		UpstreamRPS:       *upstreamRPS,
		RateWaitTimeout:   *rateWait,
		RateQueueLimit:    *ratePending,
		ToolsVersion:      *toolsVersion,
		Verbose:           true,
	}, store)
	if err != nil {
		log.Fatalf("%v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("using ChatGPT account %s", maskID(accountID))
	log.Printf("upstream transport: %s; local Responses endpoint: HTTP/SSE", *transport)
	if err := serveStandalone(ctx, srv, store, *stateDir, gateway.Limits{
		PerAccount: *maxConcurrent, Queue: *maxPending, Wait: *queueWait,
	}, *upstreamRPS); err != nil {
		log.Fatalf("%v", err)
	}
}

func authPathOrDefault(p string) string {
	if strings.TrimSpace(p) == "" {
		return auth.DefaultPath()
	}
	return p
}

func maskID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}

func openBrowser(address string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", address)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		cmd = exec.Command("xdg-open", address)
	}
	return cmd.Run()
}

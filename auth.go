package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/oauth"
)

// runAuth implements `mailer auth gmail`.
//
// The authorization-code flow needs a browser, which a daemon host usually does
// not have. Rather than requiring the server to be reachable from the outside,
// this command is meant to run wherever a browser exists (your laptop); the
// resulting token file is then copied to the server, which only ever reads it.
// The refresh token is bound to the OAuth client and user, not to a machine or
// IP, so authorizing on one host and using the token on another is legitimate.
//
// Two capture modes cover both environments:
//
//	(default)     bind 127.0.0.1 on an ephemeral port and receive the callback
//	--no-browser  print the consent URL and read the pasted redirect URL/code
func runAuth(args []string) int {
	if len(args) == 0 || args[0] != "gmail" {
		fmt.Fprintln(os.Stderr, "usage: mailer auth gmail [flags]")
		return 2
	}
	return runAuthGmail(args[1:])
}

func runAuthGmail(args []string) int {
	fs := flag.NewFlagSet("auth gmail", flag.ExitOnError)
	account := fs.String("account", "", "mailbox address to authorize (also used as login_hint)")
	clientID := fs.String("client-id", "", "OAuth client ID")
	clientSecret := fs.String("client-secret", "", "OAuth client secret (prefer --client-secret-file)")
	clientSecretFile := fs.String("client-secret-file", "", "file containing the OAuth client secret")
	output := fs.String("output", "", "write the token file here (default: print to stdout)")
	scope := fs.String("scope", config.DefaultGmailScope, "OAuth scope")
	tokenURL := fs.String("token-url", "", "token endpoint (default: Google)")
	noBrowser := fs.Bool("no-browser", false, "do not open a browser; print the URL and read the pasted redirect")
	timeout := fs.Duration("timeout", 5*time.Minute, "how long to wait for the authorization")
	fs.Parse(args)

	if *account == "" {
		fmt.Fprintln(os.Stderr, "auth gmail: --account is required")
		return 2
	}
	if *clientID == "" {
		fmt.Fprintln(os.Stderr, "auth gmail: --client-id is required")
		return 2
	}

	secret := *clientSecret
	if *clientSecretFile != "" {
		data, err := os.ReadFile(*clientSecretFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "auth gmail: read client secret: %v\n", err)
			return 1
		}
		secret = strings.TrimSpace(string(data))
	}
	if secret == "" {
		fmt.Fprintln(os.Stderr, "auth gmail: --client-secret or --client-secret-file is required")
		return 2
	}

	state, err := oauth.NewState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth gmail: %v\n", err)
		return 1
	}

	req := oauth.AuthRequest{
		ClientID:     *clientID,
		ClientSecret: secret,
		Scope:        *scope,
		TokenURL:     *tokenURL,
		LoginHint:    *account,
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var result *oauth.Result

	if *noBrowser {
		// Google requires redirect_uri on the token exchange to match the one
		// used for consent, so a fixed loopback value is used even though
		// nothing is listening on it. The browser cannot load 127.0.0.1 on a
		// remote host, which is exactly why the user copies the address bar.
		req.RedirectURI = "http://127.0.0.1:8765/callback"
		result, err = authorizeManual(ctx, req, state)
	} else {
		result, err = authorizeLoopback(ctx, req, state)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth gmail: %v\n", err)
		return 1
	}

	tok, err := oauth.Exchange(ctx, nil, req.TokenURL, req.ClientID, req.ClientSecret, result.Code, result.RedirectURI)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth gmail: exchange code: %v\n", err)
		return 1
	}
	tok.Account = *account
	tok.ClientID = *clientID

	if *output == "" {
		// Print the token, but shout about the secret so it does not end up in
		// shell history or CI logs unnoticed.
		raw, err := oauth.MarshalToken(tok)
		if err != nil {
			fmt.Fprintf(os.Stderr, "auth gmail: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "\nAuthorized %s.\n", *account)
		fmt.Fprintf(os.Stderr, "The output below contains a refresh token — treat it like a password.\n")
		fmt.Fprintf(os.Stderr, "On the server, save it to the path given by oauth2.token_file and chmod 600.\n\n")
		fmt.Println(raw)
		return 0
	}

	if err := oauth.SaveToken(*output, tok); err != nil {
		fmt.Fprintf(os.Stderr, "auth gmail: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Authorized %s; token written to %s (0600).\n", *account, *output)
	fmt.Fprintf(os.Stderr, "Copy it to the server's oauth2.token_file path.\n")
	return 0
}

// authorizeLoopback runs the normal installed-app flow: bind a loopback port,
// open a browser, and receive the callback directly.
func authorizeLoopback(ctx context.Context, req oauth.AuthRequest, state string) (*oauth.Result, error) {
	redirectURI, wait, stop, err := oauth.ListenLoopback(state)
	if err != nil {
		return nil, err
	}
	defer stop()

	req.RedirectURI = redirectURI
	consent := oauth.BuildConsentURL(req, state)

	fmt.Fprintf(os.Stderr, "Open this URL to authorize:\n\n  %s\n\n", consent)
	if err := openBrowser(consent); err == nil {
		fmt.Fprintln(os.Stderr, "(attempted to open your browser)")
	} else {
		fmt.Fprintf(os.Stderr, "(could not open a browser automatically: %v)\n", err)
	}
	fmt.Fprintln(os.Stderr, "Waiting for the callback...")

	return wait(ctx)
}

// authorizeManual is the headless flow: print the URL, let the user authorize
// on any machine, then paste back the redirect URL (or just the code).
func authorizeManual(ctx context.Context, req oauth.AuthRequest, state string) (*oauth.Result, error) {
	consent := oauth.BuildConsentURL(req, state)

	fmt.Fprintf(os.Stderr, "Authorize this app by visiting:\n\n  %s\n\n", consent)
	fmt.Fprintf(os.Stderr, "After approving, the browser redirects to %s\n", req.RedirectURI)
	fmt.Fprintf(os.Stderr, "The page will fail to load (nothing is listening there) — that is expected.\n")
	fmt.Fprintf(os.Stderr, "Copy the FULL address bar URL and paste it here.\n\n")
	fmt.Fprint(os.Stderr, "Redirect URL (or code): ")

	line, err := readLine(ctx, os.Stdin)
	if err != nil {
		return nil, err
	}

	res, err := oauth.ParseRedirectInput(line, state)
	if err != nil {
		return nil, err
	}
	// The exchange must use the same redirect_uri that started the flow, not
	// whatever host the user's browser happened to show.
	res.RedirectURI = req.RedirectURI
	return res, nil
}

func readLine(ctx context.Context, f *os.File) (string, error) {
	type lineResult struct {
		line string
		err  error
	}
	ch := make(chan lineResult, 1)
	go func() {
		line, err := bufio.NewReader(f).ReadString('\n')
		ch <- lineResult{line: strings.TrimSpace(line), err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && r.line == "" {
			return "", fmt.Errorf("read input: %w", r.err)
		}
		if r.line == "" {
			return "", errors.New("no input provided")
		}
		return r.line, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

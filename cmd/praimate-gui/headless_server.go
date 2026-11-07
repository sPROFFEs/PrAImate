//go:build server

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/sPROFFEs/PrAImate/internal/store"
)

func editionCommand(args []string) (bool, int) {
	if len(args) == 0 || args[0] != "--server" {
		return false, 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return true, runHeadlessServer(ctx, args[1:], os.Stdin, os.Stdout, os.Stderr)
}
func runHeadlessServer(parent context.Context, args []string, input io.Reader, output, stderr io.Writer) int {
	flags := flag.NewFlagSet("praimate server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "", "Bind address (default 127.0.0.1:8844; external addresses require TLS)")
	database := flags.String("db", "", "Encrypted database path (default: current profile)")
	cert := flags.String("tls-cert", "", "TLS certificate path")
	key := flags.String("tls-key", "", "TLS private key path")
	passwordStdin := flags.Bool("db-password-stdin", false, "Read the database password from piped stdin")
	passwordFile := flags.String("db-password-file", os.Getenv("PRAIMATE_DB_PASSWORD_FILE"), "Read the database password from a private service credential file")
	initialize := flags.Bool("initialize-db", false, "Initialize a database password using the provided password")
	pairing := flags.Bool("pair", false, "Print a one-use, five-minute owner pairing code to this console")
	publicURL := flags.String("public-url", "", "HTTPS hostname configured in Cloudflare Tunnel")
	connectorTokenFile := flags.String("cloudflare-token-file", "", "Private file containing only the tunnel connector token (saved encrypted)")
	connectorAction := flags.String("cloudflare-service", "", "Connector operation at startup: install, start, stop, restart, uninstall or status")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "Unexpected server arguments")
		return 2
	}
	if *connectorAction != "" && !map[string]bool{"install": true, "start": true, "stop": true, "restart": true, "uninstall": true, "status": true}[*connectorAction] {
		fmt.Fprintln(stderr, "Unknown Cloudflare connector operation")
		return 2
	}
	password := os.Getenv("PRAIMATE_DB_PASSWORD")
	_ = os.Unsetenv("PRAIMATE_DB_PASSWORD")
	_ = os.Unsetenv("PRAIMATE_DB_PASSWORD_FILE")
	if *passwordFile != "" && *passwordStdin {
		fmt.Fprintln(stderr, "Choose one database password input")
		return 2
	}
	if *passwordFile != "" {
		info, err := os.Stat(*passwordFile)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
			fmt.Fprintln(stderr, "Cannot read the database credential file")
			return 1
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			fmt.Fprintln(stderr, "Database credential file must be private to its owner")
			return 1
		}
		body, err := os.ReadFile(*passwordFile)
		if err != nil {
			fmt.Fprintln(stderr, "Cannot read database credential")
			return 1
		}
		password = strings.TrimRight(string(body), "\r\n")
	}
	if *passwordStdin {
		if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			fmt.Fprint(stderr, "Database password: ")
			raw, err := term.ReadPassword(int(file.Fd()))
			fmt.Fprintln(stderr)
			if err != nil {
				fmt.Fprintln(stderr, "Cannot read database password")
				return 1
			}
			password = string(raw)
		} else {
			line, err := bufio.NewReader(io.LimitReader(input, 4097)).ReadString('\n')
			if err != nil && err != io.EOF {
				fmt.Fprintln(stderr, "Cannot read database password")
				return 1
			}
			password = strings.TrimRight(line, "\r\n")
			if len(password) > 4096 {
				fmt.Fprintln(stderr, "Database password exceeds limit")
				return 2
			}
		}
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a := NewApp()
	a.headless = true
	a.ctx = context.WithValue(ctx, eventOwnerKey{}, a)
	a.emit = func(context.Context, string, ...any) {}
	a.quit = func(context.Context) { cancel() }
	if *listen != "" {
		a.edition.config.Listen = *listen
	}
	if a.edition.config.Listen == "" {
		a.edition.config.Listen = "127.0.0.1:8844"
	}
	if *cert != "" {
		a.edition.config.TLSCert = *cert
	}
	if *key != "" {
		a.edition.config.TLSKey = *key
	}
	path := *database
	if path == "" {
		var err error
		path, err = store.DefaultDBPath()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	a.dbPath = path
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = a.edition.openOwner(path); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer a.edition.releaseOwner()
	refreshManagedPaths()
	var st *store.Store
	if *initialize {
		st, err = store.InitializeWithPassword(path, password)
	} else if password != "" {
		st, err = store.OpenWithPassword(path, password)
	} else {
		st, err = store.Open(path)
	}
	password = ""
	if err != nil {
		fmt.Fprintln(stderr, "Cannot open the encrypted database:", err)
		return 1
	}
	if err = a.initializeUnlockedStore(a.ctx, st); err != nil {
		st.Close()
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer a.shutdown(context.Background())
	if a.edition.startError != nil {
		fmt.Fprintln(stderr, "Cannot start remote service:", a.edition.startError)
		return 1
	}
	if a.edition.gateway == nil {
		fmt.Fprintln(stderr, "Remote service was not started")
		return 1
	}
	if *publicURL != "" || *connectorTokenFile != "" {
		status, err := a.CloudflareStatus()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		url := *publicURL
		if url == "" {
			url = status.PublicURL
		}
		token := ""
		if *connectorTokenFile != "" {
			info, err := os.Stat(*connectorTokenFile)
			if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
				fmt.Fprintln(stderr, "Cloudflare token file must be a private regular file (0600), at most 16 KiB")
				return 1
			}
			body, err := os.ReadFile(*connectorTokenFile)
			if err != nil {
				fmt.Fprintln(stderr, "Cannot read Cloudflare connector token file")
				return 1
			}
			token = strings.TrimSpace(string(body))
		}
		raw, _ := json.Marshal(map[string]string{"publicURL": url, "token": token})
		if err = a.SaveCloudflareConfig(string(raw)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *connectorAction != "" {
		if *connectorAction != "status" {
			fmt.Fprintln(stderr, "Cloudflare connector:", *connectorAction)
			if err = a.CloudflareService(*connectorAction); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if *connectorAction == "install" {
				if err = a.CloudflareService("start"); err != nil {
					fmt.Fprintln(stderr, err)
					return 1
				}
			}
		}
		status, statusErr := a.CloudflareStatus()
		if statusErr != nil {
			fmt.Fprintln(stderr, statusErr)
			return 1
		}
		_ = json.NewEncoder(output).Encode(status)
	}
	if *pairing {
		code, err := a.edition.gateway.CreatePairing("Owner device", "admin")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(output, "Pairing code: %s (expires %s; administrator access)\n", code.Code, code.ExpiresAt.Format("15:04:05 MST"))
	}
	fmt.Fprintf(output, "PrAImate server ready on %s\n", a.edition.address)
	<-ctx.Done()
	a.edition.mu.Lock()
	restart := a.edition.restartRequested
	a.edition.mu.Unlock()
	if restart {
		return 75
	}
	return 0
}

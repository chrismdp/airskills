package cmd

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(whoamiCmd)
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in to airskills.ai",
	Long:  "Opens your browser to sign in with Google or Microsoft. No account needed — one is created automatically.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		// Start a temporary local server to receive the token
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("failed to start local server: %w", err)
		}
		port := listener.Addr().(*net.TCPAddr).Port

		tokenCh := make(chan *config.TokenData, 1)
		errCh := make(chan error, 1)

		mux := http.NewServeMux()
		mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
			accessToken := r.URL.Query().Get("access_token")
			refreshToken := r.URL.Query().Get("refresh_token")
			expiresAt := r.URL.Query().Get("expires_at")

			if accessToken == "" {
				errCh <- fmt.Errorf("no token received from server")
				fmt.Fprintf(w, "<html><body><h1>Login failed</h1><p>No token received. Please try again.</p></body></html>")
				return
			}

			var exp int64
			fmt.Sscanf(expiresAt, "%d", &exp)

			tokenCh <- &config.TokenData{
				AccessToken:  accessToken,
				RefreshToken: refreshToken,
				ExpiresAt:    exp,
			}

			fmt.Fprintf(w, "<html><body><h1>Logged in to airskills!</h1><p>You can close this tab and return to your terminal.</p></body></html>")
		})

		server := &http.Server{Handler: mux}
		go server.Serve(listener)
		defer server.Shutdown(context.Background())

		loginURL := fmt.Sprintf("%s/auth/cli?port=%d", cfg.APIURL, port)

		fmt.Printf("Open this URL to login:\n\n  %s\n\n", loginURL)
		fmt.Println("If the browser doesn't redirect automatically, paste the code shown on the page:")

		// Try to open browser
		openBrowser(loginURL)

		// Read stdin for pasted code (base64-encoded token JSON)
		go func() {
			reader := bufio.NewReader(os.Stdin)
			for {
				line, _ := reader.ReadString('\n')
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				// Try base64 decode first (the web page shows base64)
				if decoded, err := base64Decode(line); err == nil {
					var token config.TokenData
					if err := json.Unmarshal(decoded, &token); err == nil && token.AccessToken != "" {
						tokenCh <- &token
						return
					}
				}
				// Try raw JSON
				var token config.TokenData
				if err := json.Unmarshal([]byte(line), &token); err == nil && token.AccessToken != "" {
					tokenCh <- &token
					return
				}
				fmt.Println("Invalid code — try again, or paste the full code from the web page:")
			}
		}()

		// Wait for callback or timeout
		select {
		case token := <-tokenCh:
			// Validate token expiry before saving
			if token.ExpiresAt > 0 && time.Now().Unix() > token.ExpiresAt {
				return fmt.Errorf("received token is already expired (expires_at=%d) — this is a server bug, please report it", token.ExpiresAt)
			}
			if err := config.SaveToken(token); err != nil {
				return fmt.Errorf("failed to save token: %w", err)
			}
			// Verify token actually works
			client, err := newAPIClientAuto()
			if err != nil {
				logInfo("Logged in, but could not verify session: %s", err)
				return nil
			}
			if _, err := client.get("/api/v1/me"); err != nil {
				return fmt.Errorf("token saved but verification failed: %w", err)
			}
			logInfo("Logged in successfully!")
			return nil
		case err := <-errCh:
			return err
		case <-time.After(5 * time.Minute):
			return fmt.Errorf("login timed out — try again")
		}
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out of airskills",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.ClearToken(); err != nil {
			return err
		}
		fmt.Println("Logged out.")
		return nil
	},
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show current user",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClientAuto()
		if err != nil {
			return err
		}

		body, err := client.get("/api/v1/me")
		if err != nil {
			return err
		}

		var profile struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
		}
		if err := json.Unmarshal(body, &profile); err != nil {
			return err
		}

		name := profile.DisplayName
		if name == "" {
			name = profile.Username
		}
		fmt.Printf("%s (@%s)\n", name, profile.Username)
		return nil
	},
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return fmt.Errorf("unsupported platform")
	}
}

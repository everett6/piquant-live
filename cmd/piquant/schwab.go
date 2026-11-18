package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"piquant-live/internal/carry"
	"piquant-live/internal/execution"
	"piquant-live/internal/trend"
)

func runSchwab(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("schwab", flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	envPath := fs.String("env", execution.DefaultSchwabEnvPath(), "Schwab credentials file (SCHWAB_APP_KEY, SCHWAB_APP_SECRET, SCHWAB_REFRESH_TOKEN)")
	dir := fs.String("dir", filepath.Join(home, "piquant-trend"), "trend state directory")
	carryDir := fs.String("carry-dir", filepath.Join(home, "piquant-collect", "carry"), "carry state directory")
	dtb3Path := fs.String("dtb3", filepath.Join(home, "piquant-data", "macro", "DTB3.csv"), "FRED DTB3 CSV path")
	authURL := fs.Bool("auth-url", false, "print the Schwab v1 OAuth2 consent URL and exit")
	exchangeCode := fs.String("exchange-code", "", "exchange a Schwab OAuth2 authorization code (or full redirect URL?code=...)")
	save := fs.Bool("save", false, "with --exchange-code, save refresh token to --env with 0600 permissions")
	clientIDFlag := fs.String("client-id", "", "override SCHWAB_APP_KEY")
	redirectFlag := fs.String("redirect", "", "override SCHWAB_REDIRECT_URI")
	portfolio := fs.String("portfolio", "q42_dualspeed", "target portfolio: q42_dualspeed | q42_balanced | s36_spot | s36_fut")
	navUSD := fs.Float64("nav", 0, "account NAV USD (0 = use Schwab liquidation value when connected, else $1000)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	creds := execution.LoadSchwabCredentials(*envPath)
	if strings.TrimSpace(*clientIDFlag) != "" {
		creds.ClientID = strings.TrimSpace(*clientIDFlag)
	}
	if strings.TrimSpace(*redirectFlag) != "" {
		creds.RedirectURI = strings.TrimSpace(*redirectFlag)
	}

	if *authURL {
		if strings.TrimSpace(creds.ClientID) == "" {
			return fmt.Errorf("SCHWAB_APP_KEY (or --client-id) is required to build the OAuth URL")
		}
		fmt.Println(execution.SchwabAuthorizeURL(creds.ClientID, creds.RedirectURI))
		return nil
	}

	if code := strings.TrimSpace(*exchangeCode); code != "" {
		// Accept either a raw authorization code or the full callback URL containing ?code=...
		if i := strings.Index(code, "code="); i >= 0 {
			code = code[i+5:]
			if amp := strings.IndexByte(code, '&'); amp >= 0 {
				code = code[:amp]
			}
		}
		client := &http.Client{Timeout: 12 * time.Second}
		tok, err := execution.ExchangeSchwabAuthCode(ctx, client, "", creds.ClientID, creds.ClientSecret, code, creds.RedirectURI)
		if err != nil {
			return err
		}
		if *save {
			creds.RefreshToken = tok.RefreshToken
			if err := execution.SaveSchwabCredentials(*envPath, creds); err != nil {
				return err
			}
		}
		out := map[string]any{
			"status":        "ok",
			"token_type":    tok.TokenType,
			"expires_in":    tok.ExpiresIn,
			"scope":         tok.Scope,
			"saved_to_env":  *save,
			"has_refresh":   strings.TrimSpace(tok.RefreshToken) != "",
			"next_step_cli": "piquant schwab --portfolio " + *portfolio,
		}
		b, _ := json.MarshalIndent(out, "", " ")
		fmt.Println(string(b))
		return nil
	}

	st, err := trend.Load(filepath.Join(*dir, "trend_state.json"))
	if err != nil {
		return err
	}
	if st == nil {
		st = trend.NewState("")
	}
	cState, _ := carry.Load(filepath.Join(*carryDir, "carry_state.json"))
	rates, _ := trend.LoadDTB3Rates(*dtb3Path)
	blend, ok := trend.SelectBlend(st, cState, rates, *portfolio, nil)
	if !ok {
		return fmt.Errorf("portfolio %q unavailable in trend state", *portfolio)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	plan := execution.BuildSchwabPlanFromTargets(
		ctx, client, creds, "", "", "",
		blend.Name, blend.TargetBTCPortPct, blend.TargetETHPortPct, blend.IdleTrendCashPct, blend.CarrySleevePct, *navUSD,
	)
	b, err := json.MarshalIndent(plan, "", " ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

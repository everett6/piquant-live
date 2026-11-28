package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"piquant-live/internal/execution"
	"piquant-live/internal/trend"
	"piquant-live/internal/trendcache"
)

func runSchwab(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("schwab", flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	envPath := fs.String("env", execution.DefaultSchwabEnvPath(), "Schwab credentials file (SCHWAB_APP_KEY, SCHWAB_APP_SECRET, SCHWAB_REFRESH_TOKEN)")
	dir := fs.String("dir", defaultTrendDir(), "trend state directory")
	carryDir := fs.String("carry-dir", filepath.Join(home, "piquant-collect", "carry"), "carry state directory")
	dtb3Path := fs.String("dtb3", filepath.Join(home, "piquant-data", "macro", "DTB3.csv"), "FRED DTB3 CSV path")
	equitiesDir := fs.String("equities-dir", trend.DefaultEquitiesDir(), "US equities/macro CSV directory for q57_full_fund")
	authURL := fs.Bool("auth-url", false, "print the Schwab v1 OAuth2 consent URL and exit")
	exchangeCode := fs.String("exchange-code", "", "exchange a Schwab OAuth2 authorization code (or full redirect URL?code=...)")
	save := fs.Bool("save", false, "with --exchange-code, save refresh token to --env with 0600 permissions")
	clientIDFlag := fs.String("client-id", "", "override SCHWAB_APP_KEY")
	redirectFlag := fs.String("redirect", "", "override SCHWAB_REDIRECT_URI")
	portfolio := fs.String("portfolio", "q42_dualspeed", "target portfolio: q57_full_fund | q57_hrp_hurst | q56_quantum_fast | q51_volparity | q42_dualspeed | q42_balanced | s36_spot | s36_fut")
	navUSD := fs.Float64("nav", 0, "account NAV USD (0 = use Schwab liquidation value when connected, else $1000)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !isFinite(*navUSD) || *navUSD < 0 {
		return fmt.Errorf("--nav must be a finite value >= 0 (got %v)", *navUSD)
	}
	sel := strings.ToLower(strings.TrimSpace(*portfolio))
	if sel != "q57_full_fund" && sel != "q57_full" && sel != "q57_macro" {
		if _, ok := knownPortfolioSelectors[sel]; !ok {
			return fmt.Errorf("unknown portfolio %q", *portfolio)
		}
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
		client := execution.NewOptimizedHTTPClient(12 * time.Second)
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
		b, merr := json.MarshalIndent(out, "", " ")
		if merr != nil {
			return merr
		}
		fmt.Println(string(b))
		return nil
	}

	// Use shared trend cache for consistent caching across CLI tools
	trendCache := trendcache.NewTrendCache()

	st, err := trendCache.GetTrendState(filepath.Join(*dir, "trend_state.json"))
	if err != nil {
		return err
	}
	if st == nil {
		st = trend.NewState("")
	}
	cState, cerr := trendCache.GetCarryState(filepath.Join(*carryDir, "carry_state.json"))
	if cerr != nil && !errors.Is(cerr, os.ErrNotExist) {
		return fmt.Errorf("load carry state: %w", cerr)
	}
	rates, rerr := trendCache.GetDTB3Rates(*dtb3Path)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return fmt.Errorf("load DTB3 rates: %w", rerr)
	}

	client := execution.NewOptimizedHTTPClient(10 * time.Second)
	if sel == "q57_full_fund" || sel == "q57_full" || sel == "q57_macro" {
		macroSum, hasMacro := trendCache.GetMacroHRP(*equitiesDir, *dtb3Path, rates)
		var full trend.FullFundSummary
		var ok bool
		if hasMacro {
			full, ok = trend.ComputeQ57FullFundWithMacro(st, cState, rates, macroSum, nil)
		}
		if !ok {
			return fmt.Errorf("q57_full_fund unavailable (check equities CSVs in %s)", *equitiesDir)
		}
		specs := BuildSchwabTargetsFromFullFund(full)
		plan := execution.BuildSchwabMultiAssetPlan(
			ctx, client, creds, "", "", "",
			full.Name, specs, full.CarrySleevePct, *navUSD,
		)
		out := struct {
			FullFund trend.FullFundSummary       `json:"q57_full_fund"`
			Schwab   execution.SchwabPortfolioPlan `json:"schwab"`
		}{
			FullFund: full,
			Schwab:   plan,
		}
		b, merr := json.MarshalIndent(out, "", " ")
		if merr != nil {
			return merr
		}
		fmt.Println(string(b))
		return nil
	}

	blend, ok := trend.SelectBlend(st, cState, rates, *portfolio, nil)
	if !ok {
		return fmt.Errorf("portfolio %q unavailable in trend state", *portfolio)
	}

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

// BuildSchwabTargetsFromFullFund maps Q57_full_multi_asset_fund_1x sleeve percentages into Schwab ETF/equity targets.
func BuildSchwabTargetsFromFullFund(full trend.FullFundSummary) []execution.SchwabTargetSpec {
	specs := make([]execution.SchwabTargetSpec, 0, len(full.MacroTargets)+3)
	specs = append(specs,
		execution.SchwabTargetSpec{Symbol: "IBIT", Role: "BTC Spot Proxy (Q57 Crypto)", TargetPct: full.CryptoBTCPortPct},
		execution.SchwabTargetSpec{Symbol: "ETHA", Role: "ETH Spot Proxy (Q57 Crypto)", TargetPct: full.CryptoETHPortPct},
	)
	for _, mt := range full.MacroTargets {
		if mt.TargetPct > 0 {
			specs = append(specs, execution.SchwabTargetSpec{
				Symbol:    mt.Symbol,
				Role:      mt.Role + " (Q57 Macro HRP)",
				TargetPct: mt.TargetPct,
			})
		}
	}
	specs = append(specs, execution.SchwabTargetSpec{
		Symbol:    "SGOV",
		Role:      "Idle Crypto + Macro T-Bills (0-3m)",
		TargetPct: full.IdleSGOVPct,
	})
	return specs
}

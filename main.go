package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/brunomvsouza/ynab.go/api"
	ynabtx "github.com/brunomvsouza/ynab.go/api/transaction"
	up "github.com/jaydenthomson-mantel/up"
)

const (
	upAPIBaseURL       = "https://api.up.com.au/api/v1"
	defaultBatchSize   = 100
	defaultHTTPTimeout = 30 * time.Second
)

type config struct {
	ConfigPath     string           `json:"-"`
	UpToken        string           `json:"up_token"`
	YNABToken      string           `json:"ynab_token"`
	UpAccountID    string           `json:"up_account_id"`
	YNABBudgetID   string           `json:"ynab_budget_id"`
	YNABAccountID  string           `json:"ynab_account_id"`
	Accounts       []accountMapping `json:"accounts"`
	Since          string           `json:"since"`
	DryRun         bool             `json:"dry_run"`
	IncludePending bool             `json:"include_pending"`
	BatchSize      int              `json:"batch_size"`
	Debug          bool             `json:"debug"`
}

type accountMapping struct {
	Name          string `json:"name"`
	UpAccountID   string `json:"up_account_id"`
	YNABAccountID string `json:"ynab_account_id"`
}

type upTransaction struct {
	ID         string `json:"id"`
	Attributes struct {
		Status      string     `json:"status"`
		RawText     *string    `json:"rawText"`
		Description string     `json:"description"`
		Message     *string    `json:"message"`
		CreatedAt   time.Time  `json:"createdAt"`
		SettledAt   *time.Time `json:"settledAt"`
		Amount      struct {
			ValueInBaseUnits int64  `json:"valueInBaseUnits"`
			Value            string `json:"value"`
			CurrencyCode     string `json:"currencyCode"`
		} `json:"amount"`
	} `json:"attributes"`
	Relationships struct {
		TransferAccount struct {
			Data *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"data"`
		} `json:"transferAccount"`
	} `json:"relationships"`
}

type upTransactionsPage struct {
	Data  []upTransaction `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

type upTransactionClient struct {
	baseURL    string
	httpClient *http.Client
}

func main() {
	cfg, err := readConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx := context.Background()
	if err := run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "sync failed:", err)
		os.Exit(1)
	}
}

func readConfig() (config, error) {
	cfg := config{
		UpToken:       os.Getenv("UP_TOKEN"),
		YNABToken:     os.Getenv("YNAB_TOKEN"),
		UpAccountID:   os.Getenv("UP_ACCOUNT_ID"),
		YNABBudgetID:  os.Getenv("YNAB_BUDGET_ID"),
		YNABAccountID: os.Getenv("YNAB_ACCOUNT_ID"),
		BatchSize:     defaultBatchSize,
	}

	flag.StringVar(&cfg.ConfigPath, "config", "", "path to JSON config file")
	flag.StringVar(&cfg.UpToken, "up-token", cfg.UpToken, "Up API token, or UP_TOKEN")
	flag.StringVar(&cfg.YNABToken, "ynab-token", cfg.YNABToken, "YNAB API token, or YNAB_TOKEN")
	flag.StringVar(&cfg.UpAccountID, "up-account-id", cfg.UpAccountID, "Up account ID, or UP_ACCOUNT_ID")
	flag.StringVar(&cfg.YNABBudgetID, "ynab-budget-id", cfg.YNABBudgetID, "YNAB budget ID, or YNAB_BUDGET_ID")
	flag.StringVar(&cfg.YNABAccountID, "ynab-account-id", cfg.YNABAccountID, "YNAB account ID, or YNAB_ACCOUNT_ID")
	flag.StringVar(&cfg.Since, "since", cfg.Since, "only sync Up transactions created since YYYY-MM-DD")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "print planned YNAB transactions without creating them")
	flag.BoolVar(&cfg.IncludePending, "include-pending", false, "include unsettled Up transactions")
	flag.IntVar(&cfg.BatchSize, "batch-size", cfg.BatchSize, "YNAB transaction create batch size")
	flag.BoolVar(&cfg.Debug, "debug", false, "print YNAB transaction create request summaries and raw responses")
	flag.Parse()

	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

	if cfg.ConfigPath == "" {
		return cfg, nil
	}

	fileCfg, err := loadConfigFile(cfg.ConfigPath)
	if err != nil {
		return config{}, err
	}
	return mergeConfig(fileCfg, cfg, setFlags), nil
}

func loadConfigFile(path string) (config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return config{}, fmt.Errorf("read config file %q: %w", path, err)
	}

	cfg := config{BatchSize: defaultBatchSize}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return config{}, fmt.Errorf("parse config file %q: %w", path, err)
	}
	return cfg, nil
}

func mergeConfig(fileCfg, flagCfg config, setFlags map[string]bool) config {
	merged := fileCfg
	merged.ConfigPath = flagCfg.ConfigPath

	if setFlags["up-token"] || merged.UpToken == "" {
		merged.UpToken = flagCfg.UpToken
	}
	if setFlags["ynab-token"] || merged.YNABToken == "" {
		merged.YNABToken = flagCfg.YNABToken
	}
	if setFlags["ynab-budget-id"] || merged.YNABBudgetID == "" {
		merged.YNABBudgetID = flagCfg.YNABBudgetID
	}
	if setFlags["since"] || merged.Since == "" {
		merged.Since = flagCfg.Since
	}
	if setFlags["batch-size"] || merged.BatchSize == 0 {
		merged.BatchSize = flagCfg.BatchSize
	}

	if setFlags["dry-run"] {
		merged.DryRun = flagCfg.DryRun
	}
	if setFlags["include-pending"] {
		merged.IncludePending = flagCfg.IncludePending
	}
	if setFlags["debug"] {
		merged.Debug = flagCfg.Debug
	}

	if (setFlags["up-account-id"] || setFlags["ynab-account-id"] || len(merged.Accounts) == 0) && flagCfg.UpAccountID != "" && flagCfg.YNABAccountID != "" {
		merged.UpAccountID = flagCfg.UpAccountID
		merged.YNABAccountID = flagCfg.YNABAccountID
		if setFlags["up-account-id"] || setFlags["ynab-account-id"] {
			merged.Accounts = nil
		}
	}

	return merged
}

func (cfg config) Validate() error {
	var missing []string
	required := map[string]string{
		"UP_TOKEN":       cfg.UpToken,
		"YNAB_BUDGET_ID": cfg.YNABBudgetID,
	}
	if !cfg.DryRun {
		required["YNAB_TOKEN"] = cfg.YNABToken
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	if len(cfg.accountMappings()) == 0 {
		return errors.New("missing account mappings: set accounts in the config file or UP_ACCOUNT_ID and YNAB_ACCOUNT_ID")
	}
	for i, account := range cfg.accountMappings() {
		prefix := fmt.Sprintf("account mapping %d", i+1)
		if strings.TrimSpace(account.UpAccountID) == "" {
			return fmt.Errorf("%s is missing up_account_id", prefix)
		}
		if strings.TrimSpace(account.YNABAccountID) == "" {
			return fmt.Errorf("%s is missing ynab_account_id", prefix)
		}
	}
	if cfg.Since != "" {
		if _, err := normalizeSince(cfg.Since); err != nil {
			return err
		}
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > 100 {
		return errors.New("-batch-size must be between 1 and 100")
	}
	return nil
}

func (cfg config) accountMappings() []accountMapping {
	if len(cfg.Accounts) > 0 {
		return cfg.Accounts
	}
	if cfg.UpAccountID == "" && cfg.YNABAccountID == "" {
		return nil
	}
	return []accountMapping{
		{
			UpAccountID:   cfg.UpAccountID,
			YNABAccountID: cfg.YNABAccountID,
		},
	}
}

func run(ctx context.Context, cfg config) error {
	upClient := up.NewClient()
	if _, err := upClient.GetAccounts(cfg.UpToken, &up.PaginationParams{PageSize: "1"}); err != nil {
		return fmt.Errorf("check Up token with up library: %w", err)
	}

	transactionClient := newUpTransactionClient()
	var batches []accountPayloads
	for _, account := range cfg.accountMappings() {
		transactions, err := transactionClient.GetTransactions(ctx, cfg.UpToken, account.UpAccountID, cfg.Since)
		if err != nil {
			return fmt.Errorf("fetch Up transactions for %s: %w", account.Label(), err)
		}

		payloads, err := buildYNABPayloads(transactions, cfg, account)
		if err != nil {
			return err
		}
		batches = append(batches, accountPayloads{
			Account:  account,
			Payloads: payloads,
		})
	}

	if cfg.DryRun {
		return printDryRun(batches)
	}
	if totalPayloads(batches) == 0 {
		fmt.Println("No transactions to sync.")
		return nil
	}

	ynabClient := newYNABTransactionClient(cfg.YNABToken, cfg.Debug)
	created, duplicates, err := createTransactions(ynabClient, cfg, batches)
	if err != nil {
		return err
	}

	fmt.Printf("Synced %d transaction(s); YNAB reported %d duplicate import_id(s).\n", created, duplicates)
	return nil
}

type accountPayloads struct {
	Account  accountMapping
	Payloads []ynabPayloadTransaction
}

type ynabPayloadTransaction struct {
	ID                string                `json:"id"`
	AccountID         string                `json:"account_id"`
	Date              api.Date              `json:"date"`
	Amount            int64                 `json:"amount"`
	Cleared           ynabtx.ClearingStatus `json:"cleared"`
	Approved          bool                  `json:"approved"`
	PayeeID           *string               `json:"payee_id"`
	PayeeName         *string               `json:"payee_name"`
	CategoryID        *string               `json:"category_id"`
	Memo              *string               `json:"memo"`
	FlagColor         *ynabtx.FlagColor     `json:"flag_color"`
	ImportID          *string               `json:"import_id"`
	TransferAccountID *string               `json:"-"`
}

func (a accountMapping) Label() string {
	if a.Name != "" {
		return a.Name
	}
	if a.UpAccountID != "" {
		return a.UpAccountID
	}
	return "unnamed account"
}

func newUpTransactionClient() *upTransactionClient {
	return &upTransactionClient{
		baseURL: upAPIBaseURL,
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
	}
}

func (c *upTransactionClient) GetTransactions(ctx context.Context, token, accountID, since string) ([]upTransaction, error) {
	firstURL, err := url.Parse(fmt.Sprintf("%s/accounts/%s/transactions", c.baseURL, url.PathEscape(accountID)))
	if err != nil {
		return nil, err
	}
	query := firstURL.Query()
	query.Set("page[size]", "100")
	if since != "" {
		normalizedSince, err := normalizeSince(since)
		if err != nil {
			return nil, err
		}
		query.Set("filter[since]", normalizedSince)
	}
	firstURL.RawQuery = query.Encode()

	var out []upTransaction
	nextURL := firstURL.String()
	for nextURL != "" {
		page, err := c.getPage(ctx, token, nextURL)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Data...)
		nextURL = page.Links.Next
	}
	return out, nil
}

func normalizeSince(since string) (string, error) {
	if since == "" {
		return "", nil
	}
	if date, err := time.Parse(time.DateOnly, since); err == nil {
		return date.UTC().Format(time.RFC3339), nil
	}
	if timestamp, err := time.Parse(time.RFC3339, since); err == nil {
		return timestamp.UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("invalid -since value %q: use YYYY-MM-DD or RFC3339 like 2026-06-20T00:00:00Z", since)
}

func (c *upTransactionClient) getPage(ctx context.Context, token, pageURL string) (*upTransactionsPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Up API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var page upTransactionsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func buildYNABPayloads(transactions []upTransaction, cfg config, account accountMapping) ([]ynabPayloadTransaction, error) {
	payloads := make([]ynabPayloadTransaction, 0, len(transactions))
	mappedAccounts := ynabAccountIDsByUpAccountID(cfg.accountMappings())
	for _, tx := range transactions {
		if tx.ID == "" {
			continue
		}
		if !cfg.IncludePending && !strings.EqualFold(tx.Attributes.Status, "SETTLED") {
			continue
		}

		date := tx.Attributes.CreatedAt
		cleared := ynabtx.ClearingStatusUncleared
		if tx.Attributes.SettledAt != nil {
			date = *tx.Attributes.SettledAt
			cleared = ynabtx.ClearingStatusCleared
		}

		ynabDate, err := api.DateFromString(date.Format(time.DateOnly))
		if err != nil {
			return nil, fmt.Errorf("convert date for Up transaction %s: %w", tx.ID, err)
		}

		payeeName := payeeFor(tx)
		memo := memoFor(tx)
		importID := importIDForUpTransaction(tx.ID)
		amount := tx.Attributes.Amount.ValueInBaseUnits * 10

		payload := ynabPayloadTransaction{
			AccountID: account.YNABAccountID,
			Date:      ynabDate,
			Amount:    amount,
			Cleared:   cleared,
			Approved:  false,
			Memo:      &memo,
			ImportID:  &importID,
		}

		if transferAccountID, ok := transferYNABAccountID(tx, mappedAccounts); ok {
			if amount > 0 {
				continue
			}
			payload.TransferAccountID = &transferAccountID
		} else {
			payload.PayeeName = &payeeName
		}

		payloads = append(payloads, payload)
	}
	return payloads, nil
}

func ynabAccountIDsByUpAccountID(accounts []accountMapping) map[string]string {
	ids := make(map[string]string, len(accounts))
	for _, account := range accounts {
		ids[account.UpAccountID] = account.YNABAccountID
	}
	return ids
}

func transferYNABAccountID(tx upTransaction, mappedAccounts map[string]string) (string, bool) {
	if tx.Relationships.TransferAccount.Data == nil {
		return "", false
	}
	ynabAccountID, ok := mappedAccounts[tx.Relationships.TransferAccount.Data.ID]
	return ynabAccountID, ok
}

func payeeFor(tx upTransaction) string {
	if tx.Attributes.Description != "" {
		return tx.Attributes.Description
	}
	if tx.Attributes.RawText != nil && *tx.Attributes.RawText != "" {
		return *tx.Attributes.RawText
	}
	return "Up transaction"
}

func memoFor(tx upTransaction) string {
	parts := []string{"Up ID: " + tx.ID}
	if tx.Attributes.Message != nil && *tx.Attributes.Message != "" {
		parts = append(parts, "Message: "+*tx.Attributes.Message)
	}
	if tx.Attributes.RawText != nil && *tx.Attributes.RawText != "" && *tx.Attributes.RawText != tx.Attributes.Description {
		parts = append(parts, "Raw: "+*tx.Attributes.RawText)
	}
	return strings.Join(parts, " | ")
}

func importIDForUpTransaction(id string) string {
	sum := sha256.Sum256([]byte("up:" + id))
	return "UP:" + hex.EncodeToString(sum[:])[:33]
}

func printDryRun(batches []accountPayloads) error {
	if totalPayloads(batches) == 0 {
		fmt.Println("No transactions to sync.")
		return nil
	}

	for _, batch := range batches {
		fmt.Printf("%s (%s -> %s)\n", batch.Account.Label(), batch.Account.UpAccountID, batch.Account.YNABAccountID)
		for i, payload := range batch.Payloads {
			payee := "Transfer"
			if payload.PayeeName != nil {
				payee = *payload.PayeeName
			} else if payload.TransferAccountID != nil {
				payee = "Transfer to " + *payload.TransferAccountID
			}
			importID := ""
			if payload.ImportID != nil {
				importID = *payload.ImportID
			}
			fmt.Printf("%d. %s %s %s import_id=%s\n",
				i+1,
				api.DateFormat(payload.Date),
				milliunitsToDecimal(payload.Amount),
				payee,
				importID,
			)
		}
	}
	return nil
}

func totalPayloads(batches []accountPayloads) int {
	total := 0
	for _, batch := range batches {
		total += len(batch.Payloads)
	}
	return total
}

func milliunitsToDecimal(amount int64) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	dollars := amount / 1000
	cents := (amount % 1000) / 10
	return sign + strconv.FormatInt(dollars, 10) + "." + fmt.Sprintf("%02d", cents)
}

type ynabTransactionCreator interface {
	CreateTransactions(ctx context.Context, budgetID string, payloads []ynabPayloadTransaction) (*ynabtx.OperationSummary, error)
}

type ynabTransactionClient struct {
	accessToken string
	baseURL     string
	httpClient  *http.Client
	debug       bool
	payeeCache  map[string]string
}

func newYNABTransactionClient(accessToken string, debug bool) *ynabTransactionClient {
	return &ynabTransactionClient{
		accessToken: accessToken,
		baseURL:     "https://api.youneedabudget.com/v1",
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		debug:      debug,
		payeeCache: map[string]string{},
	}
}

func (c *ynabTransactionClient) CreateTransactions(ctx context.Context, budgetID string, payloads []ynabPayloadTransaction) (*ynabtx.OperationSummary, error) {
	prepared, err := c.prepareTransactions(ctx, budgetID, payloads)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(struct {
		Transactions []ynabPayloadTransaction `json:"transactions"`
	}{
		Transactions: prepared,
	})
	if err != nil {
		return nil, err
	}
	if c.debug {
		printYNABRequestDebug(payloads, prepared)
	}

	endpoint := fmt.Sprintf("%s/budgets/%s/transactions", c.baseURL, url.PathEscape(budgetID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if c.debug {
		fmt.Fprintf(os.Stderr, "YNAB response %s: %s\n", resp.Status, strings.TrimSpace(string(respBody)))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("YNAB API returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	response := struct {
		Data *ynabtx.OperationSummary `json:"data"`
	}{}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, errors.New("YNAB API response did not include transaction summary")
	}
	return response.Data, nil
}

func (c *ynabTransactionClient) prepareTransactions(ctx context.Context, budgetID string, payloads []ynabPayloadTransaction) ([]ynabPayloadTransaction, error) {
	prepared := make([]ynabPayloadTransaction, len(payloads))
	copy(prepared, payloads)

	for i := range prepared {
		if prepared[i].TransferAccountID == nil {
			continue
		}

		payeeID, err := c.transferPayeeID(ctx, budgetID, *prepared[i].TransferAccountID)
		if err != nil {
			return nil, err
		}
		prepared[i].PayeeID = &payeeID
		prepared[i].PayeeName = nil
		prepared[i].CategoryID = nil
	}

	return prepared, nil
}

func (c *ynabTransactionClient) transferPayeeID(ctx context.Context, budgetID, transferAccountID string) (string, error) {
	if payeeID, ok := c.payeeCache[transferAccountID]; ok {
		return payeeID, nil
	}

	endpoint := fmt.Sprintf("%s/budgets/%s/payees", c.baseURL, url.PathEscape(budgetID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if c.debug {
		fmt.Fprintf(os.Stderr, "YNAB payees response %s\n", resp.Status)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("YNAB payees API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	response := struct {
		Data struct {
			Payees []struct {
				ID                string  `json:"id"`
				Name              string  `json:"name"`
				Deleted           bool    `json:"deleted"`
				TransferAccountID *string `json:"transfer_account_id"`
			} `json:"payees"`
		} `json:"data"`
	}{}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", err
	}

	for _, payee := range response.Data.Payees {
		if payee.Deleted || payee.TransferAccountID == nil {
			continue
		}
		c.payeeCache[*payee.TransferAccountID] = payee.ID
	}

	if payeeID, ok := c.payeeCache[transferAccountID]; ok {
		return payeeID, nil
	}
	return "", fmt.Errorf("could not find YNAB transfer payee for account %s", transferAccountID)
}

func printYNABRequestDebug(original, prepared []ynabPayloadTransaction) {
	fmt.Fprintln(os.Stderr, "YNAB create request:")
	for i := range prepared {
		importID := ""
		if prepared[i].ImportID != nil {
			importID = *prepared[i].ImportID
		}
		payee := ""
		if prepared[i].PayeeID != nil {
			payee = "payee_id=" + *prepared[i].PayeeID
		} else if prepared[i].PayeeName != nil {
			payee = "payee_name=" + *prepared[i].PayeeName
		}
		transfer := ""
		if original[i].TransferAccountID != nil {
			transfer = " transfer_account_id=" + *original[i].TransferAccountID
		}
		fmt.Fprintf(os.Stderr, "  %d. account_id=%s amount=%d import_id=%s %s%s\n", i+1, prepared[i].AccountID, prepared[i].Amount, importID, payee, transfer)
	}
}

func createTransactions(client ynabTransactionCreator, cfg config, batches []accountPayloads) (int, int, error) {
	created := 0
	duplicates := 0
	for _, batch := range batches {
		payloads := batch.Payloads
		for start := 0; start < len(payloads); start += cfg.BatchSize {
			end := start + cfg.BatchSize
			if end > len(payloads) {
				end = len(payloads)
			}
			summary, err := client.CreateTransactions(context.Background(), cfg.YNABBudgetID, payloads[start:end])
			if err != nil {
				return created, duplicates, fmt.Errorf("create YNAB transactions for %s %d-%d: %w", batch.Account.Label(), start+1, end, err)
			}
			created += len(summary.TransactionIDs)
			duplicates += len(summary.DuplicateImportIDs)
		}
	}
	return created, duplicates, nil
}

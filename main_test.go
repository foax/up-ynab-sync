package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brunomvsouza/ynab.go/api"
	ynabtx "github.com/brunomvsouza/ynab.go/api/transaction"
)

func TestBuildYNABPayloadsMapsSettledTransaction(t *testing.T) {
	raw := "THE COFFEE SHOP"
	message := "flat white"
	settledAt := time.Date(2026, 6, 25, 10, 30, 0, 0, time.UTC)
	tx := upTransaction{ID: "abc-123"}
	tx.Attributes.Status = "SETTLED"
	tx.Attributes.Description = "Coffee Shop"
	tx.Attributes.RawText = &raw
	tx.Attributes.Message = &message
	tx.Attributes.CreatedAt = time.Date(2026, 6, 24, 10, 30, 0, 0, time.UTC)
	tx.Attributes.SettledAt = &settledAt
	tx.Attributes.Amount.ValueInBaseUnits = -650

	payloads, err := buildYNABPayloads([]upTransaction{tx}, config{}, accountMapping{YNABAccountID: "ynab-account"})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 {
		t.Fatalf("expected 1 payload, got %d", len(payloads))
	}

	payload := payloads[0]
	if payload.AccountID != "ynab-account" {
		t.Fatalf("account ID = %q", payload.AccountID)
	}
	if got := api.DateFormat(payload.Date); got != "2026-06-25" {
		t.Fatalf("date = %q", got)
	}
	if payload.Amount != -6500 {
		t.Fatalf("amount = %d", payload.Amount)
	}
	if payload.Cleared != ynabtx.ClearingStatusCleared {
		t.Fatalf("cleared = %q", payload.Cleared)
	}
	if payload.PayeeName == nil || *payload.PayeeName != "Coffee Shop" {
		t.Fatalf("payee = %#v", payload.PayeeName)
	}
	if payload.ImportID == nil || len(*payload.ImportID) != 36 {
		t.Fatalf("import ID = %#v", payload.ImportID)
	}
}

func TestBuildYNABPayloadsSkipsPendingByDefault(t *testing.T) {
	tx := upTransaction{ID: "pending"}
	tx.Attributes.Status = "HELD"
	tx.Attributes.CreatedAt = time.Date(2026, 6, 24, 10, 30, 0, 0, time.UTC)
	tx.Attributes.Amount.ValueInBaseUnits = -100

	payloads, err := buildYNABPayloads([]upTransaction{tx}, config{}, accountMapping{YNABAccountID: "ynab-account"})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 0 {
		t.Fatalf("expected no payloads, got %d", len(payloads))
	}
}

func TestBuildYNABPayloadsCanIncludePending(t *testing.T) {
	tx := upTransaction{ID: "pending"}
	tx.Attributes.Status = "HELD"
	tx.Attributes.CreatedAt = time.Date(2026, 6, 24, 10, 30, 0, 0, time.UTC)
	tx.Attributes.Amount.ValueInBaseUnits = -100

	payloads, err := buildYNABPayloads([]upTransaction{tx}, config{
		IncludePending: true,
	}, accountMapping{YNABAccountID: "ynab-account"})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 {
		t.Fatalf("expected 1 payload, got %d", len(payloads))
	}
	if payloads[0].Cleared != ynabtx.ClearingStatusUncleared {
		t.Fatalf("cleared = %q", payloads[0].Cleared)
	}
}

func TestBuildYNABPayloadsMapsOutgoingInternalTransfer(t *testing.T) {
	settledAt := time.Date(2026, 6, 25, 10, 30, 0, 0, time.UTC)
	tx := upTransaction{ID: "transfer-out"}
	tx.Attributes.Status = "SETTLED"
	tx.Attributes.Description = "Transfer to Saver"
	tx.Attributes.CreatedAt = settledAt
	tx.Attributes.SettledAt = &settledAt
	tx.Attributes.Amount.ValueInBaseUnits = -10000
	setTransferAccount(&tx, "up-saver")

	cfg := config{
		Accounts: []accountMapping{
			{Name: "Spending", UpAccountID: "up-spending", YNABAccountID: "ynab-spending"},
			{Name: "Saver", UpAccountID: "up-saver", YNABAccountID: "ynab-saver"},
		},
	}
	payloads, err := buildYNABPayloads([]upTransaction{tx}, cfg, cfg.Accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 {
		t.Fatalf("expected 1 payload, got %d", len(payloads))
	}

	payload := payloads[0]
	if payload.TransferAccountID == nil || *payload.TransferAccountID != "ynab-saver" {
		t.Fatalf("transfer account ID = %#v", payload.TransferAccountID)
	}
	if payload.PayeeName != nil {
		t.Fatalf("expected no payee name for transfer, got %#v", *payload.PayeeName)
	}
	if payload.Amount != -100000 {
		t.Fatalf("amount = %d", payload.Amount)
	}
}

func TestBuildYNABPayloadsSkipsIncomingInternalTransfer(t *testing.T) {
	settledAt := time.Date(2026, 6, 25, 10, 30, 0, 0, time.UTC)
	tx := upTransaction{ID: "transfer-in"}
	tx.Attributes.Status = "SETTLED"
	tx.Attributes.Description = "Transfer from Spending"
	tx.Attributes.CreatedAt = settledAt
	tx.Attributes.SettledAt = &settledAt
	tx.Attributes.Amount.ValueInBaseUnits = 10000
	setTransferAccount(&tx, "up-spending")

	cfg := config{
		Accounts: []accountMapping{
			{Name: "Spending", UpAccountID: "up-spending", YNABAccountID: "ynab-spending"},
			{Name: "Saver", UpAccountID: "up-saver", YNABAccountID: "ynab-saver"},
		},
	}
	payloads, err := buildYNABPayloads([]upTransaction{tx}, cfg, cfg.Accounts[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 0 {
		t.Fatalf("expected incoming transfer to be skipped, got %d payloads", len(payloads))
	}
}

func TestImportIDForUpTransactionIsStableAndYNABLength(t *testing.T) {
	first := importIDForUpTransaction("same-id")
	second := importIDForUpTransaction("same-id")
	other := importIDForUpTransaction("other-id")

	if first != second {
		t.Fatal("import ID should be stable")
	}
	if first == other {
		t.Fatal("different Up IDs should produce different import IDs")
	}
	if len(first) != 36 {
		t.Fatalf("length = %d, want 36", len(first))
	}
}

func TestValidateAllowsDryRunWithoutYNABToken(t *testing.T) {
	cfg := config{
		UpToken:      "up-token",
		YNABBudgetID: "ynab-budget",
		DryRun:       true,
		BatchSize:    1,
		Accounts: []accountMapping{
			{UpAccountID: "up-account", YNABAccountID: "ynab-account"},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRequiresYNABTokenWhenSyncing(t *testing.T) {
	cfg := config{
		UpToken:      "up-token",
		YNABBudgetID: "ynab-budget",
		BatchSize:    1,
		Accounts: []accountMapping{
			{UpAccountID: "up-account", YNABAccountID: "ynab-account"},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing YNAB token error")
	}
}

func TestNormalizeSinceAcceptsDateOnly(t *testing.T) {
	got, err := normalizeSince("2026-06-20")
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-06-20T00:00:00Z" {
		t.Fatalf("since = %q", got)
	}
}

func TestNormalizeSinceAcceptsRFC3339(t *testing.T) {
	got, err := normalizeSince("2026-06-20T09:30:00+09:30")
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-06-20T00:00:00Z" {
		t.Fatalf("since = %q", got)
	}
}

func TestNormalizeSinceRejectsInvalidValue(t *testing.T) {
	if _, err := normalizeSince("2026/06/20"); err == nil {
		t.Fatal("expected invalid since error")
	}
}

func TestSinceFilterUsesRollingSinceDays(t *testing.T) {
	cfg := config{SinceDays: 14}
	now := time.Date(2026, 7, 5, 13, 45, 0, 0, time.FixedZone("ACST", 9*60*60+30*60))

	got, err := cfg.SinceFilter(now)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-06-21T04:15:00Z" {
		t.Fatalf("since = %q", got)
	}
}

func TestValidateRejectsSinceAndSinceDaysTogether(t *testing.T) {
	cfg := validConfig()
	cfg.Since = "2026-06-20"
	cfg.SinceDays = 14

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected since conflict error")
	}
}

func TestValidateRejectsNegativeSinceDays(t *testing.T) {
	cfg := validConfig()
	cfg.SinceDays = -1

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected since days error")
	}
}

func TestValidateAcceptsMultipleAccountMappings(t *testing.T) {
	cfg := config{
		UpToken:      "up-token",
		YNABToken:    "ynab-token",
		YNABBudgetID: "ynab-budget",
		BatchSize:    10,
		Accounts: []accountMapping{
			{Name: "Spending", UpAccountID: "up-spending", YNABAccountID: "ynab-spending"},
			{Name: "Saver", UpAccountID: "up-saver", YNABAccountID: "ynab-saver"},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAccountMappingsFallsBackToSingleAccountFlags(t *testing.T) {
	cfg := config{
		UpAccountID:   "up-account",
		YNABAccountID: "ynab-account",
	}

	accounts := cfg.accountMappings()
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account mapping, got %d", len(accounts))
	}
	if accounts[0].UpAccountID != "up-account" || accounts[0].YNABAccountID != "ynab-account" {
		t.Fatalf("unexpected account mapping: %#v", accounts[0])
	}
}

func TestLoadConfigFileParsesYAMLAccountMappings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `up_token: up-token
ynab_token: ynab-token
ynab_budget_id: ynab-budget
since_days: 14
include_pending: true
batch_size: 50
accounts:
  - name: Spending
    up_account_id: up-spending
    ynab_account_id: ynab-spending
  - name: Saver
    up_account_id: up-saver
    ynab_account_id: ynab-saver
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpToken != "up-token" || cfg.YNABBudgetID != "ynab-budget" || cfg.SinceDays != 14 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if len(cfg.Accounts) != 2 {
		t.Fatalf("expected 2 account mappings, got %d", len(cfg.Accounts))
	}
	if cfg.Accounts[1].Name != "Saver" || cfg.Accounts[1].YNABAccountID != "ynab-saver" {
		t.Fatalf("unexpected second account: %#v", cfg.Accounts[1])
	}
}

func TestLoadConfigFileRejectsJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ynab_budget_id":"ynab-budget"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfigFile(path)
	if err == nil {
		t.Fatal("expected JSON config error")
	}
	if !strings.Contains(err.Error(), "convert it to YAML") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestYNABCreateTransactionsUsesTransferPayeeID(t *testing.T) {
	var createPayload struct {
		Transactions []struct {
			AccountID         string  `json:"account_id"`
			PayeeID           *string `json:"payee_id"`
			PayeeName         *string `json:"payee_name"`
			CategoryID        *string `json:"category_id"`
			TransferAccountID *string `json:"transfer_account_id"`
		} `json:"transactions"`
	}

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/budgets/budget/payees":
			return jsonResponse(`{
				"data": {
					"payees": [
						{
							"id": "transfer-payee",
							"name": "Transfer : Saver",
							"deleted": false,
							"transfer_account_id": "ynab-saver"
						}
					]
				}
			}`), nil
		case "/budgets/budget/transactions":
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				return nil, err
			}
			return jsonResponse(`{
				"data": {
					"transaction_ids": ["new-transaction"],
					"duplicate_import_ids": []
				}
			}`), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
		}
	})

	transferAccountID := "ynab-saver"
	importID := "UP:example"
	client := newYNABTransactionClient("token", false)
	client.baseURL = "https://ynab.test"
	client.httpClient = &http.Client{Transport: transport}

	_, err := client.CreateTransactions(contextWithTestDeadline(t), "budget", []ynabPayloadTransaction{
		{
			AccountID:         "ynab-spending",
			Amount:            -100000,
			Cleared:           ynabtx.ClearingStatusCleared,
			Approved:          false,
			ImportID:          &importID,
			TransferAccountID: &transferAccountID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(createPayload.Transactions) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(createPayload.Transactions))
	}

	tx := createPayload.Transactions[0]
	if tx.PayeeID == nil || *tx.PayeeID != "transfer-payee" {
		t.Fatalf("payee ID = %#v", tx.PayeeID)
	}
	if tx.PayeeName != nil {
		t.Fatalf("expected no payee name, got %#v", *tx.PayeeName)
	}
	if tx.CategoryID != nil {
		t.Fatalf("expected no category, got %#v", *tx.CategoryID)
	}
	if tx.TransferAccountID != nil {
		t.Fatalf("transfer_account_id should not be posted, got %#v", *tx.TransferAccountID)
	}
}

func validConfig() config {
	return config{
		UpToken:      "up-token",
		YNABToken:    "ynab-token",
		YNABBudgetID: "ynab-budget",
		BatchSize:    1,
		Accounts: []accountMapping{
			{UpAccountID: "up-account", YNABAccountID: "ynab-account"},
		},
	}
}

func setTransferAccount(tx *upTransaction, accountID string) {
	tx.Relationships.TransferAccount.Data = &struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}{
		Type: "accounts",
		ID:   accountID,
	}
}

func contextWithTestDeadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

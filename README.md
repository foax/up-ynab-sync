# up-ynab-sync

Sync transactions from an Up Banking account into a YNAB account.

The app uses:

- `github.com/brunomvsouza/ynab.go` for YNAB transaction and date types
- `github.com/jaydenthomson-mantel/up` for Up account access

The current `up` module release has an incomplete transaction model, so transaction reads are handled by a small local Up API reader with matching auth and pagination semantics. The current `ynab.go` create payload does not expose `transfer_account_id`, so transaction creation uses a narrow local YNAB API client with the same payload shape plus transfer support.

## Usage

For multiple Up accounts, create a config file:

```json
{
  "up_token": "up:yeah:...",
  "ynab_token": "...",
  "ynab_budget_id": "...",
  "since": "2026-01-01",
  "accounts": [
    {
      "name": "Spending",
      "up_account_id": "...",
      "ynab_account_id": "..."
    },
    {
      "name": "Saver",
      "up_account_id": "...",
      "ynab_account_id": "..."
    }
  ]
}
```

Then run:

```sh
go run . -config config.json -dry-run
```

Tokens can be omitted from the file and supplied with `UP_TOKEN` and `YNAB_TOKEN`.

For a single account, you can still use environment variables:

```sh
export UP_TOKEN="up:yeah:..."
export YNAB_TOKEN="..."
export UP_ACCOUNT_ID="..."
export YNAB_BUDGET_ID="..."
export YNAB_ACCOUNT_ID="..."

go run . -since 2026-01-01
```

Useful flags:

- `-config PATH` reads a JSON config file with one or more account mappings.
- `-dry-run` prints the transactions that would be sent to YNAB.
- `-since YYYY-MM-DD` limits Up transactions by creation date. RFC3339 timestamps like `2026-01-01T09:30:00+10:30` are also accepted.
- `-include-pending` includes Up transactions that have not settled yet.
- `-batch-size N` controls YNAB batch creation size.
- `-debug` prints YNAB create request summaries and raw YNAB responses.

Every YNAB transaction gets a deterministic `import_id` derived from the Up transaction ID, so reruns should be idempotent.

## Transfers

If an Up transaction is a transfer to another Up account listed in `accounts`, the app looks up YNAB's transfer payee for the destination account and sends that `payee_id` instead of a payee name. YNAB then creates a native linked transfer that does not need a category.

When both sides of the Up transfer are in your config, only the outgoing side is sent to YNAB. The matching incoming transaction is skipped because YNAB creates that side automatically.

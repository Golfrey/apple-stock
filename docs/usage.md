# Go CLI, TUI and Slack alerts

Apple Stock is a standalone Go monitor. It uses Apple's current US storefront endpoints with an anonymous cookie jar. It needs no browser or Apple account.

Default selection: all four **iPhone 18 Pro Max 256GB** colors, at **Fifth Avenue, Grand Central, Upper West Side, Upper East Side, SoHo and West 14th Street**. ZIP: `10001`.

## Run

Requires Go 1.24+ to build; the resulting executable is standalone.

```sh
go build -o bin/apple-stock ./cmd/apple-stock
./bin/apple-stock init
./bin/apple-stock catalog
./bin/apple-stock tui
```

`init` refuses to overwrite an existing configuration. On macOS the default data directory is `~/Library/Application Support/apple-stock`. Use a global `--dir PATH` before the subcommand for a different directory.

The TUI has four sections:

| Key | Action |
| --- | --- |
| `1`–`4`, Tab | Overview, Products, Stores, Settings |
| Arrow keys | Move / scroll |
| Space | Select or deselect a product or store |
| `/` in Products | Filter by model, storage, color or part number |
| `r` in Products | Load models from the configured Apple product page |
| `a` in Products / Stores | Add an exact part number or store ID and display name |
| Enter in Settings | Edit ZIP, product page or webhook; toggle pickup policy or schedule; send a test |
| `s` | Save changes atomically |
| `c` | Check now and send any new availability alerts using saved settings |
| `q` | Quit; the background schedule continues |

Settings → Product page can point to another US Apple iPhone shopping page. Refresh Products to load its configurations; existing selections are retained. Entering a ZIP automatically loads nearby Apple Stores and opens Stores for review. Matching stores retain their selections; new stores start deselected, so changing a Manhattan ZIP does not automatically subscribe you to Brooklyn or New Jersey stores. Select at least one store, then press `s` to save the ZIP and stores together. Failed or empty lookups keep the previous ZIP and store list unchanged. Nearby results can cross city or borough boundaries, so review the list if you only want Manhattan.

Press `r` in Stores to refresh the current ZIP while retaining selections for matching stores; newly discovered stores start deselected. Stores appear even when the selected iPhone is out of stock. You can also add stores as `R095 | Fifth Avenue`; Apple retail pages expose the exact `storeNumber`.

## Slack

Create a Slack incoming webhook for your desired channel. Enter its URL in **Settings → Slack webhook**, press Enter, then `s`. The field is masked. The configuration and state files are written with mode `0600`; the webhook is never placed in cron, command-line arguments or log messages.

Use **Settings → Send a Slack test message**, or:

```sh
./bin/apple-stock test-notify
```

The program requires Slack's HTTP 200 / `ok` acknowledgement. Until a webhook is configured, it still monitors and displays results but cannot send alerts. Available options stay pending for the next check after configuration.

Alerts include the exact model, storage, color, store, pickup quote and pickup date. The default policy allows **any available pickup date**, including tomorrow; switch to **Today only** in Settings if desired.

Alerts are sent on the first available observation, after an unavailable → available transition, or when the offered pickup date changes. Unchanged availability does not send a message every minute. Observed unavailability resets that store/product's alert history. Failed requests preserve the previous result as stale instead of marking the product unavailable. Failed Slack delivery remains pending for retry. Delivery is at-least-once: a crash or network timeout after Slack accepts a message but before local acknowledgement can produce a duplicate.

## Every-minute background job

The `schedule` command and TUI use **launchd on macOS** and **cron on Linux**. The macOS LaunchAgent uses `StartInterval=60` and also runs once when loaded. The explicit `cron` commands remain available.

On macOS, install the executable outside Documents so the scheduler does not need access to that protected folder:

```sh
install -d -m 700 "$HOME/Library/Application Support/apple-stock"
install -m 755 bin/apple-stock "$HOME/Library/Application Support/apple-stock/apple-stock"
"$HOME/Library/Application Support/apple-stock/apple-stock" schedule install
```

You can continue to open the TUI using `./bin/apple-stock tui`; it uses the same default config. Scheduling prefers the installed binary. The macOS job is `~/Library/LaunchAgents/local.apple-stock.monitor.plist`, automatically loaded on login. No root service is installed.

```sh
./bin/apple-stock schedule status
./bin/apple-stock schedule remove
./bin/apple-stock status
./bin/apple-stock check            # fetches without sending or acknowledging alerts
./bin/apple-stock check --notify   # fetches and sends pending alerts
```

The schedule checks once per minute **while the Mac is awake and you are logged in**. A file lock prevents overlapping runs. Each run has a 45-second deadline; Apple requests are paced at one per second. Errors trigger exponential backoff (1–16 minutes) and respect longer `Retry-After` values. Consecutive backoff intervals intentionally skip minute ticks. Slack failures do not erase successful observations.

For explicit cron scheduling, use `cron install`, `cron status` and `cron remove`. Existing unrelated cron entries are preserved, a backup is saved as `crontab.previous`, and the installed crontab is read back for verification. Stop the launchd schedule before installing cron, or remove cron before installing launchd; the CLI prevents running both backends together. A stalled cron installation times out instead of leaving the TUI waiting indefinitely.

`state.json` contains the last attempt, last successful complete check, per-store timestamps, last notification, any error and the next allowed attempt. `cron.log` captures background-job errors for either scheduler; routine successful checks are quiet.

For uninterrupted monitoring while your Mac sleeps, run the same Go program and cron job on an always-on Linux machine. The file locking and cron integration support macOS/Linux.

## HTTP flow

1. `GET /shop/address/location/update?postalCode=10001`; verify the echoed ZIP and retain cookies in memory.
2. For each selected part: `GET /shop/sba/pickup-detail?product=PART&stores.0=R095&stores.1=R415…` using the same cookie jar.
3. Require a result for every requested store and recognize `pickupSearchQuote` plus `pickupEncodedUpperDateString`.

Since the selected stores are known, scheduled checks do not need the broader `/shop/sba/availability-message` discovery call. Four products require five requests per run (one location update and four store-detail calls). Cookies are fresh each run and never copied from your browser.

Apple's old `/shop/fulfillment-messages` endpoint returned HTTP 541 during investigation. These storefront endpoints are not a documented public API, so schema errors are surfaced rather than treated as stock changes. Availability is a pickup offer, not a physical unit count or reservation.

## Tests

```sh
go test -race ./...
go vet ./...
```

Tests use local HTTP servers, never Apple's live service or Slack. They cover cookie/location handling, explicit store selection, malformed responses, restock transitions, deduplication, failed notification retry, rate-limit backoff, overlapping-run prevention, safe cron editing, catalogue parsing and secret handling.

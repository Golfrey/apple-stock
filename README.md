# Apple Stock

A small Go CLI and terminal UI for checking iPhone pickup availability at selected US Apple Stores and sending Slack alerts when availability changes.

- Select iPhone models, storage sizes, colors and stores in the TUI.
- Check every minute with launchd on macOS or cron on Linux.
- Receive Slack alerts when a model becomes available or goes out of stock at a store, without repeats for unchanged stock.
- Use direct HTTP with an anonymous session; no browser or Apple account needed.
- Keep credentials in a private local config file, outside the repository.

## Quick start

Requires Go 1.24+ to build. Supports macOS and Linux.

```sh
go build -o bin/apple-stock ./cmd/apple-stock
./bin/apple-stock init
./bin/apple-stock catalog
./bin/apple-stock tui
```

The initial selection is all four **256GB iPhone 18 Pro Max** colors at six **Manhattan** Apple Stores, using ZIP `10001`. Change these in the TUI. Refresh the catalogue from a US Apple iPhone shopping page to load other configurations.

Changing the ZIP in Settings loads a new nearby-store list. Review and toggle stores, then press `s` to save both the ZIP and selections. Press `r` in Stores to refresh the current area without reselecting stores you previously disabled.

In the TUI, use **Tab** to switch sections, **Space** to toggle products/stores, and **s** to save. Under **Settings**, enter your Slack incoming webhook, save, and send a test message. Webhooks are masked in the UI and stored in a file with mode `0600`.

## Schedule checks

On macOS, install the binary outside protected folders such as Documents:

```sh
install -d -m 700 "$HOME/Library/Application Support/apple-stock"
install -m 755 bin/apple-stock "$HOME/Library/Application Support/apple-stock/apple-stock"
"$HOME/Library/Application Support/apple-stock/apple-stock" schedule install
```

On Linux, keep the executable in a permanent location and run `./bin/apple-stock schedule install`.

The TUI's Settings section can also enable or disable the schedule. The Mac must be awake and logged in for checks to run. Use an always-on machine for continuous monitoring.

```sh
./bin/apple-stock status
./bin/apple-stock check             # fetch without sending alerts
./bin/apple-stock check --notify    # fetch and send pending alerts
./bin/apple-stock schedule status
./bin/apple-stock schedule remove
```

[Full configuration and usage guide](docs/usage.md)

## How it works

Each check establishes a ZIP-specific cookie session using Apple's `/shop/address/location/update` endpoint, then queries `/shop/sba/pickup-detail` for each selected product and explicit store IDs. This avoids confusing nearby stores in other boroughs with the stores you selected.

Alerts track each product/store pair. After an availability alert, pickup date or quote changes do not trigger another. The monitor alerts when that pair goes out of stock and again when it restocks; items initially observed as unavailable stay quiet. Failed API requests remain unknown instead of becoming “out of stock.” Failed Slack deliveries are retried against fresh results; a restock supersedes an undelivered out-of-stock alert. Requests are paced, overlapping checks are locked out, and errors trigger backoff.

Apple's storefront endpoints are not a documented public API and may change. Results describe pickup offers, not unit counts or reservations. The default policy alerts for any available pickup date; choose **Today only** to restrict it.

## Development

```sh
go test -race ./...
go vet ./...
```

Tests use local HTTP servers and do not send requests to Apple or Slack. GitHub Actions checks macOS and Linux.

## Attribution

Inspired by [MoshiCoCo/Apple-Monitor](https://github.com/MoshiCoCo/Apple-Monitor), whose Java implementation provided the starting point for investigating Apple's inventory endpoints. This repository contains the standalone Go implementation. The original MIT notice is retained in [LICENSE](LICENSE).

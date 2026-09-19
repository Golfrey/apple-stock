package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"apple-stock/internal/stock"
	"apple-stock/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "apple-stock:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("apple-stock", flag.ContinueOnError)
	dir := flags.String("dir", stock.DefaultDir(), "configuration and state directory")
	flags.Usage = func() {
		fmt.Println("Usage: apple-stock [--dir PATH] <init|tui|check|catalog|status|schedule|cron|test-notify>\n\n  init          Create Manhattan + 256GB Pro Max defaults\n  tui           Configure products, stores, Slack and schedule\n  check         One check; --notify sends new availability alerts\n  catalog       Refresh products from the configured Apple page\n  status        Show latest results and monitor health\n  schedule      install | remove | status | print (launchd on macOS, cron on Linux)\n  cron          Explicit crontab: install | remove | status | print\n  test-notify   Send a test to the configured Slack webhook\n\nDefault directory: " + stock.DefaultDir())
	}
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args := flags.Args()
	if len(args) == 0 {
		flags.Usage()
		return nil
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	*dir = abs
	switch args[0] {
	case "init":
		if _, err := os.Stat(filepath.Join(*dir, "config.json")); err == nil {
			return errors.New("config already exists; use tui to edit it")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := stock.SaveConfig(*dir, stock.DefaultConfig()); err != nil {
			return err
		}
		fmt.Println("Created", filepath.Join(*dir, "config.json"))
		fmt.Println("Default: all four 256GB Pro Max colors, six Manhattan stores, any available pickup date.\nConfigure the Slack webhook in the TUI before expecting alerts.")
	case "tui":
		return tui.Run(*dir)
	case "catalog":
		c, err := stock.LoadConfig(*dir)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		p, err := stock.NewClient(c.ProductPage).Discover(ctx, c.ProductPage)
		if err != nil {
			return err
		}
		c.Products = stock.MergeProducts(c.Products, p)
		if err := stock.SaveConfig(*dir, c); err != nil {
			return err
		}
		fmt.Printf("Loaded %d products; current selections preserved.\n", len(p))
	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		notify := fs.Bool("notify", false, "send Slack alerts for newly available pickup")
		quiet := fs.Bool("quiet", false, "only log errors (for cron)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		s, err := stock.Run(context.Background(), *dir, *notify)
		if errors.Is(err, stock.ErrBusy) {
			if !*quiet {
				fmt.Println(err)
			}
			return nil
		}
		if !*quiet {
			c, configErr := stock.LoadConfig(*dir)
			if configErr == nil {
				printStatus(s, c)
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", time.Now().Format(time.RFC3339), err)
		}
	case "status":
		c, err := stock.LoadConfig(*dir)
		if err != nil {
			return err
		}
		s, err := stock.LoadState(*dir)
		if err != nil {
			return err
		}
		printStatus(s, c)
		enabled, err := stock.ScheduleEnabled()
		if err != nil {
			fmt.Println("Schedule:", err)
		} else {
			fmt.Printf("Schedule (%s) enabled: %v\n", stock.SchedulerName(), enabled)
		}
	case "schedule":
		if len(args) != 2 {
			return errors.New("usage: schedule install | remove | status | print")
		}
		switch args[1] {
		case "install", "remove":
			enabled := args[1] == "install"
			if err := stock.SetSchedule(*dir, enabled); err != nil {
				return err
			}
			fmt.Printf("%s schedule enabled: %v (every minute while awake)\n", stock.SchedulerName(), enabled)
		case "status":
			enabled, err := stock.ScheduleEnabled()
			if err != nil {
				return err
			}
			fmt.Printf("%s schedule enabled: %v\n", stock.SchedulerName(), enabled)
		case "print":
			text, err := stock.ScheduleText(*dir)
			if err != nil {
				return err
			}
			fmt.Print(text)
		default:
			return errors.New("unknown schedule command")
		}
	case "cron":
		if len(args) != 2 {
			return errors.New("usage: cron install | remove | status | print")
		}
		switch args[1] {
		case "install":
			if _, err := stock.LoadConfig(*dir); err != nil {
				return err
			}
			if err := stock.SetCron(*dir, true); err != nil {
				return err
			}
			fmt.Println("Installed and verified: every minute. Your Mac must be awake.")
		case "remove":
			if err := stock.SetCron(*dir, false); err != nil {
				return err
			}
			fmt.Println("Removed apple-stock job; other cron entries preserved.")
		case "status":
			s, err := stock.ReadCrontab()
			if err != nil {
				return err
			}
			if !strings.Contains(s, "# BEGIN apple-stock managed job") {
				fmt.Println("Not installed")
			} else {
				inside := false
				for _, line := range strings.Split(s, "\n") {
					if line == "# BEGIN apple-stock managed job" {
						inside = true
					}
					if inside {
						fmt.Println(line)
					}
					if line == "# END apple-stock managed job" {
						inside = false
					}
				}
			}
		case "print":
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			text, err := stock.CronText("", exe, *dir, true)
			if err != nil {
				return err
			}
			fmt.Print(text)
		default:
			return errors.New("unknown cron command")
		}
	case "test-notify":
		c, err := stock.LoadConfig(*dir)
		if err != nil {
			return err
		}
		if c.SlackWebhook == "" {
			return errors.New("configure the Slack webhook in TUI Settings first")
		}
		if err := stock.SendSlack(context.Background(), c.SlackWebhook, "Apple Stock test: Slack alerts are connected. The monitor checks your selected iPhones and stores every minute while your Mac is awake."); err != nil {
			return err
		}
		fmt.Println("Slack acknowledged the test message.")
	default:
		return fmt.Errorf("unknown command %q; use --help", args[0])
	}
	return nil
}

func printStatus(s stock.State, c stock.Config) {
	fmt.Printf("ZIP %s | last attempt %s | last complete check %s\n", c.ZIP, formatTime(s.LastAttempt), formatTime(s.LastSuccess))
	for _, r := range stock.SortedResults(s, c) {
		stale := ""
		if r.CheckedAt.Before(s.LastAttempt) {
			stale = " [older result]"
		}
		fmt.Printf("%-40s  %-18s  %s %s%s\n", r.Product, r.Store, r.Quote, r.Date, stale)
	}
	if s.LastError != "" {
		fmt.Println("Last check error:", s.LastError)
	}
	if s.Notice != "" {
		fmt.Println(s.Notice)
	}
	if c.SlackWebhook == "" {
		fmt.Println("Slack: webhook not configured")
	} else {
		fmt.Println("Slack: configured")
	}
}
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format(time.RFC3339)
}

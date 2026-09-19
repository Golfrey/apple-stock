package stock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const cronBegin = "# BEGIN apple-stock managed job"
const cronEnd = "# END apple-stock managed job"

func ReadCrontab() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/crontab", "-l").CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "no crontab for") {
			return "", nil
		}
		if len(out) == 0 {
			return "", fmt.Errorf("read crontab: %w", err)
		}
		return "", fmt.Errorf("read crontab: %s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func CronEnabled() (bool, error) { s, err := ReadCrontab(); return strings.Contains(s, cronBegin), err }

func WithoutJob(current string) (string, error) {
	var lines []string
	inside := false
	for _, line := range strings.Split(strings.TrimSuffix(current, "\n"), "\n") {
		switch line {
		case cronBegin:
			if inside {
				return "", errors.New("malformed managed cron block")
			}
			inside = true
		case cronEnd:
			if !inside {
				return "", errors.New("malformed managed cron block")
			}
			inside = false
		default:
			if !inside {
				lines = append(lines, line)
			}
		}
	}
	if inside {
		return "", errors.New("unterminated managed cron block")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n"), nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func CronText(current, exe, dir string, enabled bool) (string, error) {
	base, err := WithoutJob(current)
	if err != nil {
		return "", err
	}
	if !enabled {
		if base != "" {
			base += "\n"
		}
		return base, nil
	}
	// Cron treats percent signs specially even inside shell quotes.
	if strings.ContainsAny(exe+dir, "\r\n%") {
		return "", errors.New("cron paths cannot contain newlines or percent signs")
	}
	if !filepath.IsAbs(exe) || !filepath.IsAbs(dir) {
		return "", errors.New("cron requires absolute paths")
	}
	line := fmt.Sprintf("* * * * * %s --dir %s check --notify --quiet >> %s 2>&1", shellQuote(exe), shellQuote(dir), shellQuote(filepath.Join(dir, "cron.log")))
	if base != "" {
		base += "\n"
	}
	return base + cronBegin + "\n" + line + "\n" + cronEnd + "\n", nil
}

func SetCron(dir string, enabled bool) error {
	if enabled && runtime.GOOS == "darwin" {
		active, err := ScheduleEnabled()
		if err != nil {
			return err
		}
		if active {
			return errors.New("remove the active launchd schedule before installing cron")
		}
	}
	current, err := ReadCrontab()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	// Prefer the installed binary when the TUI was opened from a development build.
	if installed := filepath.Join(dir, "apple-stock"); enabled {
		if info, statErr := os.Stat(installed); statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			exe = installed
		}
	}
	updated, err := CronText(current, exe, dir, enabled)
	if err != nil {
		return err
	}
	if current == updated {
		return nil
	}
	// Keep a private backup; never replace unrelated jobs without retaining the original.
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "crontab.previous"), []byte(current), 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/crontab", "-")
	cmd.Stdin = strings.NewReader(updated)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return errors.New("cron installation timed out; macOS may be waiting for permission to administer scheduled jobs")
		}
		return fmt.Errorf("install crontab: %s", strings.TrimSpace(string(out)))
	}
	actual, err := ReadCrontab()
	if err != nil {
		return err
	}
	if actual != updated {
		return errors.New("crontab verification failed")
	}
	return nil
}

package stock

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const launchLabel = "local.apple-stock.monitor"

func SchedulerName() string {
	if runtime.GOOS == "darwin" {
		return "launchd"
	}
	return "cron"
}

func installedExecutable(dir string) (string, error) {
	if p := filepath.Join(dir, "apple-stock"); filepath.IsAbs(p) {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return p, nil
		}
	}
	return os.Executable()
}

func launchPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
}
func launchDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }
func launchTarget() string { return launchDomain() + "/" + launchLabel }

func launchctl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput()
	if ctx.Err() != nil {
		return string(out), errors.New("launchctl timed out")
	}
	return string(out), err
}

func ScheduleEnabled() (bool, error) {
	if runtime.GOOS != "darwin" {
		return CronEnabled()
	}
	out, err := launchctl("print", launchTarget())
	if err != nil {
		if strings.Contains(strings.ToLower(out), "could not find service") || strings.Contains(strings.ToLower(out), "could not find specified service") {
			return false, nil
		}
		return false, fmt.Errorf("read launchd schedule: %s (%v)", strings.TrimSpace(out), err)
	}
	return true, nil
}

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func LaunchdPlist(exe, dir string) (string, error) {
	if !filepath.IsAbs(exe) || !filepath.IsAbs(dir) {
		return "", errors.New("schedule requires absolute paths")
	}
	var args strings.Builder
	for _, arg := range []string{exe, "--dir", dir, "check", "--notify", "--quiet"} {
		fmt.Fprintf(&args, "      <string>%s</string>\n", xmlText(arg))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key><string>%s</string>
    <key>ProgramArguments</key>
    <array>
%s    </array>
    <key>StartInterval</key><integer>60</integer>
    <key>RunAtLoad</key><true/>
    <key>ProcessType</key><string>Background</string>
    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>
  </dict>
</plist>
`, launchLabel, args.String(), xmlText(filepath.Join(dir, "cron.log")), xmlText(filepath.Join(dir, "cron.log"))), nil
}

func ScheduleText(dir string) (string, error) {
	exe, err := installedExecutable(dir)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return LaunchdPlist(exe, dir)
	}
	return CronText("", exe, dir, true)
}

func SetSchedule(dir string, enabled bool) error {
	if runtime.GOOS != "darwin" {
		return SetCron(dir, enabled)
	}
	path, err := launchPath()
	if err != nil {
		return err
	}
	loaded, err := ScheduleEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		if loaded {
			if out, err := launchctl("bootout", launchTarget()); err != nil {
				return fmt.Errorf("stop schedule: %s", strings.TrimSpace(out))
			}
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if _, err := LoadConfig(dir); err != nil {
		return err
	}
	// Avoid leaving both scheduling backends active.
	if cron, err := CronEnabled(); err != nil {
		return err
	} else if cron {
		return errors.New("remove the existing apple-stock cron job before installing launchd")
	}
	text, err := ScheduleText(dir)
	if err != nil {
		return err
	}
	if loaded {
		if b, err := os.ReadFile(path); err == nil && string(b) == text {
			return nil
		}
		if out, err := launchctl("bootout", launchTarget()); err != nil {
			return fmt.Errorf("reload schedule: %s", strings.TrimSpace(out))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		return err
	}
	if out, err := launchctl("bootstrap", launchDomain(), path); err != nil {
		return fmt.Errorf("load schedule: %s", strings.TrimSpace(out))
	}
	loaded, err = ScheduleEnabled()
	if err != nil {
		return err
	}
	if !loaded {
		return errors.New("schedule verification failed")
	}
	return nil
}

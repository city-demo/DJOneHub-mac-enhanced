package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

func defaultSMSCachePath() string {
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	}
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "DJOneHub", "sms-cache.json")
}

func smsCacheKey(item receivedSMS) string {
	return item.Sender + "\x00" + item.Content + "\x00" + item.Timestamp.UTC().Format(time.RFC3339Nano)
}

func (a *app) loadSMSCache() error {
	if a.demo || strings.TrimSpace(a.smsCachePath) == "" {
		return nil
	}
	data, err := os.ReadFile(a.smsCachePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read SMS cache: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var cached []receivedSMS
	if err := json.Unmarshal(data, &cached); err != nil {
		return fmt.Errorf("parse SMS cache: %w", err)
	}
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	a.mergeSMSLocked(cached)
	return nil
}

func (a *app) persistSMSCacheLocked() error {
	if a.demo || strings.TrimSpace(a.smsCachePath) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(a.smsCachePath), 0o700); err != nil {
		return fmt.Errorf("create SMS cache directory: %w", err)
	}
	items := a.sms
	if items == nil {
		items = []receivedSMS{}
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("encode SMS cache: %w", err)
	}
	temporary := a.smsCachePath + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write SMS cache: %w", err)
	}
	if err := os.Rename(temporary, a.smsCachePath); err != nil {
		return fmt.Errorf("replace SMS cache: %w", err)
	}
	return nil
}

func (a *app) mergeSMSLocked(messages []receivedSMS) int {
	seen := make(map[string]bool, len(a.sms)+len(messages))
	for _, item := range a.sms {
		seen[smsCacheKey(item)] = true
	}
	newCount := 0
	for _, item := range messages {
		if item.Code == "" {
			item.Code = extractSMSCode(item.Content)
		}
		key := smsCacheKey(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		a.sms = append(a.sms, item)
		newCount++
	}
	sort.SliceStable(a.sms, func(i, j int) bool {
		return a.sms[i].Timestamp.After(a.sms[j].Timestamp)
	})
	if len(a.sms) > 500 {
		a.sms = a.sms[:500]
	}
	return newCount
}

func (a *app) mergeSMSWithPersist(messages []receivedSMS) (newCount int, total int, persistErr error) {
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	newCount = a.mergeSMSLocked(messages)
	total = len(a.sms)
	if newCount > 0 {
		persistErr = a.persistSMSCacheLocked()
	}
	return newCount, total, persistErr
}

func (a *app) mergeSMS(messages []receivedSMS) (newCount int, total int) {
	newCount, total, persistErr := a.mergeSMSWithPersist(messages)
	if persistErr != nil {
		log.Printf("persist SMS cache failed: %v", persistErr)
	}
	return newCount, total
}

func (a *app) clearSMSCache() {
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	a.sms = nil
	if err := a.persistSMSCacheLocked(); err != nil {
		log.Printf("persist cleared SMS cache failed: %v", err)
	}
}

func (a *app) ensureSMSATReady() error {
	if a.demo || a.smsATRunner != nil {
		return nil
	}
	if a.modem != nil {
		if !a.modem.CanExecuteAT() {
			return errors.New("AT serial port is not ready")
		}
		return nil
	}
	if runtime.GOOS != "darwin" {
		if strings.TrimSpace(a.discoveryError) != "" {
			return errors.New(a.discoveryError)
		}
		return errors.New("AT serial port is not ready")
	}
	return a.ensureUSBAT()
}

func (a *app) runSMSATCommand(command string, timeout time.Duration) (string, error) {
	var (
		resp string
		err  error
	)
	if a.smsATRunner != nil {
		resp, err = a.smsATRunner(command, timeout)
	} else {
		resp, err = a.runATCommand(command, timeout)
	}
	if err != nil {
		return "", err
	}
	if atResponseIsError(resp) {
		return "", fmt.Errorf("%s", strings.TrimSpace(resp))
	}
	return resp, nil
}

func (a *app) triggerSMSPoll() {
	if a.smsWakeCh == nil {
		return
	}
	select {
	case a.smsWakeCh <- struct{}{}:
	default:
	}
}

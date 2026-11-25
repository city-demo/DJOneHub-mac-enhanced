package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSMSCachePersistsAcrossAppRestart(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "DJOneHub", "sms-cache.json")
	first := &app{smsCachePath: cachePath}

	ts := time.Date(2026, 11, 25, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	newCount, total := first.mergeSMS([]receivedSMS{{
		Sender:    "10086",
		Content:   "您的验证码是 654321，5分钟内有效。",
		Timestamp: ts,
	}})
	if newCount != 1 || total != 1 {
		t.Fatalf("mergeSMS() = (%d, %d), want (1, 1)", newCount, total)
	}

	// 模拟退出后重新打开软件：新建 app 实例并从同一缓存路径加载。
	restarted := &app{smsCachePath: cachePath}
	if err := restarted.loadSMSCache(); err != nil {
		t.Fatalf("loadSMSCache() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	restarted.listSMS(recorder, httptest.NewRequest(http.MethodGet, "/api/sms", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/sms status = %d, want 200", recorder.Code)
	}
	var got []receivedSMS
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode /api/sms response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("restored SMS count = %d, want 1", len(got))
	}
	if got[0].Sender != "10086" || got[0].Code != "654321" {
		t.Fatalf("restored SMS = %+v, want sender=10086 code=654321", got[0])
	}

	// 同一条短信再次以 UTC 时间合并时不应重复入库。
	dupCount, dupTotal := restarted.mergeSMS([]receivedSMS{{
		Sender:    "10086",
		Content:   "您的验证码是 654321，5分钟内有效。",
		Timestamp: ts.UTC(),
	}})
	if dupCount != 0 || dupTotal != 1 {
		t.Fatalf("duplicate mergeSMS() = (%d, %d), want (0, 1)", dupCount, dupTotal)
	}
}

func TestClearSMSCachePersistsEmptyStateAcrossRestart(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "DJOneHub", "sms-cache.json")
	a := &app{smsCachePath: cachePath}
	a.recordSMS("10010", "hello", time.Now())

	a.clearSMSCache()

	restarted := &app{smsCachePath: cachePath}
	if err := restarted.loadSMSCache(); err != nil {
		t.Fatalf("loadSMSCache() error = %v", err)
	}
	if len(restarted.sms) != 0 {
		t.Fatalf("restored SMS count after clear = %d, want 0", len(restarted.sms))
	}
}

func TestWindowsModemATSMSSyncReadsBothStoresWithoutDeletingSMAndPersists(t *testing.T) {
	const (
		pduSM = "079144872000302320048102020000625061028204401AD9775D0E72D7DBE2B21C949E8360B75A4E7683D16AB71B"
		pduME = "0004038101F100006250724190410A3754747A0E4ABBCD6F793B4C4FBFDDA0F41CE47ED341617B38CD0E8BD96590F92D07E5DF7539283C1EBFEB6E3A889E87971B"
	)
	cachePath := filepath.Join(t.TempDir(), "DJOneHub", "sms-cache.json")
	currentMemory := "SM"
	smMessages := []string{pduSM}
	meMessages := []string{pduME}
	var deletedMemories []string

	// 模拟 modem.ExecuteAT（成功响应剥离尾部 "OK"，无额外行时返回空串）。
	runner := func(command string, _ time.Duration) (string, error) {
		switch command {
		case "AT+CMGF=0":
			return "", nil
		case `AT+CPMS="SM","SM","SM"`:
			currentMemory = "SM"
			n := strconv.Itoa(len(smMessages))
			return "+CPMS: " + n + ",50," + n + ",50," + n + ",50", nil
		case `AT+CPMS="ME","ME","ME"`:
			currentMemory = "ME"
			n := strconv.Itoa(len(meMessages))
			return "+CPMS: " + n + ",50," + n + ",50," + n + ",50", nil
		case "AT+CMGL=4":
			list := smMessages
			if currentMemory == "ME" {
				list = meMessages
			}
			var lines []string
			for i, pdu := range list {
				lines = append(lines, "+CMGL: "+strconv.Itoa(i+1)+",1,,"+strconv.Itoa((len(pdu)/2)-1))
				lines = append(lines, pdu)
			}
			return strings.Join(lines, "\n"), nil
		case "AT+CMGD=1,4":
			deletedMemories = append(deletedMemories, currentMemory)
			if currentMemory == "ME" {
				meMessages = nil
			} else {
				smMessages = nil
			}
			return "", nil
		default:
			return "", nil
		}
	}

	first := &app{
		smsAutoCleanupME: true,
		smsReassembler:   nil,
		smsCachePath:     cachePath,
		smsATRunner:      runner,
	}

	recorder := httptest.NewRecorder()
	first.refreshSMS(recorder, httptest.NewRequest(http.MethodPost, "/api/sms/refresh", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST /api/sms/refresh status = %d, want 202, body=%s", recorder.Code, recorder.Body.String())
	}
	var refreshResp struct {
		Accepted bool `json:"accepted"`
		Count    int  `json:"count"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&refreshResp); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	if !refreshResp.Accepted || refreshResp.Count != 2 {
		t.Fatalf("refresh response = %+v, want accepted=true count=2", refreshResp)
	}

	// 仅允许自动清理 ME，绝不能误删 SM。
	if len(deletedMemories) != 1 || deletedMemories[0] != "ME" {
		t.Fatalf("deletedMemories = %v, want [ME]", deletedMemories)
	}
	if len(smMessages) != 1 {
		t.Fatalf("SM message count = %d, want 1 (must not be auto-deleted)", len(smMessages))
	}
	if len(meMessages) != 0 {
		t.Fatalf("ME message count = %d, want 0 after auto cleanup", len(meMessages))
	}

	// 模拟软件关闭后重新打开，且此时模块与 SIM 卡上已无任何短信。
	smMessages = nil
	meMessages = nil
	restarted := &app{
		smsAutoCleanupME: true,
		smsCachePath:     cachePath,
		smsATRunner:      runner,
	}
	if err := restarted.loadSMSCache(); err != nil {
		t.Fatalf("loadSMSCache() after restart error = %v", err)
	}
	if err := restarted.pollSMSOnce(); err != nil {
		t.Fatalf("pollSMSOnce() after restart error = %v", err)
	}

	listRec := httptest.NewRecorder()
	restarted.listSMS(listRec, httptest.NewRequest(http.MethodGet, "/api/sms", nil))
	var restored []receivedSMS
	if err := json.NewDecoder(listRec.Body).Decode(&restored); err != nil {
		t.Fatalf("decode /api/sms after restart: %v", err)
	}
	if len(restored) != 2 {
		t.Fatalf("restored SMS count after restart = %d, want 2", len(restored))
	}
}

func TestWindowsModemClearModuleSMSClearsBothStoresAndDiskCache(t *testing.T) {
	const pduSM = "079144872000302320048102020000625061028204401AD9775D0E72D7DBE2B21C949E8360B75A4E7683D16AB71B"
	cachePath := filepath.Join(t.TempDir(), "DJOneHub", "sms-cache.json")
	currentMemory := "SM"
	smCount := 1
	meCount := 2

	a := &app{
		smsCachePath: cachePath,
		smsATRunner: func(command string, _ time.Duration) (string, error) {
			switch command {
			case `AT+CPMS="SM","SM","SM"`:
				currentMemory = "SM"
				n := strconv.Itoa(smCount)
				return "+CPMS: " + n + ",50," + n + ",50," + n + ",50", nil
			case `AT+CPMS="ME","ME","ME"`:
				currentMemory = "ME"
				n := strconv.Itoa(meCount)
				return "+CPMS: " + n + ",50," + n + ",50," + n + ",50", nil
			case "AT+CMGD=1,4":
				if currentMemory == "SM" {
					smCount = 0
				} else {
					meCount = 0
				}
				return "", nil
			default:
				return "", nil
			}
		},
	}
	a.recordSMS("10086", "cached message", time.Now())

	recorder := httptest.NewRecorder()
	a.clearModuleSMS(recorder, httptest.NewRequest(http.MethodPost, "/api/sms/clear-module", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /api/sms/clear-module status = %d, want 200, body=%s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		Cleared bool `json:"cleared"`
		Before  int  `json:"before"`
		After   int  `json:"after"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("decode clear response: %v", err)
	}
	if !resp.Cleared || resp.Before != 3 || resp.After != 0 {
		t.Fatalf("clear response = %+v, want cleared=true before=3 after=0", resp)
	}
	_ = pduSM

	restarted := &app{smsCachePath: cachePath}
	if err := restarted.loadSMSCache(); err != nil {
		t.Fatalf("loadSMSCache() error = %v", err)
	}
	if len(restarted.sms) != 0 {
		t.Fatalf("restored SMS count after clear-module = %d, want 0", len(restarted.sms))
	}
}

func TestIncompleteConcatInMEDefersAutoCleanup(t *testing.T) {
	const (
		part1PDU = "004005810180F6000862506102820440120500032A020100480065006C006C006F0020"
		part2PDU = "004005810180F6000862506102820440100500032A02020057006F0072006C0064"
	)
	cachePath := filepath.Join(t.TempDir(), "DJOneHub", "sms-cache.json")
	currentMemory := "SM"
	mePDUs := []string{part1PDU}
	meDeletes := 0

	a := &app{
		smsAutoCleanupME: true,
		smsCachePath:     cachePath,
		smsATRunner: func(command string, _ time.Duration) (string, error) {
			switch command {
			case "AT+CMGF=0":
				return "", nil
			case `AT+CPMS="SM","SM","SM"`:
				currentMemory = "SM"
				return `+CPMS: 0,50,0,50,0,50`, nil
			case `AT+CPMS="ME","ME","ME"`:
				currentMemory = "ME"
				n := strconv.Itoa(len(mePDUs))
				return "+CPMS: " + n + ",50," + n + ",50," + n + ",50", nil
			case "AT+CMGL=4":
				if currentMemory == "SM" {
					return "", nil
				}
				var lines []string
				for i, pdu := range mePDUs {
					lines = append(lines, "+CMGL: "+strconv.Itoa(i+1)+",1,,"+strconv.Itoa((len(pdu)/2)-1))
					lines = append(lines, pdu)
				}
				return strings.Join(lines, "\n"), nil
			case "AT+CMGD=1,4":
				if currentMemory == "ME" {
					meDeletes++
					mePDUs = nil
				}
				return "", nil
			default:
				return "", nil
			}
		},
	}

	// 第 1 次轮询仅有分片 1：尚未拼齐，绝不能清空 ME。
	if err := a.pollSMSOnce(); err != nil {
		t.Fatalf("first pollSMSOnce() error = %v", err)
	}
	if meDeletes != 0 || len(mePDUs) != 1 {
		t.Fatalf("ME was cleared prematurely: meDeletes=%d remaining=%d", meDeletes, len(mePDUs))
	}
	if len(a.sms) != 0 {
		t.Fatalf("incomplete concat produced cached SMS: %+v", a.sms)
	}

	// 第 2 分片到达后：完成重组、落盘并自动清理 ME。
	mePDUs = append(mePDUs, part2PDU)
	if err := a.pollSMSOnce(); err != nil {
		t.Fatalf("second pollSMSOnce() error = %v", err)
	}
	if meDeletes != 1 || len(mePDUs) != 0 {
		t.Fatalf("ME was not cleaned after concat completion: meDeletes=%d remaining=%d", meDeletes, len(mePDUs))
	}
	if len(a.sms) != 1 || a.sms[0].Content != "Hello World" {
		t.Fatalf("reassembled SMS = %+v, want content \"Hello World\"", a.sms)
	}

	restarted := &app{smsCachePath: cachePath}
	if err := restarted.loadSMSCache(); err != nil {
		t.Fatalf("loadSMSCache() error = %v", err)
	}
	if len(restarted.sms) != 1 || restarted.sms[0].Content != "Hello World" {
		t.Fatalf("persisted reassembled SMS = %+v, want \"Hello World\"", restarted.sms)
	}
}

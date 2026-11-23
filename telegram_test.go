package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelegramConfigFromEnvironment(t *testing.T) {
	t.Setenv("VTT_CZ_TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("VTT_CZ_TELEGRAM_CHAT_ID", "123456")
	t.Setenv("VTT_CZ_TELEGRAM_API_BASE_URL", "https://telegram.example/")

	config, err := telegramConfigFromEnvironment()
	if err != nil {
		t.Fatalf("telegramConfigFromEnvironment() error = %v", err)
	}
	if config.Token != "test-token" || config.ChatID != 123456 || config.APIBaseURL != "https://telegram.example" {
		t.Fatalf("telegram config = %#v", config)
	}

	t.Setenv("VTT_CZ_TELEGRAM_CHAT_ID", "group")
	if _, err := telegramConfigFromEnvironment(); err == nil {
		t.Fatal("telegramConfigFromEnvironment() accepted invalid chat ID")
	}
}

func TestWorkRootDirFromEnvironment(t *testing.T) {
	want := filepath.Join(t.TempDir(), "jobs")
	t.Setenv("VTT_CZ_WORK_ROOT", want)
	if got := workRootDir(); got != want {
		t.Fatalf("workRootDir() = %q, want %q", got, want)
	}
}

func TestEnsureTelegramHandoffSendsDocumentOnce(t *testing.T) {
	var requests atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bottest-token/sendDocument" {
			return telegramTestResponse(http.StatusNotFound, "unexpected path"), nil
		}
		requests.Add(1)
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			return telegramTestResponse(http.StatusBadRequest, err.Error()), nil
		}
		if request.FormValue("chat_id") != "123456" || !strings.Contains(request.FormValue("caption"), "20261123-120000") {
			return telegramTestResponse(http.StatusBadRequest, "unexpected form fields"), nil
		}
		file, header, err := request.FormFile("document")
		if err != nil {
			return telegramTestResponse(http.StatusBadRequest, err.Error()), nil
		}
		defer file.Close()
		contents, _ := io.ReadAll(file)
		if header.Filename != "video.en.raw.vtt" || !strings.HasPrefix(string(contents), "WEBVTT") {
			return telegramTestResponse(http.StatusBadRequest, "unexpected document"), nil
		}
		return telegramTestResponse(http.StatusOK, `{"ok":true,"result":{"message_id":777,"chat":{"id":123456}}}`), nil
	})

	workDir := filepath.Join(t.TempDir(), "20261123-120000")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(workDir, "video.en.raw.vtt")
	csPath := filepath.Join(workDir, "video.cs.vtt")
	outputPath := filepath.Join(workDir, "video.cs.mp4")
	if err := os.WriteFile(rawPath, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nHello.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := telegramConfig{Token: "test-token", ChatID: 123456, APIBaseURL: "https://telegram.test"}
	client := newTelegramClient(config)
	client.httpClient.Transport = transport
	for index := 0; index < 2; index++ {
		if err := ensureTelegramHandoff(context.Background(), client, config, workDir, rawPath, csPath, outputPath); err != nil {
			t.Fatalf("ensureTelegramHandoff() error = %v", err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("sendDocument requests = %d, want 1", requests.Load())
	}
	handoff, err := loadTelegramHandoff(filepath.Join(workDir, telegramHandoffFileName))
	if err != nil {
		t.Fatal(err)
	}
	if handoff.MessageID != 777 || handoff.State != telegramStateWaiting || handoff.CzechVTT != "video.cs.vtt" {
		t.Fatalf("handoff = %#v", handoff)
	}
}

func TestHandleTelegramUpdateAcceptsValidReply(t *testing.T) {
	validVTT := "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nAhoj.\n"
	transport := telegramFileTestTransport(t, validVTT)

	workRoot := t.TempDir()
	workDir := filepath.Join(workRoot, "20261123-120000")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	handoff := testTelegramHandoff(123456, 777)
	if err := saveTelegramHandoff(filepath.Join(workDir, telegramHandoffFileName), handoff); err != nil {
		t.Fatal(err)
	}

	config := telegramConfig{Token: "test-token", ChatID: 123456, APIBaseURL: "https://telegram.test"}
	client := newTelegramClient(config)
	client.httpClient.Transport = transport
	jobs := make(chan telegramJob, 1)
	update := telegramUpdate{
		UpdateID: 10,
		Message: &telegramMessage{
			MessageID: 888,
			From:      &telegramUser{ID: 123456},
			Chat:      telegramChat{ID: 123456},
			ReplyToMessage: &telegramMessage{
				MessageID: 777,
			},
			Document: &telegramDocument{FileID: "file-1", FileName: "translated.cs.vtt", FileSize: int64(len(validVTT))},
		},
	}
	if err := handleTelegramUpdate(context.Background(), client, config, workRoot, update, jobs); err != nil {
		t.Fatalf("handleTelegramUpdate() error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(workDir, "video.cs.vtt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != validVTT {
		t.Fatalf("saved VTT = %q", contents)
	}
	updated, err := loadTelegramHandoff(filepath.Join(workDir, telegramHandoffFileName))
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != telegramStateQueued {
		t.Fatalf("handoff state = %q, want %q", updated.State, telegramStateQueued)
	}
	select {
	case job := <-jobs:
		if job.workDir != workDir {
			t.Fatalf("queued workDir = %q, want %q", job.workDir, workDir)
		}
	case <-time.After(time.Second):
		t.Fatal("Telegram job was not queued")
	}
}

func TestHandleTelegramUpdateRejectsUnauthorizedSender(t *testing.T) {
	var requests atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		return telegramTestResponse(http.StatusInternalServerError, "unexpected request"), nil
	})

	config := telegramConfig{Token: "test-token", ChatID: 123456, APIBaseURL: "https://telegram.test"}
	client := newTelegramClient(config)
	client.httpClient.Transport = transport
	jobs := make(chan telegramJob, 1)
	update := telegramUpdate{Message: &telegramMessage{
		From:     &telegramUser{ID: 999},
		Chat:     telegramChat{ID: 123456},
		Document: &telegramDocument{FileID: "file-1", FileName: "translated.cs.vtt"},
	}}
	if err := handleTelegramUpdate(context.Background(), client, config, t.TempDir(), update, jobs); err != nil {
		t.Fatalf("handleTelegramUpdate() error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("unauthorized update caused %d Telegram request(s)", requests.Load())
	}
	if len(jobs) != 0 {
		t.Fatal("unauthorized update queued a job")
	}
}

func TestFinishTelegramJobMarksExistingOutputDone(t *testing.T) {
	var messages atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bottest-token/sendMessage" {
			return telegramTestResponse(http.StatusNotFound, "unexpected path"), nil
		}
		messages.Add(1)
		return telegramTestResponse(http.StatusOK, `{"ok":true,"result":{"message_id":999,"chat":{"id":123456}}}`), nil
	})

	workDir := t.TempDir()
	for name, contents := range map[string]string{
		"video.mp4":        "video",
		"video.mp3":        "audio",
		"video.en.raw.vtt": "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nHello.\n",
		"video.cs.vtt":     "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nAhoj.\n",
		"video.cs.mp4":     "completed video",
	} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	handoff := testTelegramHandoff(123456, 777)
	handoff.State = telegramStateQueued
	if err := saveTelegramHandoff(filepath.Join(workDir, telegramHandoffFileName), handoff); err != nil {
		t.Fatal(err)
	}

	config := telegramConfig{Token: "test-token", ChatID: 123456, APIBaseURL: "https://telegram.test"}
	client := newTelegramClient(config)
	client.httpClient.Transport = transport
	finishTelegramJob(context.Background(), client, config, workDir)

	updated, err := loadTelegramHandoff(filepath.Join(workDir, telegramHandoffFileName))
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != telegramStateDone || updated.LastError != "" {
		t.Fatalf("handoff after finish = %#v", updated)
	}
	if messages.Load() != 1 {
		t.Fatalf("completion messages = %d, want 1", messages.Load())
	}
}

func telegramFileTestTransport(t *testing.T, contents string) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/bottest-token/getFile":
			if err := request.ParseForm(); err != nil || request.FormValue("file_id") != "file-1" {
				return telegramTestResponse(http.StatusBadRequest, "unexpected file_id"), nil
			}
			return telegramTestResponse(http.StatusOK, `{"ok":true,"result":{"file_path":"documents/result.vtt"}}`), nil
		case "/file/bottest-token/documents/result.vtt":
			response := telegramTestResponse(http.StatusOK, contents)
			response.Header.Set("Content-Type", "text/vtt")
			return response, nil
		case "/bottest-token/sendMessage":
			return telegramTestResponse(http.StatusOK, `{"ok":true,"result":{"message_id":999,"chat":{"id":123456}}}`), nil
		default:
			return telegramTestResponse(http.StatusNotFound, "unexpected path "+request.URL.Path), nil
		}
	})
}

func testTelegramHandoff(chatID, messageID int64) telegramHandoff {
	now := time.Now().UTC().Format(time.RFC3339)
	return telegramHandoff{
		Version:     1,
		JobID:       "20261123-120000",
		ChatID:      chatID,
		MessageID:   messageID,
		RawVTT:      "video.en.raw.vtt",
		CzechVTT:    "video.cs.vtt",
		OutputVideo: "video.cs.mp4",
		State:       telegramStateWaiting,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestTelegramTokenIsRedactedFromTransportErrors(t *testing.T) {
	config := telegramConfig{Token: "secret-token", ChatID: 123456, APIBaseURL: "https://telegram.test"}
	client := newTelegramClient(config)
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("request to %s failed", request.URL.String())
	})
	_, err := client.sendMessage(context.Background(), config.ChatID, "test")
	if err == nil {
		t.Fatal("sendMessage() error = nil")
	}
	if strings.Contains(err.Error(), config.Token) {
		t.Fatalf("transport error leaked Telegram token: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func telegramTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     strconv.Itoa(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestSaveAndLoadTelegramOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offset")
	if err := saveTelegramOffset(path, 42); err != nil {
		t.Fatal(err)
	}
	offset, err := loadTelegramOffset(path)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 42 {
		t.Fatalf("offset = %d, want 42", offset)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(contents)) != strconv.FormatInt(offset, 10) {
		t.Fatalf("offset file = %q", contents)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	telegramHandoffFileName = ".telegram-handoff.json"
	telegramOffsetFileName  = ".telegram-update-offset"
	telegramMaxVTTBytes     = 5 * 1024 * 1024

	telegramStateWaiting   = "waiting_for_translation"
	telegramStateQueued    = "queued_for_rendering"
	telegramStateRendering = "rendering"
	telegramStateDone      = "done"
	telegramStateFailed    = "failed"
)

type telegramConfig struct {
	Token      string
	ChatID     int64
	APIBaseURL string
}

type telegramClient struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

type telegramHandoff struct {
	Version     int    `json:"version"`
	JobID       string `json:"job_id"`
	ChatID      int64  `json:"chat_id"`
	MessageID   int64  `json:"message_id"`
	RawVTT      string `json:"raw_vtt"`
	CzechVTT    string `json:"czech_vtt"`
	OutputVideo string `json:"output_video"`
	State       string `json:"state"`
	LastError   string `json:"last_error,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message,omitempty"`
}

type telegramMessage struct {
	MessageID      int64             `json:"message_id"`
	From           *telegramUser     `json:"from,omitempty"`
	Chat           telegramChat      `json:"chat"`
	ReplyToMessage *telegramMessage  `json:"reply_to_message,omitempty"`
	Document       *telegramDocument `json:"document,omitempty"`
}

type telegramUser struct {
	ID int64 `json:"id"`
}

type telegramChat struct {
	ID int64 `json:"id"`
}

type telegramDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size,omitempty"`
}

type telegramFile struct {
	FilePath string `json:"file_path"`
}

type telegramJob struct {
	workDir string
}

func telegramConfigFromEnvironment() (telegramConfig, error) {
	token := strings.TrimSpace(os.Getenv("VTT_CZ_TELEGRAM_BOT_TOKEN"))
	if token == "" {
		return telegramConfig{}, fmt.Errorf("chybi VTT_CZ_TELEGRAM_BOT_TOKEN")
	}
	for _, character := range token {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == ':' || character == '_' || character == '-') {
			return telegramConfig{}, fmt.Errorf("VTT_CZ_TELEGRAM_BOT_TOKEN ma neplatny format")
		}
	}
	chatIDText := strings.TrimSpace(os.Getenv("VTT_CZ_TELEGRAM_CHAT_ID"))
	if chatIDText == "" {
		return telegramConfig{}, fmt.Errorf("chybi VTT_CZ_TELEGRAM_CHAT_ID")
	}
	chatID, err := strconv.ParseInt(chatIDText, 10, 64)
	if err != nil || chatID <= 0 {
		return telegramConfig{}, fmt.Errorf("VTT_CZ_TELEGRAM_CHAT_ID musi byt kladne cislo soukromeho chatu")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("VTT_CZ_TELEGRAM_API_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return telegramConfig{}, fmt.Errorf("VTT_CZ_TELEGRAM_API_BASE_URL neni platna HTTP(S) adresa")
	}

	return telegramConfig{Token: token, ChatID: chatID, APIBaseURL: baseURL}, nil
}

func newTelegramClient(config telegramConfig) *telegramClient {
	return &telegramClient{
		token:   config.Token,
		baseURL: strings.TrimRight(config.APIBaseURL, "/"),
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func ensureTelegramHandoffFromEnvironment(ctx context.Context, workDir, rawVTTPath, csVTTPath, outputVideoPath string) error {
	config, err := telegramConfigFromEnvironment()
	if err != nil {
		return err
	}
	return ensureTelegramHandoff(ctx, newTelegramClient(config), config, workDir, rawVTTPath, csVTTPath, outputVideoPath)
}

func ensureTelegramHandoff(ctx context.Context, client *telegramClient, config telegramConfig, workDir, rawVTTPath, csVTTPath, outputVideoPath string) error {
	handoffPath := filepath.Join(workDir, telegramHandoffFileName)
	if existing, err := loadTelegramHandoff(handoffPath); err == nil {
		if existing.ChatID != config.ChatID {
			return fmt.Errorf("uloha uz byla odeslana do jineho Telegram chatu")
		}
		if existing.MessageID > 0 {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	jobID := filepath.Base(filepath.Clean(workDir))
	caption := fmt.Sprintf(
		"VTT-CZ uloha %s\n\nAnglicke titulky jsou pripravene. Nahraj prilozene .en.raw.vtt do ChatGPT Work a finalni .cs.vtt posli jako odpoved na tuto zpravu.",
		jobID,
	)
	message, err := client.sendDocument(ctx, config.ChatID, rawVTTPath, caption)
	if err != nil {
		return err
	}
	if message.MessageID <= 0 {
		return fmt.Errorf("Telegram nevratil message_id odeslaneho dokumentu")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	handoff := telegramHandoff{
		Version:     1,
		JobID:       jobID,
		ChatID:      config.ChatID,
		MessageID:   message.MessageID,
		RawVTT:      filepath.Base(rawVTTPath),
		CzechVTT:    filepath.Base(csVTTPath),
		OutputVideo: filepath.Base(outputVideoPath),
		State:       telegramStateWaiting,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	return saveTelegramHandoff(handoffPath, handoff)
}

func runTelegramListenerFromEnvironment(ctx context.Context, workRoot string) error {
	config, err := telegramConfigFromEnvironment()
	if err != nil {
		return err
	}
	return runTelegramListener(ctx, newTelegramClient(config), config, workRoot)
}

func runTelegramListener(ctx context.Context, client *telegramClient, config telegramConfig, workRoot string) error {
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		return fmt.Errorf("vytvoreni korenove slozky uloh: %w", err)
	}

	jobs := make(chan telegramJob, 32)
	go telegramJobWorker(ctx, client, config, jobs)
	if err := enqueuePendingTelegramJobs(workRoot, jobs); err != nil {
		return err
	}

	offsetPath := filepath.Join(workRoot, telegramOffsetFileName)
	offset, err := loadTelegramOffset(offsetPath)
	if err != nil {
		return err
	}
	fmt.Println("Telegram listener bezi pro soukromy chat", config.ChatID)
	fmt.Println("Pracovni slozka:", workRoot)

	for {
		updates, err := client.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintln(os.Stderr, "Varovani: Telegram getUpdates selhalo:", err)
			if !waitForContext(ctx, 5*time.Second) {
				return nil
			}
			continue
		}
		for _, update := range updates {
			if update.UpdateID < offset {
				continue
			}
			if err := handleTelegramUpdate(ctx, client, config, workRoot, update, jobs); err != nil {
				fmt.Fprintln(os.Stderr, "Varovani: zpracovani Telegram zpravy selhalo:", err)
				if !waitForContext(ctx, 5*time.Second) {
					return nil
				}
				break
			}
			offset = update.UpdateID + 1
			if err := saveTelegramOffset(offsetPath, offset); err != nil {
				return err
			}
		}
	}
}

func handleTelegramUpdate(ctx context.Context, client *telegramClient, config telegramConfig, workRoot string, update telegramUpdate, jobs chan<- telegramJob) error {
	message := update.Message
	if message == nil || message.Chat.ID != config.ChatID || message.From == nil || message.From.ID != config.ChatID {
		return nil
	}
	if message.Document == nil {
		return nil
	}
	if message.ReplyToMessage == nil {
		_, err := client.sendMessage(ctx, config.ChatID, "VTT soubor posli jako odpoved na zpravu s anglickymi titulky, aby bylo mozne urcit ulohu.")
		return err
	}
	if !strings.HasSuffix(strings.ToLower(message.Document.FileName), ".vtt") {
		_, err := client.sendMessage(ctx, config.ChatID, "Priloha musi byt WebVTT soubor s koncovkou .vtt.")
		return err
	}
	if message.Document.FileSize > telegramMaxVTTBytes {
		_, err := client.sendMessage(ctx, config.ChatID, "VTT soubor je neocekavane velky; limit vtt-cz je 5 MB.")
		return err
	}

	workDir, handoff, err := findTelegramHandoffByMessageID(workRoot, config.ChatID, message.ReplyToMessage.MessageID)
	if err != nil {
		_, sendErr := client.sendMessage(ctx, config.ChatID, "K teto zprave nebyla nalezena cekajici vtt-cz uloha.")
		if sendErr != nil {
			return fmt.Errorf("%v; navic selhalo odeslani odpovedi: %w", err, sendErr)
		}
		return nil
	}
	if handoff.State != telegramStateWaiting {
		_, err := client.sendMessage(ctx, config.ChatID, fmt.Sprintf("Uloha %s uz neni ve stavu cekani (stav: %s).", handoff.JobID, handoff.State))
		return err
	}

	temporary, err := os.CreateTemp(workDir, ".telegram-czech-*.vtt")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	defer os.Remove(temporaryPath)
	if err := client.downloadDocument(ctx, message.Document.FileID, temporaryPath, telegramMaxVTTBytes); err != nil {
		return err
	}
	if err := validateVTTFile(temporaryPath); err != nil {
		_, sendErr := client.sendMessage(ctx, config.ChatID, "Vraceny VTT neni platny: "+err.Error())
		if sendErr != nil {
			return fmt.Errorf("neplatny VTT: %v; odeslani odpovedi: %w", err, sendErr)
		}
		return nil
	}

	csVTTPath, err := safeTelegramJobPath(workDir, handoff.CzechVTT)
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, csVTTPath); err != nil {
		return fmt.Errorf("ulozeni ceskeho VTT: %w", err)
	}
	handoff.State = telegramStateQueued
	handoff.LastError = ""
	handoff.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := saveTelegramHandoff(filepath.Join(workDir, telegramHandoffFileName), handoff); err != nil {
		return err
	}
	jobs <- telegramJob{workDir: workDir}
	_, err = client.sendMessage(ctx, config.ChatID, fmt.Sprintf("Uloha %s: ceske VTT bylo prijato a overeno. Spoustim vypaleni titulku.", handoff.JobID))
	return err
}

func telegramJobWorker(ctx context.Context, client *telegramClient, config telegramConfig, jobs <-chan telegramJob) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			finishTelegramJob(ctx, client, config, job.workDir)
		}
	}
}

func finishTelegramJob(ctx context.Context, client *telegramClient, config telegramConfig, workDir string) {
	handoffPath := filepath.Join(workDir, telegramHandoffFileName)
	handoff, err := loadTelegramHandoff(handoffPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Telegram uloha nelze nacist:", err)
		return
	}
	handoff.State = telegramStateRendering
	handoff.LastError = ""
	handoff.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := saveTelegramHandoff(handoffPath, handoff); err != nil {
		fmt.Fprintln(os.Stderr, "Telegram uloha nelze oznacit jako rendering:", err)
		return
	}

	videoPath, mp3Path, found, err := findExistingVideoAndMP3(workDir)
	if err == nil && !found {
		err = fmt.Errorf("v pracovni slozce chybi odpovidajici video a MP3")
	}
	if err == nil {
		var rawVTTPath, csVTTPath, outputVideoPath string
		rawVTTPath, err = safeTelegramJobPath(workDir, handoff.RawVTT)
		if err == nil {
			csVTTPath, err = safeTelegramJobPath(workDir, handoff.CzechVTT)
		}
		if err == nil {
			outputVideoPath, err = safeTelegramJobPath(workDir, handoff.OutputVideo)
		}
		if err == nil {
			err = runWorkMode(workDir, videoPath, mp3Path, rawVTTPath, csVTTPath, outputVideoPath, fileExists(filepath.Join(workDir, ".reel-mode")), true)
		}
	}
	if err != nil {
		handoff.State = telegramStateFailed
		handoff.LastError = err.Error()
		handoff.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		_ = saveTelegramHandoff(handoffPath, handoff)
		_, _ = client.sendMessage(ctx, config.ChatID, fmt.Sprintf("Uloha %s selhala pri dokonceni: %v", handoff.JobID, err))
		return
	}

	handoff.State = telegramStateDone
	handoff.LastError = ""
	handoff.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = saveTelegramHandoff(handoffPath, handoff)
	_, _ = client.sendMessage(ctx, config.ChatID, fmt.Sprintf("Uloha %s je hotova. Video s ceskymi titulky je na VPS v %s.", handoff.JobID, filepath.Join(workDir, handoff.OutputVideo)))
}

func enqueuePendingTelegramJobs(workRoot string, jobs chan<- telegramJob) error {
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workDir := filepath.Join(workRoot, entry.Name())
		handoff, err := loadTelegramHandoff(filepath.Join(workDir, telegramHandoffFileName))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if handoff.State != telegramStateQueued && handoff.State != telegramStateRendering {
			continue
		}
		csVTTPath, err := safeTelegramJobPath(workDir, handoff.CzechVTT)
		if err != nil {
			return err
		}
		if fileExists(csVTTPath) {
			jobs <- telegramJob{workDir: workDir}
		}
	}
	return nil
}

func findTelegramHandoffByMessageID(workRoot string, chatID, messageID int64) (string, telegramHandoff, error) {
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		return "", telegramHandoff{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workDir := filepath.Join(workRoot, entry.Name())
		handoff, err := loadTelegramHandoff(filepath.Join(workDir, telegramHandoffFileName))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", telegramHandoff{}, err
		}
		if handoff.ChatID == chatID && handoff.MessageID == messageID {
			return workDir, handoff, nil
		}
	}
	return "", telegramHandoff{}, fmt.Errorf("Telegram handoff pro message_id %d nebyl nalezen", messageID)
}

func safeTelegramJobPath(workDir, name string) (string, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", fmt.Errorf("neplatny nazev souboru v Telegram handoffu %q", name)
	}
	return filepath.Join(workDir, name), nil
}

func loadTelegramHandoff(path string) (telegramHandoff, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return telegramHandoff{}, err
	}
	var handoff telegramHandoff
	if err := json.Unmarshal(contents, &handoff); err != nil {
		return telegramHandoff{}, fmt.Errorf("cteni %s: %w", path, err)
	}
	if handoff.Version != 1 || handoff.JobID == "" || handoff.ChatID <= 0 || handoff.MessageID <= 0 {
		return telegramHandoff{}, fmt.Errorf("%s neobsahuje platny Telegram handoff", path)
	}
	for _, name := range []string{handoff.RawVTT, handoff.CzechVTT, handoff.OutputVideo} {
		if _, err := safeTelegramJobPath(filepath.Dir(path), name); err != nil {
			return telegramHandoff{}, err
		}
	}
	return handoff, nil
}

func saveTelegramHandoff(path string, handoff telegramHandoff) error {
	contents, err := json.MarshalIndent(handoff, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".telegram-handoff-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func loadTelegramOffset(path string) (int64, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	offset, err := strconv.ParseInt(strings.TrimSpace(string(contents)), 10, 64)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("%s neobsahuje platny Telegram offset", path)
	}
	return offset, nil
}

func saveTelegramOffset(path string, offset int64) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".telegram-offset-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := fmt.Fprintf(temporary, "%d\n", offset); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func waitForContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (client *telegramClient) sendDocument(ctx context.Context, chatID int64, path, caption string) (telegramMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return telegramMessage{}, err
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return telegramMessage{}, err
	}
	if err := writer.WriteField("caption", caption); err != nil {
		return telegramMessage{}, err
	}
	part, err := writer.CreateFormFile("document", filepath.Base(path))
	if err != nil {
		return telegramMessage{}, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return telegramMessage{}, err
	}
	if err := writer.Close(); err != nil {
		return telegramMessage{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.methodURL("sendDocument"), &body)
	if err != nil {
		return telegramMessage{}, client.redactedError("vytvoreni Telegram pozadavku", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	var message telegramMessage
	if err := client.do(request, &message); err != nil {
		return telegramMessage{}, err
	}
	return message, nil
}

func (client *telegramClient) sendMessage(ctx context.Context, chatID int64, text string) (telegramMessage, error) {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("text", text)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.methodURL("sendMessage"), strings.NewReader(values.Encode()))
	if err != nil {
		return telegramMessage{}, client.redactedError("vytvoreni Telegram pozadavku", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var message telegramMessage
	if err := client.do(request, &message); err != nil {
		return telegramMessage{}, err
	}
	return message, nil
}

func (client *telegramClient) getUpdates(ctx context.Context, offset int64) ([]telegramUpdate, error) {
	values := url.Values{}
	values.Set("offset", strconv.FormatInt(offset, 10))
	values.Set("timeout", "30")
	values.Set("allowed_updates", `["message"]`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.methodURL("getUpdates"), strings.NewReader(values.Encode()))
	if err != nil {
		return nil, client.redactedError("vytvoreni Telegram pozadavku", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var updates []telegramUpdate
	if err := client.do(request, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (client *telegramClient) downloadDocument(ctx context.Context, fileID, outputPath string, maxBytes int64) error {
	values := url.Values{}
	values.Set("file_id", fileID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.methodURL("getFile"), strings.NewReader(values.Encode()))
	if err != nil {
		return client.redactedError("vytvoreni Telegram pozadavku", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var file telegramFile
	if err := client.do(request, &file); err != nil {
		return err
	}
	if file.FilePath == "" {
		return fmt.Errorf("Telegram nevratil cestu k souboru")
	}

	downloadURL := client.fileURL(file.FilePath)
	downloadRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return client.redactedError("vytvoreni pozadavku pro stazeni Telegram souboru", err)
	}
	response, err := client.httpClient.Do(downloadRequest)
	if err != nil {
		return client.redactedError("stazeni Telegram souboru", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("stazeni Telegram souboru vratilo HTTP %s", response.Status)
	}

	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(response.Body, maxBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxBytes {
		return fmt.Errorf("Telegram VTT prekrocil limit %d bajtu", maxBytes)
	}
	return nil
}

func (client *telegramClient) do(request *http.Request, result any) error {
	response, err := client.httpClient.Do(request)
	if err != nil {
		return client.redactedError("Telegram API pozadavek", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("Telegram API vratilo HTTP %s", response.Status)
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("Telegram API vratilo neplatny JSON: %w", err)
	}
	if !envelope.OK {
		if envelope.Description == "" {
			envelope.Description = "neznamy problem"
		}
		description := strings.ReplaceAll(envelope.Description, client.token, "<redacted>")
		return fmt.Errorf("Telegram API chyba: %s", description)
	}
	if result != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("Telegram API result nelze precist: %w", err)
		}
	}
	return nil
}

func (client *telegramClient) methodURL(method string) string {
	return client.baseURL + "/bot" + client.token + "/" + method
}

func (client *telegramClient) fileURL(filePath string) string {
	parts := strings.Split(strings.TrimLeft(filePath, "/"), "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return client.baseURL + "/file/bot" + client.token + "/" + strings.Join(parts, "/")
}

func (client *telegramClient) redactedError(operation string, err error) error {
	message := strings.ReplaceAll(err.Error(), client.token, "<redacted>")
	return fmt.Errorf("%s: %s", operation, message)
}

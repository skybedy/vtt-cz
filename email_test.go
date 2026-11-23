package main

import (
	"bytes"
	"context"
	"errors"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingEmailSender struct {
	messages []outgoingEmail
	err      error
}

func (sender *recordingEmailSender) Send(_ context.Context, message outgoingEmail) error {
	sender.messages = append(sender.messages, message)
	return sender.err
}

func TestEmailConfigFromEnvironment(t *testing.T) {
	setEmailTestEnvironment(t)
	config, err := emailConfigFromEnvironment()
	if err != nil {
		t.Fatalf("emailConfigFromEnvironment() error = %v", err)
	}
	if config.SMTPAddr != "smtp.example.com:465" || config.SMTPMode != "tls" || config.IMAPAddr != "imap.example.com:993" || config.From != "bot@example.com" || config.To != "user@example.net" || config.AllowedFrom != "user@example.net" || config.Mailbox != "INBOX" {
		t.Fatalf("config = %#v", config)
	}

	t.Setenv("VTT_CZ_EMAIL_ALLOWED_FROM", "bot@example.com")
	if _, err := emailConfigFromEnvironment(); err == nil {
		t.Fatal("emailConfigFromEnvironment() accepted the bot address as allowed sender")
	}
}

func TestLocalEmailConfigFromEnvironment(t *testing.T) {
	t.Setenv("VTT_CZ_EMAIL_TRANSPORT", "local")
	t.Setenv("VTT_CZ_EMAIL_FROM", "bot@example.com")
	t.Setenv("VTT_CZ_EMAIL_TO", "user@example.net")
	t.Setenv("VTT_CZ_EMAIL_ALLOWED_FROM", "user@example.net")
	t.Setenv("VTT_CZ_EMAIL_MAILDIR", "/home/example/Maildir")
	t.Setenv("VTT_CZ_EMAIL_SENDMAIL", "")

	config, err := emailConfigFromEnvironment()
	if err != nil {
		t.Fatalf("emailConfigFromEnvironment() error = %v", err)
	}
	if config.Transport != "local" || config.SendmailPath != "/usr/sbin/sendmail" || config.MaildirPath != "/home/example/Maildir" || config.To != "user@example.net" {
		t.Fatalf("local config = %#v", config)
	}
}

func TestWorkRootDirFromEnvironment(t *testing.T) {
	want := filepath.Join(t.TempDir(), "jobs")
	t.Setenv("VTT_CZ_WORK_ROOT", want)
	if got := workRootDir(); got != want {
		t.Fatalf("workRootDir() = %q, want %q", got, want)
	}
}

func TestBuildOutgoingEmailRoundTripAttachment(t *testing.T) {
	attachmentPath := filepath.Join(t.TempDir(), "video.en.raw.vtt")
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nHello.\n"
	if err := os.WriteFile(attachmentPath, []byte(vtt), 0o644); err != nil {
		t.Fatal(err)
	}
	config := testEmailConfig()
	contents, err := buildOutgoingEmail(config, outgoingEmail{
		Subject:        "Test titulku",
		Body:           "Priloha je pripravena.\n",
		MessageID:      "<job@example.com>",
		AttachmentPath: attachmentPath,
	})
	if err != nil {
		t.Fatalf("buildOutgoingEmail() error = %v", err)
	}
	message, err := mail.ReadMessage(bytes.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	if message.Header.Get("Message-ID") != "<job@example.com>" {
		t.Fatalf("Message-ID = %q", message.Header.Get("Message-ID"))
	}
	attachment, name, err := extractSingleVTTAttachment(message.Header, message.Body)
	if err != nil {
		t.Fatal(err)
	}
	if name != filepath.Base(attachmentPath) || string(attachment) != vtt {
		t.Fatalf("attachment = %q %q", name, attachment)
	}
}

func TestSendmailEmailSender(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "sendmail")
	messagePath := filepath.Join(t.TempDir(), "message.eml")
	argumentsPath := filepath.Join(t.TempDir(), "arguments.txt")
	writeTestExecutable(t, binPath, `#!/bin/sh
printf '%s\n' "$*" > "$VTT_CZ_TEST_ARGUMENTS"
cat > "$VTT_CZ_TEST_MESSAGE"
`)
	t.Setenv("VTT_CZ_TEST_ARGUMENTS", argumentsPath)
	t.Setenv("VTT_CZ_TEST_MESSAGE", messagePath)
	config := testLocalEmailConfig(t)
	config.SendmailPath = binPath
	sender := sendmailEmailSender{config: config}
	if err := sender.Send(context.Background(), outgoingEmail{
		Subject:   "Test",
		Body:      "Testovaci zprava.\n",
		MessageID: "<test@example.com>",
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "-oi -t -f bot@example.com") {
		t.Fatalf("sendmail arguments = %q", arguments)
	}
	message, err := os.ReadFile(messagePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(message), "To: user@example.net") || !strings.Contains(string(message), "Message-ID: <test@example.com>") {
		t.Fatalf("sendmail message = %q", message)
	}
}

func TestMaildirEmailInboxFetchesInOrder(t *testing.T) {
	config := testLocalEmailConfig(t)
	for _, directory := range []string{"new", "cur", "tmp"} {
		if err := os.MkdirAll(filepath.Join(config.MaildirPath, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	raw := buildTestEmailReply(t, "<original@example.com>", "user@example.net", "translated.cs.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nAhoj.\n")
	firstPath := filepath.Join(config.MaildirPath, "new", "first")
	secondPath := filepath.Join(config.MaildirPath, "cur", "second:2,S")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	firstTime := time.Unix(100, 0)
	secondTime := time.Unix(200, 0)
	if err := os.Chtimes(firstPath, firstTime, firstTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(secondPath, secondTime, secondTime); err != nil {
		t.Fatal(err)
	}

	inbox := maildirEmailInbox{config: config}
	batch, err := inbox.Fetch(context.Background(), emailCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Backend != "maildir" || len(batch.Messages) != 2 || batch.Messages[0].MaildirName != filepath.Join("new", "first") || batch.Messages[1].MaildirName != filepath.Join("cur", "second:2,S") {
		t.Fatalf("maildir batch = %#v", batch)
	}
	cursor := emailCursor{Backend: "maildir"}
	advanceEmailCursor(&cursor, batch.Messages[0])
	nextBatch, err := inbox.Fetch(context.Background(), cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(nextBatch.Messages) != 1 || nextBatch.Messages[0].MaildirName != filepath.Join("cur", "second:2,S") {
		t.Fatalf("next maildir batch = %#v", nextBatch)
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("Maildir message was modified or moved: %v", err)
	}
}

func TestEnsureEmailHandoffSendsOnce(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "20261123-120000")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(workDir, "video.en.raw.vtt")
	csPath := filepath.Join(workDir, "video.cs.vtt")
	outputPath := filepath.Join(workDir, "video.cs.mp4")
	if err := os.WriteFile(rawPath, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nHello.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sender := new(recordingEmailSender)
	config := testEmailConfig()
	for index := 0; index < 2; index++ {
		if err := ensureEmailHandoff(context.Background(), sender, config, workDir, rawPath, csPath, outputPath); err != nil {
			t.Fatalf("ensureEmailHandoff() error = %v", err)
		}
	}
	if len(sender.messages) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sender.messages))
	}
	handoff, err := loadEmailHandoff(filepath.Join(workDir, emailHandoffFileName))
	if err != nil {
		t.Fatal(err)
	}
	if handoff.State != emailStateWaiting || handoff.MessageID == "" || handoff.CzechVTT != "video.cs.vtt" {
		t.Fatalf("handoff = %#v", handoff)
	}
}

func TestHandleInboundEmailAcceptsValidReply(t *testing.T) {
	workRoot := t.TempDir()
	workDir := filepath.Join(workRoot, "20261123-120000")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	handoff := testEmailHandoff("<original@example.com>")
	if err := saveEmailHandoff(filepath.Join(workDir, emailHandoffFileName), handoff); err != nil {
		t.Fatal(err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nAhoj.\n"
	raw := buildTestEmailReply(t, handoff.MessageID, "user@example.net", "translated.cs.vtt", vtt)
	sender := new(recordingEmailSender)
	jobs := make(chan emailJob, 1)
	if err := handleInboundEmail(context.Background(), sender, testEmailConfig(), workRoot, inboundEmail{UID: 10, Raw: raw}, jobs); err != nil {
		t.Fatalf("handleInboundEmail() error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(workDir, "video.cs.vtt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != vtt {
		t.Fatalf("saved VTT = %q", contents)
	}
	updated, err := loadEmailHandoff(filepath.Join(workDir, emailHandoffFileName))
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != emailStateQueued {
		t.Fatalf("state = %q, want %q", updated.State, emailStateQueued)
	}
	select {
	case job := <-jobs:
		if job.workDir != workDir {
			t.Fatalf("queued workDir = %q", job.workDir)
		}
	case <-time.After(time.Second):
		t.Fatal("email job was not queued")
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].Body, "prijato") {
		t.Fatalf("status messages = %#v", sender.messages)
	}
}

func TestHandleInboundEmailRejectsUnauthorizedSender(t *testing.T) {
	workRoot := t.TempDir()
	workDir := filepath.Join(workRoot, "20261123-120000")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	handoff := testEmailHandoff("<original@example.com>")
	if err := saveEmailHandoff(filepath.Join(workDir, emailHandoffFileName), handoff); err != nil {
		t.Fatal(err)
	}
	raw := buildTestEmailReply(t, handoff.MessageID, "attacker@example.org", "translated.cs.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nAhoj.\n")
	sender := new(recordingEmailSender)
	jobs := make(chan emailJob, 1)
	if err := handleInboundEmail(context.Background(), sender, testEmailConfig(), workRoot, inboundEmail{UID: 10, Raw: raw}, jobs); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 0 || len(jobs) != 0 || fileExists(filepath.Join(workDir, "video.cs.vtt")) {
		t.Fatal("unauthorized sender changed the job")
	}
}

func TestHandleInboundEmailRejectsUnknownReplyID(t *testing.T) {
	workRoot := t.TempDir()
	workDir := filepath.Join(workRoot, "20261123-120000")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	handoff := testEmailHandoff("<original@example.com>")
	if err := saveEmailHandoff(filepath.Join(workDir, emailHandoffFileName), handoff); err != nil {
		t.Fatal(err)
	}
	raw := buildTestEmailReply(t, "<unknown@example.com>", "user@example.net", "translated.cs.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nAhoj.\n")
	sender := new(recordingEmailSender)
	jobs := make(chan emailJob, 1)
	if err := handleInboundEmail(context.Background(), sender, testEmailConfig(), workRoot, inboundEmail{UID: 10, Raw: raw}, jobs); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 0 || len(jobs) != 0 || fileExists(filepath.Join(workDir, "video.cs.vtt")) {
		t.Fatal("unknown reply ID changed the job")
	}
}

func TestFinishEmailJobMarksExistingOutputDone(t *testing.T) {
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
	handoff := testEmailHandoff("<original@example.com>")
	handoff.State = emailStateQueued
	if err := saveEmailHandoff(filepath.Join(workDir, emailHandoffFileName), handoff); err != nil {
		t.Fatal(err)
	}
	sender := new(recordingEmailSender)
	finishEmailJob(context.Background(), sender, testEmailConfig(), workDir)
	updated, err := loadEmailHandoff(filepath.Join(workDir, emailHandoffFileName))
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != emailStateDone || updated.LastError != "" || len(sender.messages) != 1 {
		t.Fatalf("updated handoff = %#v, messages = %#v", updated, sender.messages)
	}
}

func TestSaveAndLoadEmailCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), emailCursorFileName)
	want := emailCursor{Version: 1, UIDValidity: 123, LastUID: 456}
	if err := saveEmailCursor(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadEmailCursor(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cursor = %#v, want %#v", got, want)
	}
}

func TestRedactEmailError(t *testing.T) {
	err := redactEmailError(errors.New("login failed for app-password"), "app-password")
	if err == nil || strings.Contains(err.Error(), "app-password") || !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("redacted error = %v", err)
	}
}

func buildTestEmailReply(t *testing.T, inReplyTo, from, filename, contents string) []byte {
	t.Helper()
	dir := t.TempDir()
	attachmentPath := filepath.Join(dir, filename)
	if err := os.WriteFile(attachmentPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	config := testEmailConfig()
	config.From = from
	config.To = "bot@example.com"
	raw, err := buildOutgoingEmail(config, outgoingEmail{
		Subject:        "Re: titulky",
		Body:           "Posilam vysledek.\n",
		MessageID:      "<reply@example.net>",
		InReplyTo:      inReplyTo,
		AttachmentPath: attachmentPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testEmailHandoff(messageID string) emailHandoff {
	now := time.Now().UTC().Format(time.RFC3339)
	return emailHandoff{
		Version:     1,
		JobID:       "20261123-120000",
		MessageID:   messageID,
		Recipient:   "user@example.net",
		RawVTT:      "video.en.raw.vtt",
		CzechVTT:    "video.cs.vtt",
		OutputVideo: "video.cs.mp4",
		State:       emailStateWaiting,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func testEmailConfig() emailConfig {
	return emailConfig{
		Transport:   "smtp-imap",
		SMTPAddr:    "smtp.example.com:465",
		SMTPMode:    "tls",
		IMAPAddr:    "imap.example.com:993",
		Username:    "bot@example.com",
		Password:    "app-password",
		From:        "bot@example.com",
		To:          "user@example.net",
		AllowedFrom: "user@example.net",
		Mailbox:     "INBOX",
	}
}

func testLocalEmailConfig(t *testing.T) emailConfig {
	t.Helper()
	return emailConfig{
		Transport:    "local",
		From:         "bot@example.com",
		To:           "user@example.net",
		AllowedFrom:  "user@example.net",
		SendmailPath: "/usr/sbin/sendmail",
		MaildirPath:  filepath.Join(t.TempDir(), "Maildir"),
	}
}

func setEmailTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("VTT_CZ_EMAIL_SMTP_ADDR", "smtp.example.com:465")
	t.Setenv("VTT_CZ_EMAIL_TRANSPORT", "")
	t.Setenv("VTT_CZ_EMAIL_SMTP_MODE", "")
	t.Setenv("VTT_CZ_EMAIL_IMAP_ADDR", "imap.example.com:993")
	t.Setenv("VTT_CZ_EMAIL_USERNAME", "bot@example.com")
	t.Setenv("VTT_CZ_EMAIL_PASSWORD", "app-password")
	t.Setenv("VTT_CZ_EMAIL_FROM", "Bot <bot@example.com>")
	t.Setenv("VTT_CZ_EMAIL_TO", "User <user@example.net>")
	t.Setenv("VTT_CZ_EMAIL_ALLOWED_FROM", "user@example.net")
	t.Setenv("VTT_CZ_EMAIL_MAILBOX", "")
}

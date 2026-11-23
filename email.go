package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

const (
	emailHandoffFileName = ".email-handoff.json"
	emailCursorFileName  = ".email-inbox-cursor.json"
	emailMaxVTTBytes     = 5 * 1024 * 1024
	emailMaxMessageBytes = 16 * 1024 * 1024

	emailStateWaiting   = "waiting_for_translation"
	emailStateQueued    = "queued_for_rendering"
	emailStateRendering = "rendering"
	emailStateDone      = "done"
	emailStateFailed    = "failed"
)

type emailConfig struct {
	Transport    string
	SMTPAddr     string
	SMTPMode     string
	IMAPAddr     string
	Username     string
	Password     string
	From         string
	To           string
	AllowedFrom  string
	Mailbox      string
	SendmailPath string
	MaildirPath  string
}

type emailHandoff struct {
	Version     int    `json:"version"`
	JobID       string `json:"job_id"`
	MessageID   string `json:"message_id"`
	Recipient   string `json:"recipient"`
	RawVTT      string `json:"raw_vtt"`
	CzechVTT    string `json:"czech_vtt"`
	OutputVideo string `json:"output_video"`
	State       string `json:"state"`
	LastError   string `json:"last_error,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type emailCursor struct {
	Version         int    `json:"version"`
	Backend         string `json:"backend,omitempty"`
	UIDValidity     uint32 `json:"uid_validity,omitempty"`
	LastUID         uint32 `json:"last_uid,omitempty"`
	LastMaildirTime int64  `json:"last_maildir_time,omitempty"`
	LastMaildirName string `json:"last_maildir_name,omitempty"`
}

type outgoingEmail struct {
	Subject        string
	Body           string
	MessageID      string
	InReplyTo      string
	AttachmentPath string
}

type inboundEmail struct {
	UID         uint32
	MaildirTime int64
	MaildirName string
	Raw         []byte
	TooLarge    bool
}

type emailFetchBatch struct {
	Backend     string
	UIDValidity uint32
	Messages    []inboundEmail
}

type emailSender interface {
	Send(context.Context, outgoingEmail) error
}

type emailInbox interface {
	Fetch(context.Context, emailCursor) (emailFetchBatch, error)
}

type smtpEmailSender struct {
	config emailConfig
}

type imapEmailInbox struct {
	config emailConfig
}

type sendmailEmailSender struct {
	config emailConfig
}

type maildirEmailInbox struct {
	config emailConfig
}

type emailJob struct {
	workDir string
}

func emailConfigFromEnvironment() (emailConfig, error) {
	config := emailConfig{
		Transport:    strings.ToLower(strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_TRANSPORT"))),
		SMTPAddr:     strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_SMTP_ADDR")),
		SMTPMode:     strings.ToLower(strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_SMTP_MODE"))),
		IMAPAddr:     strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_IMAP_ADDR")),
		Username:     strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_USERNAME")),
		Password:     os.Getenv("VTT_CZ_EMAIL_PASSWORD"),
		From:         strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_FROM")),
		To:           strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_TO")),
		AllowedFrom:  strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_ALLOWED_FROM")),
		Mailbox:      strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_MAILBOX")),
		SendmailPath: strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_SENDMAIL")),
		MaildirPath:  strings.TrimSpace(os.Getenv("VTT_CZ_EMAIL_MAILDIR")),
	}
	if config.Transport == "" {
		config.Transport = "smtp-imap"
	}
	if config.Mailbox == "" {
		config.Mailbox = "INBOX"
	}
	if config.SMTPMode == "" {
		config.SMTPMode = "tls"
	}
	if config.SendmailPath == "" {
		config.SendmailPath = "/usr/sbin/sendmail"
	}
	for name, value := range map[string]string{
		"VTT_CZ_EMAIL_FROM":         config.From,
		"VTT_CZ_EMAIL_TO":           config.To,
		"VTT_CZ_EMAIL_ALLOWED_FROM": config.AllowedFrom,
	} {
		if value == "" {
			return emailConfig{}, fmt.Errorf("chybi %s", name)
		}
	}
	if config.Transport != "local" && config.Transport != "smtp-imap" {
		return emailConfig{}, fmt.Errorf("VTT_CZ_EMAIL_TRANSPORT musi byt local nebo smtp-imap")
	}
	if config.Transport == "local" {
		if config.MaildirPath == "" {
			return emailConfig{}, fmt.Errorf("chybi VTT_CZ_EMAIL_MAILDIR")
		}
		if !filepath.IsAbs(config.MaildirPath) || !filepath.IsAbs(config.SendmailPath) {
			return emailConfig{}, fmt.Errorf("VTT_CZ_EMAIL_MAILDIR a VTT_CZ_EMAIL_SENDMAIL musi byt absolutni cesty")
		}
	} else {
		for name, value := range map[string]string{
			"VTT_CZ_EMAIL_SMTP_ADDR": config.SMTPAddr,
			"VTT_CZ_EMAIL_IMAP_ADDR": config.IMAPAddr,
			"VTT_CZ_EMAIL_USERNAME":  config.Username,
			"VTT_CZ_EMAIL_PASSWORD":  config.Password,
		} {
			if value == "" {
				return emailConfig{}, fmt.Errorf("chybi %s", name)
			}
		}
		for name, address := range map[string]string{
			"VTT_CZ_EMAIL_SMTP_ADDR": config.SMTPAddr,
			"VTT_CZ_EMAIL_IMAP_ADDR": config.IMAPAddr,
		} {
			if _, _, err := net.SplitHostPort(address); err != nil {
				return emailConfig{}, fmt.Errorf("%s musi byt ve tvaru server:port", name)
			}
		}
		if config.SMTPMode != "tls" && config.SMTPMode != "starttls" {
			return emailConfig{}, fmt.Errorf("VTT_CZ_EMAIL_SMTP_MODE musi byt tls nebo starttls")
		}
	}
	for name, address := range map[string]string{
		"VTT_CZ_EMAIL_FROM":         config.From,
		"VTT_CZ_EMAIL_TO":           config.To,
		"VTT_CZ_EMAIL_ALLOWED_FROM": config.AllowedFrom,
	} {
		parsed, err := parseEmailAddress(address)
		if err != nil {
			return emailConfig{}, fmt.Errorf("%s: %w", name, err)
		}
		switch name {
		case "VTT_CZ_EMAIL_FROM":
			config.From = parsed
		case "VTT_CZ_EMAIL_TO":
			config.To = parsed
		case "VTT_CZ_EMAIL_ALLOWED_FROM":
			config.AllowedFrom = parsed
		}
	}
	if strings.ContainsAny(config.Mailbox, "\r\n") {
		return emailConfig{}, fmt.Errorf("VTT_CZ_EMAIL_MAILBOX obsahuje neplatne znaky")
	}
	if strings.EqualFold(config.From, config.AllowedFrom) {
		return emailConfig{}, fmt.Errorf("VTT_CZ_EMAIL_FROM a VTT_CZ_EMAIL_ALLOWED_FROM musi byt ruzne adresy; pouzij vyhrazenou schranku pro vtt-cz")
	}
	return config, nil
}

func parseEmailAddress(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("e-mailova adresa obsahuje neplatne znaky")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address == "" {
		return "", fmt.Errorf("neplatna e-mailova adresa")
	}
	return strings.ToLower(address.Address), nil
}

func ensureEmailHandoffFromEnvironment(ctx context.Context, workDir, rawVTTPath, csVTTPath, outputVideoPath string) error {
	config, err := emailConfigFromEnvironment()
	if err != nil {
		return err
	}
	return ensureEmailHandoff(ctx, emailSenderForConfig(config), config, workDir, rawVTTPath, csVTTPath, outputVideoPath)
}

func emailSenderForConfig(config emailConfig) emailSender {
	if config.Transport == "local" {
		return sendmailEmailSender{config: config}
	}
	return smtpEmailSender{config: config}
}

func emailInboxForConfig(config emailConfig) emailInbox {
	if config.Transport == "local" {
		return maildirEmailInbox{config: config}
	}
	return imapEmailInbox{config: config}
}

func ensureEmailHandoff(ctx context.Context, sender emailSender, config emailConfig, workDir, rawVTTPath, csVTTPath, outputVideoPath string) error {
	handoffPath := filepath.Join(workDir, emailHandoffFileName)
	if existing, err := loadEmailHandoff(handoffPath); err == nil {
		if !strings.EqualFold(existing.Recipient, config.To) {
			return fmt.Errorf("uloha uz byla odeslana na jinou e-mailovou adresu")
		}
		if existing.MessageID != "" {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	jobID := filepath.Base(filepath.Clean(workDir))
	messageID, err := newEmailMessageID(jobID, config.From)
	if err != nil {
		return err
	}
	message := outgoingEmail{
		Subject:        fmt.Sprintf("[vtt-cz:%s] Anglicke titulky k prekladu", jobID),
		MessageID:      messageID,
		AttachmentPath: rawVTTPath,
		Body:           "Anglicke titulky jsou pripravene. Prilohu zpracuj v ChatGPT Work a finalni .cs.vtt posli jako odpoved na tento e-mail.\n",
	}
	if err := sender.Send(ctx, message); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	handoff := emailHandoff{
		Version:     1,
		JobID:       jobID,
		MessageID:   messageID,
		Recipient:   config.To,
		RawVTT:      filepath.Base(rawVTTPath),
		CzechVTT:    filepath.Base(csVTTPath),
		OutputVideo: filepath.Base(outputVideoPath),
		State:       emailStateWaiting,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	return saveEmailHandoff(handoffPath, handoff)
}

func runEmailListenerFromEnvironment(ctx context.Context, workRoot string) error {
	config, err := emailConfigFromEnvironment()
	if err != nil {
		return err
	}
	return runEmailListener(ctx, emailSenderForConfig(config), emailInboxForConfig(config), config, workRoot)
}

func runEmailListener(ctx context.Context, sender emailSender, inbox emailInbox, config emailConfig, workRoot string) error {
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		return fmt.Errorf("vytvoreni korenove slozky uloh: %w", err)
	}
	jobs := make(chan emailJob, 32)
	go emailJobWorker(ctx, sender, config, jobs)
	if err := enqueuePendingEmailJobs(workRoot, jobs); err != nil {
		return err
	}

	cursorPath := filepath.Join(workRoot, emailCursorFileName)
	cursor, err := loadEmailCursor(cursorPath)
	if err != nil {
		return err
	}
	fmt.Println("E-mailovy listener bezi pro povoleneho odesilatele", config.AllowedFrom)
	fmt.Println("Pracovni slozka:", workRoot)

	for {
		batch, err := inbox.Fetch(ctx, cursor)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintln(os.Stderr, "Varovani: kontrola e-mailove schranky selhala:", redactEmailError(err, config.Password))
			if !waitForEmailContext(ctx, 10*time.Second) {
				return nil
			}
			continue
		}
		if cursor.Backend != "" && cursor.Backend != batch.Backend {
			cursor = emailCursor{Version: 1, Backend: batch.Backend}
			if err := saveEmailCursor(cursorPath, cursor); err != nil {
				return err
			}
			continue
		}
		cursor.Backend = batch.Backend
		if batch.Backend == "imap" && cursor.UIDValidity != 0 && batch.UIDValidity != 0 && cursor.UIDValidity != batch.UIDValidity {
			cursor = emailCursor{Version: 1, Backend: batch.Backend, UIDValidity: batch.UIDValidity}
			if err := saveEmailCursor(cursorPath, cursor); err != nil {
				return err
			}
			continue
		}
		if batch.Backend == "imap" && cursor.UIDValidity == 0 {
			cursor.UIDValidity = batch.UIDValidity
		}
		for _, message := range batch.Messages {
			if err := handleInboundEmail(ctx, sender, config, workRoot, message, jobs); err != nil {
				fmt.Fprintln(os.Stderr, "Varovani: zpracovani e-mailu selhalo:", redactEmailError(err, config.Password))
				break
			}
			advanceEmailCursor(&cursor, message)
			if err := saveEmailCursor(cursorPath, cursor); err != nil {
				return err
			}
		}
		if !waitForEmailContext(ctx, 20*time.Second) {
			return nil
		}
	}
}

func advanceEmailCursor(cursor *emailCursor, message inboundEmail) {
	if cursor.Backend == "maildir" {
		cursor.LastMaildirTime = message.MaildirTime
		cursor.LastMaildirName = message.MaildirName
		return
	}
	if message.UID > cursor.LastUID {
		cursor.LastUID = message.UID
	}
}

func handleInboundEmail(ctx context.Context, sender emailSender, config emailConfig, workRoot string, incoming inboundEmail, jobs chan<- emailJob) error {
	if incoming.TooLarge {
		return nil
	}
	message, err := mail.ReadMessage(bytes.NewReader(incoming.Raw))
	if err != nil {
		return nil
	}
	from, err := singleFromAddress(message.Header.Get("From"))
	if err != nil || !strings.EqualFold(from, config.AllowedFrom) {
		return nil
	}
	replyIDs := referencedMessageIDs(message.Header)
	workDir, handoff, err := findEmailHandoffByMessageIDs(workRoot, replyIDs)
	if err != nil {
		return nil
	}
	if handoff.State != emailStateWaiting {
		return nil
	}

	contents, _, err := extractSingleVTTAttachment(message.Header, message.Body)
	if err != nil {
		return sendEmailStatus(ctx, sender, config, handoff, "Vraceny e-mail nelze prijmout: "+err.Error())
	}
	temporary, err := os.CreateTemp(workDir, ".email-czech-*.vtt")
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
	if err := validateVTTFile(temporaryPath); err != nil {
		return sendEmailStatus(ctx, sender, config, handoff, "Vraceny VTT neni platny: "+err.Error())
	}
	csVTTPath, err := safeEmailJobPath(workDir, handoff.CzechVTT)
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, csVTTPath); err != nil {
		return fmt.Errorf("ulozeni ceskeho VTT: %w", err)
	}
	handoff.State = emailStateQueued
	handoff.LastError = ""
	handoff.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := saveEmailHandoff(filepath.Join(workDir, emailHandoffFileName), handoff); err != nil {
		return err
	}
	jobs <- emailJob{workDir: workDir}
	return sendEmailStatus(ctx, sender, config, handoff, "Ceske VTT bylo prijato a overeno. Spoustim vypaleni titulku.")
}

func emailJobWorker(ctx context.Context, sender emailSender, config emailConfig, jobs <-chan emailJob) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			finishEmailJob(ctx, sender, config, job.workDir)
		}
	}
}

func finishEmailJob(ctx context.Context, sender emailSender, config emailConfig, workDir string) {
	handoffPath := filepath.Join(workDir, emailHandoffFileName)
	handoff, err := loadEmailHandoff(handoffPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "E-mailovou ulohu nelze nacist:", err)
		return
	}
	handoff.State = emailStateRendering
	handoff.LastError = ""
	handoff.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := saveEmailHandoff(handoffPath, handoff); err != nil {
		fmt.Fprintln(os.Stderr, "E-mailovou ulohu nelze oznacit jako rendering:", err)
		return
	}

	videoPath, mp3Path, found, err := findExistingVideoAndMP3(workDir)
	if err == nil && !found {
		err = fmt.Errorf("v pracovni slozce chybi odpovidajici video a MP3")
	}
	if err == nil {
		var rawVTTPath, csVTTPath, outputVideoPath string
		rawVTTPath, err = safeEmailJobPath(workDir, handoff.RawVTT)
		if err == nil {
			csVTTPath, err = safeEmailJobPath(workDir, handoff.CzechVTT)
		}
		if err == nil {
			outputVideoPath, err = safeEmailJobPath(workDir, handoff.OutputVideo)
		}
		if err == nil {
			err = runWorkMode(workDir, videoPath, mp3Path, rawVTTPath, csVTTPath, outputVideoPath, fileExists(filepath.Join(workDir, ".reel-mode")), true)
		}
	}
	if err != nil {
		handoff.State = emailStateFailed
		handoff.LastError = err.Error()
		handoff.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		_ = saveEmailHandoff(handoffPath, handoff)
		_ = sendEmailStatus(ctx, sender, config, handoff, "Uloha selhala pri dokonceni: "+err.Error())
		return
	}

	handoff.State = emailStateDone
	handoff.LastError = ""
	handoff.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = saveEmailHandoff(handoffPath, handoff)
	_ = sendEmailStatus(ctx, sender, config, handoff, "Uloha je hotova. Video s ceskymi titulky je na VPS v "+filepath.Join(workDir, handoff.OutputVideo))
}

func sendEmailStatus(ctx context.Context, sender emailSender, config emailConfig, handoff emailHandoff, body string) error {
	messageID, err := newEmailMessageID(handoff.JobID, config.From)
	if err != nil {
		return err
	}
	return sender.Send(ctx, outgoingEmail{
		Subject:   fmt.Sprintf("Re: [vtt-cz:%s] stav ulohy", handoff.JobID),
		Body:      body + "\n",
		MessageID: messageID,
		InReplyTo: handoff.MessageID,
	})
}

func enqueuePendingEmailJobs(workRoot string, jobs chan<- emailJob) error {
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workDir := filepath.Join(workRoot, entry.Name())
		handoff, err := loadEmailHandoff(filepath.Join(workDir, emailHandoffFileName))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if handoff.State != emailStateQueued && handoff.State != emailStateRendering {
			continue
		}
		csVTTPath, err := safeEmailJobPath(workDir, handoff.CzechVTT)
		if err != nil {
			return err
		}
		if fileExists(csVTTPath) {
			jobs <- emailJob{workDir: workDir}
		}
	}
	return nil
}

func findEmailHandoffByMessageIDs(workRoot string, messageIDs []string) (string, emailHandoff, error) {
	if len(messageIDs) == 0 {
		return "", emailHandoff{}, fmt.Errorf("e-mail nema In-Reply-To ani References")
	}
	wanted := make(map[string]struct{}, len(messageIDs))
	for _, id := range messageIDs {
		wanted[id] = struct{}{}
	}
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		return "", emailHandoff{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workDir := filepath.Join(workRoot, entry.Name())
		handoff, err := loadEmailHandoff(filepath.Join(workDir, emailHandoffFileName))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", emailHandoff{}, err
		}
		if _, ok := wanted[handoff.MessageID]; ok {
			return workDir, handoff, nil
		}
	}
	return "", emailHandoff{}, fmt.Errorf("odpovidajici e-mailova uloha nebyla nalezena")
}

func referencedMessageIDs(header mail.Header) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, name := range []string{"In-Reply-To", "References"} {
		for _, field := range header[textproto.CanonicalMIMEHeaderKey(name)] {
			for _, token := range strings.Fields(field) {
				if !strings.HasPrefix(token, "<") || !strings.HasSuffix(token, ">") {
					continue
				}
				if _, ok := seen[token]; ok {
					continue
				}
				seen[token] = struct{}{}
				result = append(result, token)
			}
		}
	}
	return result
}

func singleFromAddress(value string) (string, error) {
	addresses, err := mail.ParseAddressList(value)
	if err != nil || len(addresses) != 1 {
		return "", fmt.Errorf("e-mail nema prave jednoho odesilatele")
	}
	return strings.ToLower(addresses[0].Address), nil
}

func extractSingleVTTAttachment(header mail.Header, body io.Reader) ([]byte, string, error) {
	var attachments []emailAttachment
	if err := collectVTTAttachments(textproto.MIMEHeader(header), body, &attachments, 0); err != nil {
		return nil, "", err
	}
	if len(attachments) == 0 {
		return nil, "", fmt.Errorf("chybi priloha .vtt")
	}
	if len(attachments) > 1 {
		return nil, "", fmt.Errorf("e-mail obsahuje vice priloh .vtt")
	}
	return attachments[0].contents, attachments[0].name, nil
}

type emailAttachment struct {
	name     string
	contents []byte
}

func collectVTTAttachments(header textproto.MIMEHeader, body io.Reader, attachments *[]emailAttachment, depth int) error {
	if depth > 8 {
		return fmt.Errorf("MIME zprava je prilis hluboce vnorena")
	}
	mediaType := "text/plain"
	parameters := map[string]string{}
	if value := header.Get("Content-Type"); value != "" {
		parsedType, parsedParameters, err := mime.ParseMediaType(value)
		if err != nil {
			return fmt.Errorf("neplatny Content-Type: %w", err)
		}
		mediaType, parameters = strings.ToLower(parsedType), parsedParameters
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := parameters["boundary"]
		if boundary == "" {
			return fmt.Errorf("multipart e-mail nema boundary")
		}
		reader := multipart.NewReader(body, boundary)
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := collectVTTAttachments(part.Header, part, attachments, depth+1); err != nil {
				part.Close()
				return err
			}
			part.Close()
		}
	}

	filename := parameters["name"]
	if disposition := header.Get("Content-Disposition"); disposition != "" {
		_, dispositionParameters, err := mime.ParseMediaType(disposition)
		if err != nil {
			return fmt.Errorf("neplatny Content-Disposition: %w", err)
		}
		if dispositionParameters["filename"] != "" {
			filename = dispositionParameters["filename"]
		}
	}
	if decoded, err := new(mime.WordDecoder).DecodeHeader(filename); err == nil {
		filename = decoded
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".vtt") {
		return nil
	}
	decodedBody, err := decodeEmailBody(header.Get("Content-Transfer-Encoding"), body)
	if err != nil {
		return err
	}
	contents, err := io.ReadAll(io.LimitReader(decodedBody, emailMaxVTTBytes+1))
	if err != nil {
		return err
	}
	if len(contents) > emailMaxVTTBytes {
		return fmt.Errorf("VTT priloha prekrocila limit 5 MB")
	}
	*attachments = append(*attachments, emailAttachment{name: filepath.Base(filename), contents: contents})
	return nil
}

func decodeEmailBody(encoding string, body io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "7bit", "8bit", "binary":
		return body, nil
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body), nil
	case "quoted-printable":
		return quotedprintable.NewReader(body), nil
	default:
		return nil, fmt.Errorf("nepodporovane Content-Transfer-Encoding %q", encoding)
	}
}

func safeEmailJobPath(workDir, name string) (string, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", fmt.Errorf("neplatny nazev souboru v e-mailovem handoffu %q", name)
	}
	return filepath.Join(workDir, name), nil
}

func loadEmailHandoff(path string) (emailHandoff, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return emailHandoff{}, err
	}
	var handoff emailHandoff
	if err := json.Unmarshal(contents, &handoff); err != nil {
		return emailHandoff{}, fmt.Errorf("cteni %s: %w", path, err)
	}
	if handoff.Version != 1 || handoff.JobID == "" || handoff.MessageID == "" || handoff.Recipient == "" {
		return emailHandoff{}, fmt.Errorf("%s neobsahuje platny e-mailovy handoff", path)
	}
	for _, name := range []string{handoff.RawVTT, handoff.CzechVTT, handoff.OutputVideo} {
		if _, err := safeEmailJobPath(filepath.Dir(path), name); err != nil {
			return emailHandoff{}, err
		}
	}
	return handoff, nil
}

func saveEmailHandoff(path string, handoff emailHandoff) error {
	contents, err := json.MarshalIndent(handoff, "", "  ")
	if err != nil {
		return err
	}
	return writeEmailStateAtomically(path, append(contents, '\n'))
}

func loadEmailCursor(path string) (emailCursor, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return emailCursor{Version: 1}, nil
	}
	if err != nil {
		return emailCursor{}, err
	}
	var cursor emailCursor
	if err := json.Unmarshal(contents, &cursor); err != nil || cursor.Version != 1 {
		return emailCursor{}, fmt.Errorf("%s neobsahuje platny e-mailovy cursor", path)
	}
	return cursor, nil
}

func saveEmailCursor(path string, cursor emailCursor) error {
	cursor.Version = 1
	contents, err := json.MarshalIndent(cursor, "", "  ")
	if err != nil {
		return err
	}
	return writeEmailStateAtomically(path, append(contents, '\n'))
}

func writeEmailStateAtomically(path string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".email-state-*")
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

func newEmailMessageID(jobID, fromAddress string) (string, error) {
	randomBytes := make([]byte, 12)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("vytvoreni Message-ID: %w", err)
	}
	safeJobID := strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			return character
		}
		return '-'
	}, jobID)
	domain := "localhost"
	if separator := strings.LastIndex(fromAddress, "@"); separator >= 0 && separator+1 < len(fromAddress) {
		domain = fromAddress[separator+1:]
	}
	return fmt.Sprintf("<vtt-cz.%s.%s@%s>", safeJobID, hex.EncodeToString(randomBytes), domain), nil
}

func (sender smtpEmailSender) Send(ctx context.Context, message outgoingEmail) error {
	contents, err := buildOutgoingEmail(sender.config, message)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(sender.config.SMTPAddr)
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", sender.config.SMTPAddr)
	if err != nil {
		return redactEmailError(fmt.Errorf("SMTP spojeni: %w", err), sender.config.Password)
	}
	_ = connection.SetDeadline(time.Now().Add(45 * time.Second))
	var client *smtp.Client
	if sender.config.SMTPMode == "tls" {
		tlsConnection := tls.Client(connection, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			connection.Close()
			return redactEmailError(fmt.Errorf("SMTP TLS: %w", err), sender.config.Password)
		}
		client, err = smtp.NewClient(tlsConnection, host)
	} else {
		client, err = smtp.NewClient(connection, host)
		if err == nil {
			err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		}
	}
	if err != nil {
		connection.Close()
		return redactEmailError(err, sender.config.Password)
	}
	defer client.Close()
	if err := client.Auth(smtp.PlainAuth("", sender.config.Username, sender.config.Password, host)); err != nil {
		return redactEmailError(fmt.Errorf("SMTP prihlaseni: %w", err), sender.config.Password)
	}
	if err := client.Mail(sender.config.From); err != nil {
		return err
	}
	if err := client.Rcpt(sender.config.To); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(contents); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func (sender sendmailEmailSender) Send(ctx context.Context, message outgoingEmail) error {
	contents, err := buildOutgoingEmail(sender.config, message)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, sender.config.SendmailPath, "-oi", "-t", "-f", sender.config.From)
	command.Stdin = bytes.NewReader(contents)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 2000 {
			message = message[:2000] + "..."
		}
		if message == "" {
			return fmt.Errorf("lokalni sendmail: %w", err)
		}
		return fmt.Errorf("lokalni sendmail: %w: %s", err, message)
	}
	return nil
}

func buildOutgoingEmail(config emailConfig, message outgoingEmail) ([]byte, error) {
	if message.MessageID == "" || strings.ContainsAny(message.Subject+message.MessageID+message.InReplyTo, "\r\n") {
		return nil, fmt.Errorf("e-mail obsahuje neplatne hlavicky")
	}
	var output bytes.Buffer
	fmt.Fprintf(&output, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&output, "From: %s\r\n", config.From)
	fmt.Fprintf(&output, "To: %s\r\n", config.To)
	fmt.Fprintf(&output, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", message.Subject))
	fmt.Fprintf(&output, "Message-ID: %s\r\n", message.MessageID)
	if message.InReplyTo != "" {
		fmt.Fprintf(&output, "In-Reply-To: %s\r\nReferences: %s\r\n", message.InReplyTo, message.InReplyTo)
	}
	output.WriteString("MIME-Version: 1.0\r\n")
	if message.AttachmentPath == "" {
		output.WriteString("Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		writer := quotedprintable.NewWriter(&output)
		_, err := writer.Write([]byte(strings.ReplaceAll(message.Body, "\n", "\r\n")))
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		return output.Bytes(), err
	}

	multipartWriter := multipart.NewWriter(&output)
	output.WriteString("Content-Type: multipart/mixed; boundary=" + strconv.Quote(multipartWriter.Boundary()) + "\r\n\r\n")
	textHeader := make(textproto.MIMEHeader)
	textHeader.Set("Content-Type", "text/plain; charset=UTF-8")
	textHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	textPart, err := multipartWriter.CreatePart(textHeader)
	if err != nil {
		return nil, err
	}
	quotedWriter := quotedprintable.NewWriter(textPart)
	if _, err := quotedWriter.Write([]byte(strings.ReplaceAll(message.Body, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := quotedWriter.Close(); err != nil {
		return nil, err
	}

	file, err := os.Open(message.AttachmentPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	attachmentHeader := make(textproto.MIMEHeader)
	attachmentHeader.Set("Content-Type", mime.FormatMediaType("text/vtt", map[string]string{"name": filepath.Base(message.AttachmentPath)}))
	attachmentHeader.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(message.AttachmentPath)}))
	attachmentHeader.Set("Content-Transfer-Encoding", "base64")
	attachmentPart, err := multipartWriter.CreatePart(attachmentHeader)
	if err != nil {
		return nil, err
	}
	encoded := base64.NewEncoder(base64.StdEncoding, newBase64LineWriter(attachmentPart))
	if _, err := io.Copy(encoded, file); err != nil {
		return nil, err
	}
	if err := encoded.Close(); err != nil {
		return nil, err
	}
	if err := multipartWriter.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

type base64LineWriter struct {
	writer io.Writer
	column int
}

func newBase64LineWriter(writer io.Writer) *base64LineWriter {
	return &base64LineWriter{writer: writer}
}

func (writer *base64LineWriter) Write(contents []byte) (int, error) {
	written := 0
	for len(contents) > 0 {
		space := 76 - writer.column
		count := len(contents)
		if count > space {
			count = space
		}
		if _, err := writer.writer.Write(contents[:count]); err != nil {
			return written, err
		}
		written += count
		writer.column += count
		contents = contents[count:]
		if writer.column == 76 {
			if _, err := io.WriteString(writer.writer, "\r\n"); err != nil {
				return written, err
			}
			writer.column = 0
		}
	}
	return written, nil
}

func (inbox maildirEmailInbox) Fetch(ctx context.Context, cursor emailCursor) (emailFetchBatch, error) {
	type candidate struct {
		path    string
		name    string
		modTime int64
	}
	var candidates []candidate
	for _, directory := range []string{"new", "cur"} {
		directoryPath := filepath.Join(inbox.config.MaildirPath, directory)
		entries, err := os.ReadDir(directoryPath)
		if err != nil {
			return emailFetchBatch{Backend: "maildir"}, fmt.Errorf("cteni Maildir %s: %w", directoryPath, err)
		}
		for _, entry := range entries {
			select {
			case <-ctx.Done():
				return emailFetchBatch{Backend: "maildir"}, ctx.Err()
			default:
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return emailFetchBatch{Backend: "maildir"}, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			name := filepath.Join(directory, entry.Name())
			modTime := info.ModTime().UnixNano()
			if !maildirCursorBefore(cursor, modTime, name) {
				continue
			}
			candidates = append(candidates, candidate{
				path:    filepath.Join(directoryPath, entry.Name()),
				name:    name,
				modTime: modTime,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].modTime == candidates[j].modTime {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].modTime < candidates[j].modTime
	})
	if len(candidates) > 100 {
		candidates = candidates[:100]
	}

	result := make([]inboundEmail, 0, len(candidates))
	for _, candidate := range candidates {
		file, err := os.Open(candidate.path)
		if err != nil {
			return emailFetchBatch{Backend: "maildir"}, err
		}
		contents, readErr := io.ReadAll(io.LimitReader(file, emailMaxMessageBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return emailFetchBatch{Backend: "maildir"}, readErr
		}
		if closeErr != nil {
			return emailFetchBatch{Backend: "maildir"}, closeErr
		}
		message := inboundEmail{MaildirTime: candidate.modTime, MaildirName: candidate.name}
		if len(contents) > emailMaxMessageBytes {
			message.TooLarge = true
		} else {
			message.Raw = contents
		}
		result = append(result, message)
	}
	return emailFetchBatch{Backend: "maildir", Messages: result}, nil
}

func maildirCursorBefore(cursor emailCursor, modTime int64, name string) bool {
	if modTime > cursor.LastMaildirTime {
		return true
	}
	return modTime == cursor.LastMaildirTime && name > cursor.LastMaildirName
}

func (inbox imapEmailInbox) Fetch(ctx context.Context, cursor emailCursor) (emailFetchBatch, error) {
	host, _, err := net.SplitHostPort(inbox.config.IMAPAddr)
	if err != nil {
		return emailFetchBatch{}, err
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	client, err := imapclient.DialWithDialerTLS(dialer, inbox.config.IMAPAddr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return emailFetchBatch{}, redactEmailError(fmt.Errorf("IMAP spojeni: %w", err), inbox.config.Password)
	}
	client.Timeout = 45 * time.Second
	defer client.Logout()
	if err := client.Login(inbox.config.Username, inbox.config.Password); err != nil {
		return emailFetchBatch{}, redactEmailError(fmt.Errorf("IMAP prihlaseni: %w", err), inbox.config.Password)
	}
	mailbox, err := client.Select(inbox.config.Mailbox, true)
	if err != nil {
		return emailFetchBatch{}, err
	}
	uids, err := client.UidSearch(imap.NewSearchCriteria())
	if err != nil {
		return emailFetchBatch{Backend: "imap", UIDValidity: mailbox.UidValidity}, err
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	var result []inboundEmail
	for _, uid := range uids {
		if uid <= cursor.LastUID {
			continue
		}
		message, err := fetchIMAPMessage(client, uid)
		if err != nil {
			return emailFetchBatch{Backend: "imap", UIDValidity: mailbox.UidValidity}, err
		}
		result = append(result, message)
		if len(result) >= 100 {
			break
		}
		select {
		case <-ctx.Done():
			return emailFetchBatch{Backend: "imap", UIDValidity: mailbox.UidValidity, Messages: result}, ctx.Err()
		default:
		}
	}
	return emailFetchBatch{Backend: "imap", UIDValidity: mailbox.UidValidity, Messages: result}, nil
}

func fetchIMAPMessage(client *imapclient.Client, uid uint32) (inboundEmail, error) {
	sequence := new(imap.SeqSet)
	sequence.AddNum(uid)
	section := &imap.BodySectionName{Peek: true}
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.UidFetch(sequence, []imap.FetchItem{imap.FetchUid, section.FetchItem()}, messages)
	}()
	message, ok := <-messages
	if !ok || message == nil {
		if err := <-done; err != nil {
			return inboundEmail{}, err
		}
		return inboundEmail{}, fmt.Errorf("IMAP nevratil zpravu UID %d", uid)
	}
	body := message.GetBody(section)
	if body == nil {
		return inboundEmail{}, fmt.Errorf("IMAP nevratil telo zpravy UID %d", uid)
	}
	contents, readErr := io.ReadAll(io.LimitReader(body, emailMaxMessageBytes+1))
	if err := <-done; err != nil {
		return inboundEmail{}, err
	}
	if readErr != nil {
		return inboundEmail{}, readErr
	}
	if len(contents) > emailMaxMessageBytes {
		return inboundEmail{UID: uid, TooLarge: true}, nil
	}
	return inboundEmail{UID: uid, Raw: contents}, nil
}

func redactEmailError(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "<redacted>"))
}

func waitForEmailContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

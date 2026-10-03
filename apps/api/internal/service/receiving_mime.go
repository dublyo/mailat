package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dublyo/mailat/api/internal/model"
	"golang.org/x/net/html/charset"
)

const maxIncomingBytes = 40 * 1024 * 1024
const maxMIMEParts = 200
const maxMIMEDepth = 16

type parsedIncoming struct {
	Header      mail.Header
	Text, HTML  string
	Attachments []AttachmentInfo
}

type AttachmentInfo struct {
	Filename, ContentType, S3Key, S3Bucket, ContentID, Checksum string
	SizeBytes                                                   int
	IsInline                                                    bool
	Data                                                        []byte
}

// Decode before filtering/persisting so retries never leave a header-only message.
func parseIncomingMIME(raw []byte) (*parsedIncoming, error) {
	if len(raw) > maxIncomingBytes {
		return nil, fmt.Errorf("email exceeds 40 MiB")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	for key, values := range msg.Header {
		for index, value := range values {
			msg.Header[key][index] = safeMailText(value)
		}
	}
	result := &parsedIncoming{Header: msg.Header}
	parts := 0
	var walk func(textproto.MIMEHeader, io.Reader, int) error
	walk = func(header textproto.MIMEHeader, body io.Reader, depth int) error {
		parts++
		if parts > maxMIMEParts || depth > maxMIMEDepth {
			return fmt.Errorf("MIME nesting or part limit exceeded")
		}
		mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
		if err != nil {
			mediaType, params = "text/plain", map[string]string{}
		}
		switch strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding"))) {
		case "base64":
			body = base64.NewDecoder(base64.StdEncoding, body)
		case "quoted-printable":
			body = quotedprintable.NewReader(body)
		}
		if strings.HasPrefix(mediaType, "multipart/") {
			if params["boundary"] == "" {
				return fmt.Errorf("missing MIME boundary")
			}
			reader := multipart.NewReader(body, params["boundary"])
			for {
				part, err := reader.NextRawPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				if err = walk(part.Header, part, depth+1); err != nil {
					part.Close()
					return err
				}
				part.Close()
			}
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(body, maxIncomingBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxIncomingBytes {
			return fmt.Errorf("MIME part too large")
		}
		disposition, dp, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
		filename := dp["filename"]
		if filename == "" {
			filename = params["name"]
		}
		isAttachment := disposition == "attachment" || filename != "" || (disposition == "inline" && !strings.HasPrefix(mediaType, "text/"))
		if !isAttachment && (mediaType == "text/plain" || mediaType == "text/html") {
			if cs := params["charset"]; cs != "" {
				reader, err := charset.NewReaderLabel(cs, bytes.NewReader(data))
				if err != nil {
					return fmt.Errorf("unsupported charset: %w", err)
				}
				data, err = io.ReadAll(io.LimitReader(reader, maxIncomingBytes+1))
				if err != nil {
					return err
				}
				if len(data) > maxIncomingBytes {
					return fmt.Errorf("decoded body too large")
				}
			}
			content := safeMailText(string(data))
			if mediaType == "text/plain" {
				result.Text += content
			} else {
				result.HTML += content
			}
			return nil
		}
		if filename == "" {
			filename = "attachment"
		}
		filename = filepath.Base(strings.ReplaceAll(decodeMIMEHeader(filename), "\\", "/"))
		sum := sha256.Sum256(data)
		result.Attachments = append(result.Attachments, AttachmentInfo{Filename: clipUTF8(filename, 255), ContentType: clipUTF8(mediaType, 255), SizeBytes: len(data), ContentID: clipUTF8(strings.Trim(header.Get("Content-ID"), "<>"), 255), IsInline: disposition == "inline", Checksum: hex.EncodeToString(sum[:]), Data: data})
		return nil
	}
	if err := walk(textproto.MIMEHeader(msg.Header), msg.Body, 0); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeMIMEHeader(value string) string {
	decoder := mime.WordDecoder{CharsetReader: charset.NewReaderLabel}
	result, err := decoder.DecodeHeader(value)
	if err != nil {
		return safeMailText(value)
	}
	return safeMailText(result)
}
func clipUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}
func addressList(header mail.Header, key string) []string {
	addresses, err := header.AddressList(key)
	if err != nil {
		return []string{}
	}
	result := make([]string, 0, len(addresses))
	for _, a := range addresses {
		result = append(result, a.Address)
	}
	return result
}

func safeMailText(value string) string {
	// PostgreSQL text cannot store NUL; preserve a visible replacement instead of
	// making one valid-but-unusual message poison the durable receiving retries.
	return strings.ReplaceAll(strings.ToValidUTF8(value, "�"), "\x00", "�")
}

func malformedIncomingFallback(raw []byte, notification *model.SESNotification, parseErr error) *parsedIncoming {
	header := mail.Header{}
	for _, h := range notification.Mail.Headers {
		if strings.EqualFold(h.Name, "From") || strings.EqualFold(h.Name, "To") || strings.EqualFold(h.Name, "Cc") || strings.EqualFold(h.Name, "Subject") || strings.EqualFold(h.Name, "Message-ID") {
			header[textproto.CanonicalMIMEHeaderKey(h.Name)] = []string{safeMailText(h.Value)}
		}
	}
	if header.Get("From") == "" {
		header["From"] = []string{safeMailText(notification.Mail.Source)}
	}
	if header.Get("Subject") == "" {
		header["Subject"] = []string{safeMailText(notification.Mail.CommonHeaders.Subject)}
	}
	if header.Get("To") == "" {
		header["To"] = []string{safeMailText(strings.Join(notification.Mail.CommonHeaders.To, ", "))}
	}
	sum := sha256.Sum256(raw)
	return &parsedIncoming{Header: header, Text: "This message could not be fully decoded. The original message is preserved in the attached original-message.eml file.\n\nParse warning: " + clipUTF8(safeMailText(parseErr.Error()), 256), Attachments: []AttachmentInfo{{Filename: "original-message.eml", ContentType: "message/rfc822", SizeBytes: len(raw), Checksum: hex.EncodeToString(sum[:]), Data: raw}}}
}

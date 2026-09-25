package activity

import (
	"regexp"
	"sort"
	"strings"

	"github.com/uvwt/agentdock/internal/textutil"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`(?i)(?:Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(?:--?|\$env:)?[A-Za-z0-9_]*(?:token|password|passwd|secret|api[_-]?key|authorization)[A-Za-z0-9_-]*[\s"']*(?:=|:|\s)[\s]*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s;,\r\n]+)`),
	regexp.MustCompile(`(?i)(?:--identity-file|--private-key|--key-file|-i)\s+(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s;]+)`),
	regexp.MustCompile(`(?i)https?://[^\s/@:]+:[^\s/@]+@`),
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]+|sk-[A-Za-z0-9_-]{16,})\b`),
}

// Redactor never stores environment maps. Known values are provided only in memory.
type Redactor struct{ values []string }

func NewRedactor(values ...string) Redactor {
	unique := map[string]bool{}
	for _, value := range values {
		if value != "" {
			unique[value] = true
		}
	}
	r := Redactor{}
	for value := range unique {
		r.values = append(r.values, value)
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

func (r Redactor) Text(value string, limit int) string {
	for _, secret := range r.values {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	for _, pattern := range secretPatterns {
		value = pattern.ReplaceAllString(value, "[REDACTED]")
	}
	return textutil.SafeTruncateString(strings.ToValidUTF8(value, "�"), limit).Text
}

func (r Redactor) Event(e Event) Event {
	e.CallMeasurements = e.CallMeasurements.clone()
	e.Request = e.Request.clone(true)
	e.Response = e.Response.clone(true)
	e.OutputSource = e.OutputSource.clone(true)
	for _, payload := range []*Payload{e.Request, e.Response, e.OutputSource} {
		if payload != nil {
			payload.Preview = r.Text(payload.Preview, PayloadPreviewBytes)
			payload.Reason = r.Text(payload.Reason, 512)
		}
	}
	if e.FileEdit != nil {
		bounded := *e.FileEdit
		if len(bounded.AffectedFiles) > MaxRecordedAffectedFiles {
			bounded.AffectedFiles = bounded.AffectedFiles[:MaxRecordedAffectedFiles]
			bounded.FilesTruncated = true
		}
		detail := bounded.clone(true)
		detail.Action = r.Text(detail.Action, 32)
		detail.Path, detail.NewPath = r.Text(detail.Path, 512), r.Text(detail.NewPath, 512)
		if len(detail.AffectedFiles) > MaxRecordedAffectedFiles {
			detail.AffectedFiles = detail.AffectedFiles[:MaxRecordedAffectedFiles]
			detail.FilesTruncated = true
		}
		for index := range detail.AffectedFiles {
			file := &detail.AffectedFiles[index]
			file.Path, file.MoveTo = r.Text(file.Path, 512), r.Text(file.MoveTo, 512)
			file.Operation = r.Text(file.Operation, 32)
		}
		detail.DiffTruncated = detail.DiffTruncated || len(detail.DiffPreview) > 2048
		detail.DiffPreview = r.Text(detail.DiffPreview, 2048)
		e.FileEdit = detail
	}
	e.Label = r.Text(e.Label, 512)
	e.Title = r.Text(e.Title, 512)
	e.ParameterSummary = r.Text(e.ParameterSummary, 4096)
	e.DisplayCommand = r.Text(e.DisplayCommand, 4096)
	e.Workdir = r.Text(e.Workdir, 2048)
	e.LogicalPath = r.Text(e.LogicalPath, 2048)
	e.ResolvedPath = r.Text(e.ResolvedPath, 2048)
	e.StdoutTruncated = e.StdoutTruncated || len(e.OutputPreview) > MaxPreviewBytes
	e.StderrTruncated = e.StderrTruncated || len(e.StderrPreview) > MaxPreviewBytes
	e.OutputPreview = r.Text(e.OutputPreview, MaxPreviewBytes)
	e.StderrPreview = r.Text(e.StderrPreview, MaxPreviewBytes)
	e.Summary = r.Text(e.Summary, 4096)
	return e
}

// LineBuffer prevents credentials split between process writes from reaching disk.
// Very long unterminated lines and private-key blocks are omitted, not partially logged.
type LineBuffer struct {
	pending                string
	discarding, privateKey bool
}

func (b *LineBuffer) Feed(text string, final bool) (string, bool) {
	text = b.pending + text
	b.pending = ""
	var out strings.Builder
	truncated := false
	for text != "" {
		line, rest, complete := strings.Cut(text, "\n")
		if !complete && !final {
			if len(line) > MaxPreviewBytes {
				b.discarding = true
				truncated = true
			} else if !b.discarding {
				b.pending = line
			}
			break
		}
		text = rest
		if strings.Contains(line, "-----BEGIN ") && strings.Contains(line, "PRIVATE KEY-----") {
			b.privateKey = true
		}
		if b.privateKey {
			if strings.Contains(line, "-----END ") && strings.Contains(line, "PRIVATE KEY-----") {
				b.privateKey = false
			}
			truncated = true
		} else if !b.discarding && len(line) <= MaxPreviewBytes && out.Len()+len(line)+1 <= MaxPreviewBytes {
			out.WriteString(line)
			if complete {
				out.WriteByte('\n')
			}
		} else {
			truncated = true
		}
		b.discarding = false
		if !complete {
			break
		}
	}
	return out.String(), truncated
}

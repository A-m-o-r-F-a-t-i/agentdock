package permission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/process"
)

const MaxReviewRequestBytes = 32768
const maxReviewResponseBytes = 8192

// ReviewerConfig is local administrator configuration, never a tool argument.
// The executable is a trusted independent reviewer adapter, not a sandbox.
// No agent tool, ACP session or shell is implicitly started here.
type ReviewerConfig struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args,omitempty"`
	TimeoutMS  int               `json:"timeout_ms"`
	EnvFromEnv map[string]string `json:"env_from_env,omitempty"`
}

type ReviewRequest struct {
	SchemaVersion int      `json:"schema_version"`
	Approval      Approval `json:"approval"`
	FixedRequest  string   `json:"fixed_request"`
	Redacted      bool     `json:"redacted"`
}

type ReviewResult struct {
	ApprovalID string `json:"approval_id"`
	CallID     string `json:"call_id"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
}

func strictJSON(data []byte, value any) error {
	check := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(check, 0); err != nil {
		return err
	}
	if _, err := check.Token(); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(value)
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("JSON nesting limit")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON key")
			}
			seen[name] = true
			if err = uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}

func (s *Store) reviewerConfig() (ReviewerConfig, error) {
	var cfg ReviewerConfig
	name := filepath.Join(s.root, "auto-review.json")
	info, err := os.Lstat(name)
	if err != nil {
		return cfg, errors.New("auto_review is not configured; no operation was approved")
	}
	if !info.Mode().IsRegular() || info.Size() > 16384 {
		return cfg, errors.New("invalid auto_review configuration file")
	}
	data, err := os.ReadFile(name)
	if err != nil || len(data) > 16384 {
		return cfg, errors.New("cannot read bounded auto_review configuration")
	}
	if err = strictJSON(data, &cfg); err != nil {
		return cfg, errors.New("invalid auto_review configuration JSON")
	}
	if !filepath.IsAbs(cfg.Command) || strings.ContainsRune(cfg.Command, 0) || len(cfg.Command) > 4096 || len(cfg.Args) > 32 || cfg.TimeoutMS < 100 || cfg.TimeoutMS > 30000 || len(cfg.EnvFromEnv) > 16 {
		return cfg, errors.New("auto_review requires an absolute executable, at most 32 arguments and a 100-30000ms timeout")
	}
	total := 0
	for _, arg := range cfg.Args {
		total += len(arg)
		if strings.ContainsRune(arg, 0) {
			return cfg, errors.New("invalid reviewer argument")
		}
	}
	if total > 8192 {
		return cfg, errors.New("reviewer arguments exceed limit")
	}
	return cfg, nil
}

func reviewerEnvironment(cfg ReviewerConfig) ([]string, error) {
	// MCP bearer tokens and provider keys are not inherited implicitly.
	env := []string{}
	seen := map[string]bool{}
	for _, name := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR", "LANG"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
			seen[strings.ToUpper(name)] = true
		}
	}
	for child, host := range cfg.EnvFromEnv {
		if !validID.MatchString(child) || !validID.MatchString(host) || seen[strings.ToUpper(child)] {
			return nil, errors.New("invalid or duplicate reviewer environment name")
		}
		value, found := os.LookupEnv(host)
		if !found || strings.ContainsRune(value, 0) {
			return nil, errors.New("required reviewer environment is unavailable")
		}
		env = append(env, child+"="+value)
		seen[strings.ToUpper(child)] = true
	}
	return env, nil
}

type reviewOutput struct {
	bytes.Buffer
	exceeded bool
}

func (w *reviewOutput) Write(data []byte) (int, error) {
	if w.Len()+len(data) > maxReviewResponseBytes {
		w.exceeded = true
		return 0, errors.New("review output limit")
	}
	return w.Buffer.Write(data)
}

// RunReviewer alone grants nothing. The dispatcher must record the verdict,
// revalidate the immutable request and policy, then claim the original CallID.
func (s *Store) RunReviewer(ctx context.Context, request ReviewRequest) (ReviewResult, error) {
	var result ReviewResult
	if len(request.FixedRequest) == 0 || len(request.FixedRequest) > MaxReviewRequestBytes || !request.Redacted || request.Approval.Reviewer != ReviewerAuto {
		return result, errors.New("auto_review requires a complete bounded redacted request")
	}
	cfg, err := s.reviewerConfig()
	if err != nil {
		return result, err
	}
	env, err := reviewerEnvironment(cfg)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer cancel()
	select {
	case s.reviewSlots <- struct{}{}:
		defer func() { <-s.reviewSlots }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir, cmd.Env = s.root, env
	cmd.Stdin = bytes.NewReader(data)
	var output reviewOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	cmd.WaitDelay = 500 * time.Millisecond
	process.Configure(cmd)
	if err = cmd.Start(); err != nil {
		return result, errors.New("independent reviewer could not be started")
	}
	controller, err := process.Attach(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return result, errors.New("reviewer process ownership could not be established")
	}
	defer controller.Close()
	defer controller.Terminate()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = controller.Terminate()
		<-done
		return result, ctx.Err()
	}
	if err != nil || output.exceeded {
		return result, errors.New("independent reviewer failed or exceeded its output limit")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err = strictJSON(output.Bytes(), &result); err != nil {
		return result, errors.New("independent reviewer returned invalid JSON")
	}
	if result.ApprovalID != request.Approval.ID || result.CallID != request.Approval.CallID || (result.Decision != "approve" && result.Decision != "reject") || strings.TrimSpace(result.Reason) == "" || len(result.Reason) > 2048 {
		return ReviewResult{}, errors.New("reviewer response does not match the fixed approval or supported verdict")
	}
	return result, nil
}

func (s *Store) RecordReview(ctx context.Context, id string, result ReviewResult) error {
	return s.locked(ctx, func() error {
		a, err := s.loadApproval(id)
		if err != nil {
			return err
		}
		p, err := s.loadPolicy()
		if err != nil {
			return err
		}
		if a.Status != "pending" || a.PolicyRevision != p.Revision || !time.Now().Before(a.ExpiresAt) {
			return ErrApprovalExpired
		}
		if a.Reviewer != ReviewerAuto || result.ApprovalID != a.ID || result.CallID != a.CallID || (result.Decision != "approve" && result.Decision != "reject") || len(result.Reason) > 2048 || result.Reason == "" {
			return errors.New("invalid automatic review record")
		}
		if a.ReviewDecision != "" {
			return errors.New("approval already reviewed")
		}
		a.ReviewDecision, a.ReviewReason = result.Decision, result.Reason
		now := time.Now().UTC()
		a.ReviewedAt = &now
		path, _ := s.approvalPath(id)
		return writeJSON(ctx, path, a)
	})
}

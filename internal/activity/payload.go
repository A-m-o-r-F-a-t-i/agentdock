package activity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/securepath"
	"github.com/uvwt/agentdock/internal/textutil"
)

const MaxPayloadBytes = 16 << 20
const MaxPayloadStorageBytes = 256 << 20
const PayloadPreviewBytes = 2048
const payloadPublicationGrace = 5 * time.Minute
const payloadPublicationSafety = time.Minute

// Payload is part of the canonical call projection. Its immutable local blob is
// addressed only through an authorized call, never via an arbitrary file path.
// Missing historical fields remain unknown rather than a fabricated empty value.
type Payload struct {
	State     string `json:"state"`
	Format    string `json:"format,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Lines     int    `json:"lines,omitempty"`
	Preview   string `json:"preview,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type PayloadPage struct {
	Payload    *Payload `json:"payload"`
	Text       string   `json:"text"`
	Offset     int64    `json:"offset"`
	NextOffset int64    `json:"next_offset"`
	HasMore    bool     `json:"has_more"`
}

func (payload *Payload) clone(preview bool) *Payload {
	if payload == nil {
		return nil
	}
	copy := *payload
	if !preview {
		copy.Preview = ""
	}
	return &copy
}

// CapturePayload applies the same credential policy before disk, previews and
// copyable text. Storage failures cannot re-run or change the business outcome.
func (s *Store) CapturePayload(ctx context.Context, value any, state string, redactor Redactor) *Payload {
	result := &Payload{State: state, Format: "json"}
	failure := func(reason string) *Payload {
		result.State = "not_stored"
		result.Ref = ""
		result.Reason = reason
		return result
	}
	if err := ctx.Err(); err != nil {
		return failure("保存时已取消；原工具状态保持不变。")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return failure("工具结果不能编码为 JSON，原业务结果未被重执行。")
	}
	if len(data) > MaxPayloadBytes {
		return failure("结果超过单次安全存储上限（16 MiB）；未静默伪装为空输出。")
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber() // Preserve integer result fields beyond float64 precision.
	if err := decoder.Decode(&decoded); err != nil {
		return failure("结果序列化失败。")
	}
	data, err = json.MarshalIndent(redactor.PayloadValue(decoded), "", "  ")
	if err != nil {
		return failure("脱敏结果编码失败。")
	}
	if len(data) > MaxPayloadBytes {
		return failure("格式化结果超过单次安全存储上限（16 MiB）。")
	}
	result.Bytes = int64(len(data))
	result.Lines = strings.Count(string(data), "\n") + 1
	result.Preview = textutil.SafeTruncateString(string(data), PayloadPreviewBytes).Text
	result.Truncated = len(result.Preview) < len(data)
	if s == nil || s.root == "" {
		return failure("当前活动日志没有可持久化的存储目录。")
	}
	if err := ctx.Err(); err != nil {
		return failure("保存时已取消；原工具状态保持不变。")
	}
	path := filepath.Join(s.root, "payloads")
	release, err := s.lockPayload(ctx)
	if err != nil {
		return failure("活动存储锁不可用；输出未保存。")
	}
	defer release()
	if err := os.MkdirAll(path, 0700); err != nil {
		return failure("活动输出目录不可写。")
	}
	if err := rejectPayloadLink(path); err != nil {
		return failure("活动输出目录未通过路径安全检查。")
	}
	info, err := os.Stat(path)
	if err != nil {
		return failure("活动输出目录不可读取。")
	}
	if s.payloadDirectory == nil || !os.SameFile(s.payloadDirectory, info) {
		if err := securepath.EnsurePrivate(path); err != nil {
			return failure("活动输出目录权限准备失败。")
		}
		s.payloadDirectory = info
	}
	hash := sha256.Sum256(data)
	ref := hex.EncodeToString(hash[:])
	target := filepath.Join(path, ref+".json")
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != int64(len(data)) {
			return failure("活动输出引用不是普通文件。")
		}
		// A reused blob may have outlived its original journal event. Renew its
		// publication grace only when less than the safety interval remains; hot
		// repeated calls otherwise avoid an unnecessary metadata write.
		now := time.Now()
		if info.ModTime().Before(now.Add(-(payloadPublicationGrace - payloadPublicationSafety))) {
			if err := os.Chtimes(target, now, now); err != nil {
				return failure("活动输出引用的保留时间无法更新。")
			}
		}
		result.Ref = ref
		return result
	} else if !errors.Is(err, os.ErrNotExist) {
		return failure("活动输出引用不可读取。")
	}
	used, err := s.payloadUsageLocked(ctx, path)
	if err != nil {
		return failure("活动输出配额记录不可读取。")
	}
	if used+int64(len(data)) > MaxPayloadStorageBytes {
		used, err = s.prunePayloadsLocked(ctx, path)
		if err != nil || used+int64(len(data)) > MaxPayloadStorageBytes {
			return failure("活动完整输出已达到 256 MiB 存储配额，摘要仍保留。")
		}
	}
	if err := ctx.Err(); err != nil {
		return failure("保存输出时已取消。")
	}
	// Reserve bytes before writing. A crash can overcount until collection, but
	// concurrent Store instances cannot each spend the full storage allowance.
	if err := s.savePayloadUsageLocked(used + int64(len(data))); err != nil {
		return failure("活动输出配额不能持久化。")
	}
	if err := atomicfile.Write(target, data, 0600); err != nil {
		return failure("完整输出落盘失败，预览不代表完整结果。")
	}
	result.Ref = ref
	return result
}

func rejectPayloadLink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("payload directory is not an ordinary directory")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Windows 8.3 aliases resolve to different strings without crossing a link.
	// Check actual directory entries instead of rejecting spelling changes.
	for parent := filepath.Dir(absolute); ; parent = filepath.Dir(parent) {
		ancestor, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		if !ancestor.IsDir() || ancestor.Mode()&os.ModeSymlink != 0 {
			return errors.New("payload directory has a linked ancestor")
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	return nil
}

func (s *Store) ReadCallPayload(ctx context.Context, callID, kind string, offset int64, limit int) (PayloadPage, error) {
	call, err := s.Call(ctx, callID)
	if err != nil {
		return PayloadPage{}, err
	}
	var payload *Payload
	switch kind {
	case "request":
		payload = call.Request
	case "response":
		payload = call.Response
	default:
		return PayloadPage{}, fmt.Errorf("invalid payload kind")
	}
	if payload == nil {
		return PayloadPage{Payload: &Payload{State: "unknown", Reason: "旧记录未保存" + map[string]string{"request": "调用参数", "response": "输出"}[kind]}}, nil
	}
	page := PayloadPage{Payload: payload.clone(true), Offset: offset, NextOffset: offset}
	if payload.Ref == "" {
		if offset != 0 {
			return page, errors.New("invalid offset for an unavailable payload")
		}
		page.Text = payload.Preview
		return page, nil
	}
	if len(payload.Ref) != 64 {
		return page, errors.New("invalid payload reference")
	}
	if _, err := hex.DecodeString(payload.Ref); err != nil {
		return page, errors.New("invalid payload reference")
	}
	if offset < 0 || offset > payload.Bytes {
		return page, errors.New("invalid payload offset")
	}
	if limit <= 0 {
		limit = 32768
	}
	if limit < 4 {
		limit = 4
	}
	if limit > 262144 {
		limit = 262144
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	root := filepath.Join(s.root, "payloads")
	if err := rejectPayloadLink(root); err != nil {
		return page, err
	}
	path := filepath.Join(root, payload.Ref+".json")
	before, err := os.Lstat(path)
	if err != nil {
		return page, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return page, errors.New("payload is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return page, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return page, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != payload.Bytes {
		return page, errors.New("payload changed after recording")
	}
	buffer := make([]byte, limit)
	n, err := file.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return page, err
	}
	buffer = buffer[:n]
	if n > 0 && !utf8.RuneStart(buffer[0]) {
		return page, errors.New("offset does not point to a UTF-8 boundary")
	}
	for len(buffer) > 0 && !utf8.Valid(buffer) {
		buffer = buffer[:len(buffer)-1]
	}
	if n > 0 && len(buffer) == 0 {
		return page, errors.New("offset does not point to a UTF-8 boundary")
	}
	page.Text = string(buffer)
	page.NextOffset = offset + int64(len(buffer))
	page.HasMore = page.NextOffset < payload.Bytes
	return page, ctx.Err()
}

func sensitivePayloadKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
	switch key {
	case "env", "environment", "authorization", "proxyauthorization", "cookie", "setcookie", "password", "passwd", "secret", "token", "accesstoken", "refreshtoken", "idtoken", "apikey", "clientsecret", "privatekey", "credentials", "credential":
		return true
	}
	return strings.HasSuffix(key, "password") || strings.HasSuffix(key, "accesstoken") || strings.HasSuffix(key, "refreshtoken") || strings.HasSuffix(key, "apikey")
}

func (r Redactor) PayloadValue(value any) any {
	switch input := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(input))
		kind, _ := input["type"].(string)
		_, mime := input["mimeType"]
		for key, value := range input {
			if sensitivePayloadKey(key) {
				result[key] = "[REDACTED]"
				continue
			}
			if (key == "data" && (kind == "image" || kind == "audio" || mime)) || key == "blob" {
				if text, ok := value.(string); ok {
					result[key] = "[BINARY CONTENT: stored as metadata only]"
					result[key+"_encoded_bytes"] = len(text)
					continue
				}
			}
			result[key] = r.PayloadValue(value)
		}
		return result
	case []any:
		result := make([]any, len(input))
		for i, value := range input {
			result[i] = r.PayloadValue(value)
		}
		return result
	case string:
		return r.Text(input, len(input)+1)
	default:
		return value
	}
}

// Called with the dedicated payload lock held. The durable reservation keeps the
// aggregate cap valid across processes, including interrupted blob writes.
func (s *Store) payloadUsageLocked(ctx context.Context, root string) (int64, error) {
	path := filepath.Join(s.root, "payload-usage.json")
	if err := regularPath(path, false); err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s.prunePayloadsLocked(ctx, root)
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil {
		return 0, err
	}
	var usage struct {
		Bytes int64 `json:"bytes"`
	}
	if len(data) > 1024 || json.Unmarshal(data, &usage) != nil || usage.Bytes < 0 {
		return 0, errors.New("invalid payload quota")
	}
	return usage.Bytes, nil
}
func (s *Store) savePayloadUsageLocked(bytes int64) error {
	data, err := json.Marshal(map[string]int64{"bytes": bytes})
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(s.root, "payload-usage.json"), data, 0600)
}
func (s *Store) prunePayloadsLocked(ctx context.Context, root string) (int64, error) {
	// Lock ordering is payload -> journal. Copy the retained references under
	// the journal lock, then release it before scanning or deleting blobs.
	// Captured, unpublished blobs have a five-minute grace period below.
	release, err := s.lock(ctx)
	if err != nil {
		return 0, err
	}
	projection, err := s.projectionLocked(ctx)
	if err != nil {
		release()
		return 0, err
	}
	retained := map[string]bool{}
	for _, call := range projection.calls {
		for _, payload := range []*Payload{call.Request, call.Response} {
			if payload != nil {
				retained[payload.Ref] = true
			}
		}
	}
	release()
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	var used int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return 0, errors.New("invalid payload entry")
		}
		ref := strings.TrimSuffix(entry.Name(), ".json")
		// Capture and event publication are separate durable steps. A new blob
		// must survive that interval even when another process runs collection.
		if !retained[ref] && info.ModTime().Before(time.Now().Add(-payloadPublicationGrace)) {
			if err := os.Remove(filepath.Join(root, entry.Name())); err != nil {
				return 0, err
			}
			continue
		}
		used += info.Size()
	}
	return used, s.savePayloadUsageLocked(used)
}

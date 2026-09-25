package httpx

import (
	"context"
	"errors"
	"net/url"
	"strconv"

	"github.com/uvwt/agentdock/internal/activity"
)

func payloadReadBudget(query url.Values) (offset int64, bytes, chars int, err error) {
	for _, name := range []string{"offset", "limit", "limit_chars"} {
		if values, ok := query[name]; ok && (len(values) != 1 || values[0] == "") {
			return 0, 0, 0, errors.New("payload parameter must occur once with a value: " + name)
		}
	}
	if query.Has("limit") && query.Has("limit_chars") {
		return 0, 0, 0, errors.New("choose limit bytes or limit_chars, not both")
	}
	if query.Has("offset") {
		offset, err = strconv.ParseInt(query.Get("offset"), 10, 64)
	}
	if err != nil || offset < 0 {
		return 0, 0, 0, errors.New("invalid payload byte offset")
	}
	if query.Has("limit_chars") {
		chars, err = strconv.Atoi(query.Get("limit_chars"))
		if err != nil || chars < 1 || chars > 100000 {
			return 0, 0, 0, errors.New("limit_chars must be 1..100000 Unicode scalars")
		}
		return offset, 0, chars, nil
	}
	bytes = 32768
	if query.Has("limit") {
		bytes, err = strconv.Atoi(query.Get("limit"))
	}
	if err != nil || bytes < 4 || bytes > 262144 {
		return 0, 0, 0, errors.New("payload limit must be 4..262144 bytes")
	}
	return offset, bytes, 0, nil
}

// Authentication and the local-management origin check remain in serveExecution.
func readExecutionPayload(ctx context.Context, store *activity.Store, callID, kind string, query url.Values) (activity.PayloadPage, error) {
	offset, bytes, chars, err := payloadReadBudget(query)
	if err != nil {
		return activity.PayloadPage{}, err
	}
	if chars > 0 {
		return store.ReadCallPayloadCharacters(ctx, callID, kind, offset, chars)
	}
	return store.ReadCallPayload(ctx, callID, kind, offset, bytes)
}

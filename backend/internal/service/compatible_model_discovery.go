package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/tidwall/gjson"
)

// fetchCompatibleModels uses no shared cache: every result is obtained using
// this account's current credentials and URL validation. Failed/empty/truncated
// fetches return no partial catalog, so callers retain their last valid snapshot.
func (s *AccountTestService) fetchCompatibleModels(ctx context.Context, account *Account) ([]string, []byte, error) {
	req, err := s.buildOpenAIUpstreamModelsRequest(ctx, account)
	if err != nil {
		return nil, nil, err
	}
	if account.Platform != PlatformOpenRouter {
		body, err := s.readUpstreamModelsResponse(req, account)
		if err != nil {
			return nil, nil, err
		}
		if gjson.GetBytes(body, "has_more").Bool() || (gjson.GetBytes(body, "links.next").Exists() && gjson.GetBytes(body, "links.next").Type != gjson.Null && gjson.GetBytes(body, "links.next").String() != "") {
			return nil, nil, fmt.Errorf("model catalog pagination is unsupported for this provider")
		}
		models, err := extractUpstreamModelIDs(body)
		if err != nil || len(models) == 0 || len(models) > 10000 {
			return nil, nil, fmt.Errorf("model catalog is empty, invalid or exceeds model limit")
		}
		return models, body, nil
	}
	// Official OpenRouter list contract supports offset/limit (max 1000),
	// total_count and links.next. Never request the server-provided next URL.
	fixed := *req.URL
	query := fixed.Query()
	query.Set("offset", "0")
	query.Set("limit", "500")
	fixed.RawQuery = query.Encode()
	offset := 0
	totalBytes := 0
	var entries []json.RawMessage
	seen := make(map[string]bool)
	for page := 0; page < 20; page++ {
		next := *req
		u := fixed
		q := u.Query()
		q.Set("offset", strconv.Itoa(offset))
		u.RawQuery = q.Encode()
		next.URL = &u
		body, err := s.readUpstreamModelsResponse(&next, account)
		if err != nil {
			return nil, nil, err
		}
		totalBytes += len(body)
		if totalBytes > int(upstreamModelsBodyLimit) {
			return nil, nil, fmt.Errorf("model catalog exceeds aggregate size limit")
		}
		data := gjson.GetBytes(body, "data")
		if !gjson.ValidBytes(body) || !data.IsArray() {
			return nil, nil, fmt.Errorf("model catalog page is invalid")
		}
		rows := data.Array()
		if len(rows) == 0 {
			return nil, nil, fmt.Errorf("model catalog page is empty")
		}
		for _, row := range rows {
			id := row.Get("id").String()
			if id == "" || seen[id] {
				return nil, nil, fmt.Errorf("model catalog contains invalid or repeated model IDs")
			}
			seen[id] = true
			entries = append(entries, json.RawMessage(row.Raw))
		}
		if len(entries) > 10000 {
			return nil, nil, fmt.Errorf("model catalog exceeds model limit")
		}
		more := gjson.GetBytes(body, "links.next")
		total := gjson.GetBytes(body, "total_count")
		if !more.Exists() || more.Type == gjson.Null || more.String() == "" {
			if total.Exists() && (total.Type != gjson.Number || total.Int() != int64(len(entries))) {
				return nil, nil, fmt.Errorf("model catalog is truncated")
			}
			merged, err := json.Marshal(struct {
				Data []json.RawMessage `json:"data"`
			}{entries})
			if err != nil {
				return nil, nil, err
			}
			models, err := extractUpstreamModelIDs(merged)
			return models, merged, err
		}
		parsed, err := url.Parse(more.String())
		if err != nil {
			return nil, nil, fmt.Errorf("model catalog next link is invalid")
		}
		resolved := u.ResolveReference(parsed)
		if resolved.Scheme != fixed.Scheme || resolved.Host != fixed.Host || resolved.Path != fixed.Path || resolved.User != nil || resolved.Fragment != "" {
			return nil, nil, fmt.Errorf("model catalog next link changed origin or endpoint")
		}
		nextQuery := resolved.Query()
		for key := range nextQuery {
			if key != "offset" && key != "limit" {
				return nil, nil, fmt.Errorf("model catalog next link has unsupported pagination fields")
			}
		}
		nextOffset, err := strconv.Atoi(nextQuery.Get("offset"))
		if err != nil || nextOffset != offset+len(rows) {
			return nil, nil, fmt.Errorf("model catalog next offset is invalid or repeated")
		}
		if nextQuery.Get("limit") != "500" {
			return nil, nil, fmt.Errorf("model catalog next limit changed")
		}
		offset = nextOffset
	}
	return nil, nil, fmt.Errorf("model catalog pagination exceeds page limit")
}

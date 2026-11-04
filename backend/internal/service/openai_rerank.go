package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const MaxRerankBodyBytes = 1 << 20

// ValidateRerankRequest supports only the standard non-streaming string-document
// subset. Provider-specific document objects, search units and legacy auth are
// deliberately excluded.
func ValidateRerankRequest(body []byte) error {
	if len(body) == 0 || len(body) > MaxRerankBodyBytes {
		return errors.New("rerank body must be between 1 byte and 1 MiB")
	}
	var req struct {
		Model           string   `json:"model"`
		Query           string   `json:"query"`
		Documents       []string `json:"documents"`
		TopN            *int     `json:"top_n"`
		ReturnDocuments bool     `json:"return_documents"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return errors.New("rerank requires model, query, string documents and optional top_n/return_documents")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("rerank requires one JSON object")
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Query) == "" || len(req.Query) > 32768 {
		return errors.New("rerank model and query are required; query maximum is 32 KiB")
	}
	if len(req.Documents) == 0 || len(req.Documents) > 1000 {
		return errors.New("rerank requires 1 to 1000 documents")
	}
	for _, d := range req.Documents {
		if strings.TrimSpace(d) == "" || len(d) > 65536 {
			return errors.New("rerank documents must be nonempty strings up to 64 KiB")
		}
	}
	if req.TopN != nil && (*req.TopN < 1 || *req.TopN > len(req.Documents)) {
		return errors.New("rerank top_n must be between 1 and document count")
	}
	return nil
}

package service

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var bedrockRegionSyntax = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)

// Cloud credential-file mode is intentionally stricter than legacy accounts.
// Operators pin exact native models/profiles; this does not discover availability
// or authorize global routing. Model allowlisting is enforced after resolution.
func validateBedrockCloudRequest(account *Account, region, model string, body []byte) ([]byte, error) {
	if account == nil || account.GetCredential("aws_auth_mode") != "credential_file" {
		return body, nil
	}
	if !bedrockRegionSyntax.MatchString(region) || account.GetCredential("aws_region") != region {
		return nil, errors.New("bedrock application credentials require an explicit valid region")
	}
	var allowed []string
	raw := account.GetCredential("aws_allowed_model_ids")
	if err := json.Unmarshal([]byte(raw), &allowed); err != nil || len(allowed) == 0 {
		return nil, errors.New("bedrock application credentials require explicit model allowlist")
	}
	found := false
	for _, id := range allowed {
		if id == model {
			found = true
		}
	}
	if !found {
		return nil, errors.New("resolved bedrock model is not allowed")
	}
	if strings.HasPrefix(model, "global.") && account.GetCredential("aws_allow_global") != "true" {
		return nil, errors.New("global bedrock routing requires explicit consent")
	}
	tokens := gjson.GetBytes(body, "max_tokens")
	if !tokens.Exists() {
		return sjson.SetBytes(body, "max_tokens", 1024)
	}
	var value int64
	if err := json.Unmarshal([]byte(tokens.Raw), &value); err != nil || value < 1 || value > 8192 {
		return nil, errors.New("bedrock application max_tokens must be an integer between 1 and 8192")
	}
	return body, nil
}

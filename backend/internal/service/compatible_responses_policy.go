package service

import (
	"errors"

	"github.com/tidwall/gjson"
)

// validateCompatibleResponsesBody is applied before transformations and again
// to the actual outgoing Responses body. It never silently removes state.
func validateCompatibleResponsesBody(account *Account, body []byte) error {
	if account == nil {
		return nil
	}
	profile := account.providerProfile()
	if profile == nil || !profile.StatelessResponses {
		return nil
	}
	if !gjson.ValidBytes(body) {
		return errors.New("compatible Responses body must be JSON")
	}
	for _, key := range []string{"previous_response_id", "conversation"} {
		if value := gjson.GetBytes(body, key); value.Exists() && value.Type != gjson.Null {
			return errors.New("compatible Responses supports stateless requests only; " + key + " is unsupported")
		}
	}
	for _, key := range []string{"store", "background"} {
		if value := gjson.GetBytes(body, key); value.Exists() && value.Type != gjson.Null && (value.Type != gjson.False) {
			return errors.New("compatible Responses requires " + key + "=false")
		}
	}
	return nil
}

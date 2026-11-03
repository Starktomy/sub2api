package service

import (
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"testing"
)

func TestBedrockCloudRequestPolicy(t *testing.T) {
	account := &Account{Credentials: map[string]any{"aws_auth_mode": "credential_file", "aws_region": "us-west-2", "aws_allowed_model_ids": `["anthropic.test","global.anthropic.test"]`}}
	out, err := validateBedrockCloudRequest(account, "us-west-2", "anthropic.test", []byte(`{"messages":[]}`))
	require.NoError(t, err)
	require.Equal(t, int64(1024), gjson.GetBytes(out, "max_tokens").Int())
	for _, body := range []string{`{"max_tokens":0}`, `{"max_tokens":8193}`, `{"max_tokens":1.5}`, `{"max_tokens":"12"}`, `{"max_tokens":null}`} {
		_, err = validateBedrockCloudRequest(account, "us-west-2", "anthropic.test", []byte(body))
		require.Error(t, err)
	}
	_, err = validateBedrockCloudRequest(account, "us-east-1", "anthropic.test", []byte(`{}`))
	require.Error(t, err)
	_, err = validateBedrockCloudRequest(account, "us-west-2", "unapproved", []byte(`{}`))
	require.Error(t, err)
	_, err = validateBedrockCloudRequest(account, "us-west-2", "global.anthropic.test", []byte(`{}`))
	require.Error(t, err)
	account.Credentials["aws_allow_global"] = "true"
	_, err = validateBedrockCloudRequest(account, "us-west-2", "global.anthropic.test", []byte(`{"max_tokens":32}`))
	require.NoError(t, err)
}
func TestBedrockCloudPreservesExplicitProfile(t *testing.T) {
	account := &Account{Credentials: map[string]any{"aws_auth_mode": "credential_file", "aws_region": "eu-west-1"}}
	model, ok := ResolveBedrockModelID(account, "us.anthropic.example")
	require.True(t, ok)
	require.Equal(t, "us.anthropic.example", model)
}

func TestBedrockCloudPolicyLegacyUnchanged(t *testing.T) {
	body := []byte(`{"max_tokens":81920}`)
	got, err := validateBedrockCloudRequest(&Account{}, "us-east-1", "legacy", body)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

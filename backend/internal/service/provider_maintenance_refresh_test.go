//go:build unit

package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/refreshpolicy"
)

// These tests exercise actual services with fake client seams. They establish
// service call counts, not HTTP transport single-send or HA safety.
func TestGeminiMaintenanceRefreshSingleAttempt(t *testing.T) {
	for _, failure := range []error{errors.New("upstream 503"), errors.New("unauthorized_client"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			for _, oauthType := range []string{"code_assist", "google_one"} {
				calls := 0
				cfg := &config.Config{}
				cfg.Gemini.OAuth.ClientID = "fake-client"
				cfg.Gemini.OAuth.ClientSecret = "fake-secret"
				svc := &GeminiOAuthService{cfg: cfg, oauthClient: &mockGeminiOAuthClient{
					refreshTokenFunc: func(ctx context.Context, gotType, token, proxy string) (*geminicli.TokenResponse, error) {
						calls++
						if !isMaintenanceRefresh(ctx) || gotType != oauthType || token != "old-refresh" || proxy != "" {
							t.Fatal("unexpected refresh invocation")
						}
						return nil, failure
					},
				}}
				_, err := svc.RefreshAccountToken(refreshpolicy.WithSingleAttempt(context.Background()), &Account{
					Platform: PlatformGemini, Type: AccountTypeOAuth,
					Credentials: map[string]any{"refresh_token": "old-refresh", "oauth_type": oauthType},
				})
				if !errors.Is(err, failure) || calls != 1 {
					t.Fatalf("type=%s calls=%d error=%v", oauthType, calls, err)
				}
			}
		})
	}
}

func TestGeminiMaintenanceRefreshPreservesLocalMetadata(t *testing.T) {
	for _, oauthType := range []string{"code_assist", "google_one", "ai_studio"} {
		for _, project := range []string{"", "stored-project"} {
			t.Run(oauthType+"/"+project, func(t *testing.T) {
				calls := 0
				svc := &GeminiOAuthService{
					oauthClient: &mockGeminiOAuthClient{refreshTokenFunc: func(ctx context.Context, gotType, token, proxy string) (*geminicli.TokenResponse, error) {
						calls++
						if gotType != oauthType || token != "old-refresh" || proxy != "" {
							t.Fatal("unexpected refresh invocation")
						}
						return &geminicli.TokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600}, nil
					}},
					// Missing callbacks panic on any unexpected discovery/onboarding.
					codeAssist: &mockGeminiCodeAssistClient{},
					driveClient: &mockDriveClient{getStorageQuotaFunc: func(context.Context, string, string) (*geminicli.DriveStorageInfo, error) {
						t.Fatal("unexpected Drive enrichment")
						return nil, nil
					}},
				}
				account := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{
					"refresh_token": "old-refresh", "oauth_type": oauthType, "project_id": project,
					"tier_id": "stored-tier", "drive_tier_updated_at": "2000-01-01T00:00:00Z", "drive_storage_limit": "stored-limit",
				}}
				got, err := NewGeminiTokenRefresher(svc).Refresh(refreshpolicy.WithSingleAttempt(context.Background()), account)
				if err != nil || calls != 1 || got["access_token"] != "new-access" || got["refresh_token"] != "new-refresh" {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
				for _, key := range []string{"oauth_type", "project_id", "tier_id", "drive_tier_updated_at", "drive_storage_limit"} {
					if !reflect.DeepEqual(got[key], account.Credentials[key]) {
						t.Fatalf("local metadata changed: %s", key)
					}
				}
			})
		}
	}
}

func TestGeminiLegacyRefreshStillRetriesAndFallsBack(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gemini.OAuth.ClientID = "fake-client"
	cfg.Gemini.OAuth.ClientSecret = "fake-secret"
	for _, oauthType := range []string{"code_assist", "google_one"} {
		t.Run(oauthType, func(t *testing.T) {
			var calls []string
			svc := &GeminiOAuthService{cfg: cfg, oauthClient: &mockGeminiOAuthClient{
				refreshTokenFunc: func(ctx context.Context, gotType, token, proxy string) (*geminicli.TokenResponse, error) {
					calls = append(calls, gotType)
					if isMaintenanceRefresh(ctx) || token != "old-refresh" || proxy != "" {
						t.Fatal("unexpected invocation")
					}
					switch len(calls) {
					case 1:
						return nil, errors.New("upstream 503")
					case 2:
						return nil, errors.New("unauthorized_client")
					case 3:
						return &geminicli.TokenResponse{AccessToken: "new-access", ExpiresIn: 3600}, nil
					default:
						t.Fatal("unexpected retry")
						return nil, nil
					}
				},
			}}
			account := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{
				"refresh_token": "old-refresh", "oauth_type": oauthType, "project_id": "stored-project", "tier_id": GeminiTierGoogleAIPro,
			}, Extra: map[string]any{"drive_tier_updated_at": "2999-01-01T00:00:00Z"}}
			got, err := svc.RefreshAccountToken(context.Background(), account)
			alt := "ai_studio"
			if oauthType == "google_one" {
				alt = "code_assist"
			}
			if err != nil || got.AccessToken != "new-access" || !reflect.DeepEqual(calls, []string{oauthType, oauthType, alt}) {
				t.Fatalf("calls=%v error=%v", calls, err)
			}
		})
	}
}

type maintenanceAntigravityClient struct {
	t       *testing.T
	refresh func(context.Context, string) (*antigravity.TokenResponse, error)
	load    func(context.Context, string) (*antigravity.LoadCodeAssistResponse, map[string]any, error)
}

func (c *maintenanceAntigravityClient) RefreshToken(ctx context.Context, token string) (*antigravity.TokenResponse, error) {
	return c.refresh(ctx, token)
}
func (c *maintenanceAntigravityClient) LoadCodeAssist(ctx context.Context, token string) (*antigravity.LoadCodeAssistResponse, map[string]any, error) {
	if c.load != nil {
		return c.load(ctx, token)
	}
	c.t.Fatal("unexpected LoadCodeAssist")
	return nil, nil, nil
}
func (c *maintenanceAntigravityClient) OnboardUser(context.Context, string, string) (string, error) {
	c.t.Fatal("unexpected OnboardUser")
	return "", nil
}

func TestAntigravityMaintenanceRefreshSingleAttempt(t *testing.T) {
	for _, failure := range []error{errors.New("upstream 503"), errors.New("unauthorized_client"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			calls := 0
			svc := &AntigravityOAuthService{refreshClientFactory: func(proxy string) (antigravityRefreshClient, error) {
				if proxy != "" {
					t.Fatal("unexpected proxy")
				}
				return &maintenanceAntigravityClient{t: t, refresh: func(ctx context.Context, token string) (*antigravity.TokenResponse, error) {
					calls++
					if !isMaintenanceRefresh(ctx) || token != "old-refresh" {
						t.Fatal("unexpected refresh invocation")
					}
					return nil, failure
				}}, nil
			}}
			_, err := svc.RefreshToken(refreshpolicy.WithSingleAttempt(context.Background()), "old-refresh", "")
			if !errors.Is(err, failure) || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestAntigravityMaintenanceRefreshPreservesLocalMetadata(t *testing.T) {
	for _, project := range []string{"", "stored-project"} {
		t.Run(project, func(t *testing.T) {
			calls := 0
			svc := &AntigravityOAuthService{refreshClientFactory: func(proxy string) (antigravityRefreshClient, error) {
				if proxy != "" {
					t.Fatal("unexpected proxy")
				}
				return &maintenanceAntigravityClient{t: t, refresh: func(ctx context.Context, token string) (*antigravity.TokenResponse, error) {
					calls++
					if token != "old-refresh" {
						t.Fatal("unexpected token")
					}
					return &antigravity.TokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600}, nil
				}}, nil
			}}
			account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{
				"refresh_token": "old-refresh", "project_id": project, "email": "local@example.invalid", "plan_type": "stored-plan",
			}}
			got, err := NewAntigravityTokenRefresher(svc).Refresh(refreshpolicy.WithSingleAttempt(context.Background()), account)
			if err != nil || calls != 1 || got["access_token"] != "new-access" || got["refresh_token"] != "new-refresh" {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			for _, key := range []string{"project_id", "email", "plan_type"} {
				if !reflect.DeepEqual(got[key], account.Credentials[key]) {
					t.Fatalf("local metadata changed: %s", key)
				}
			}
		})
	}
}

func TestAntigravityLegacyRefreshStillRetriesAndEnriches(t *testing.T) {
	calls, loads := 0, 0
	svc := &AntigravityOAuthService{refreshClientFactory: func(proxy string) (antigravityRefreshClient, error) {
		if proxy != "" {
			t.Fatal("unexpected proxy")
		}
		return &maintenanceAntigravityClient{t: t,
			refresh: func(ctx context.Context, token string) (*antigravity.TokenResponse, error) {
				calls++
				if isMaintenanceRefresh(ctx) || token != "old-refresh" {
					t.Fatal("unexpected invocation")
				}
				if calls == 1 {
					return nil, errors.New("upstream 503")
				}
				if calls != 2 {
					t.Fatal("unexpected retry")
				}
				return &antigravity.TokenResponse{AccessToken: "new-access", ExpiresIn: 3600}, nil
			},
			load: func(ctx context.Context, token string) (*antigravity.LoadCodeAssistResponse, map[string]any, error) {
				loads++
				if token != "new-access" {
					t.Fatal("unexpected enrichment token")
				}
				return &antigravity.LoadCodeAssistResponse{CloudAICompanionProject: "detected-project"}, nil, nil
			},
		}, nil
	}}
	got, err := svc.RefreshAccountToken(context.Background(), &Account{
		Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "old-refresh"},
	})
	if err != nil || calls != 2 || loads != 1 || got.ProjectID != "detected-project" {
		t.Fatalf("refresh calls=%d load calls=%d error=%v", calls, loads, err)
	}
}

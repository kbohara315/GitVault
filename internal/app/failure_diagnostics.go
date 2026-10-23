package app

import (
	"errors"

	"gitvault/internal/backup"
	"gitvault/internal/github"
	"gitvault/internal/secrets"
)

var (
	errGitHubAccountMismatch     = errors.New("GitHub account changed")
	errGitHubInventorySave       = errors.New("GitHub inventory persistence failed")
	errGitHubRepositoryMissing   = errors.New("selected GitHub repository unavailable")
	errGitHubInventoryIncomplete = errors.New("selected GitHub inventory incomplete")
)

// Only these fixed categories may cross the durable diagnostics boundary.
// Never infer a category from provider text, Git stderr, paths or URLs.
var failureCategories = []struct {
	cause             error
	code, description string
}{
	{secrets.ErrDenied, "engine_failed_credential_denied", "OS secure storage denied access; unlock your keyring and try again"},
	{secrets.ErrNotFound, "engine_failed_credential_missing", "saved token was not found in OS secure storage; save a token in Repositories"},
	{secrets.ErrRevoked, "engine_failed_credential_revoked", "saved credential generation was revoked; save a token in Repositories"},
	{secrets.ErrBusy, "engine_failed_credential_pending", "an OS credential prompt is still pending; finish it before retrying"},
	{secrets.ErrUnavailable, "engine_failed_credential_unavailable", "OS secure storage is unavailable; check your keyring"},
	{ErrCredentialUnavailable, "engine_failed_credential_unavailable", "OS secure storage is unavailable; check your keyring"},
	{backup.ErrCredentialDenied, "engine_failed_credential_broker", "controlled Git credential handoff was denied"},
	{errGitHubAccountMismatch, "engine_failed_github_account_changed", "authenticated account differs from the saved account; rediscover repositories"},
	{errGitHubInventorySave, "engine_failed_github_inventory_save", "fresh repository inventory could not be saved locally"},
	{errGitHubRepositoryMissing, "engine_failed_github_repository_missing", "selected repository is no longer accessible; rediscover and check token access"},
	{errGitHubInventoryIncomplete, "engine_failed_github_inventory_incomplete", "discovery was incomplete; rediscover repositories before retrying"},
}

var githubFailureDescriptions = map[github.Kind]string{
	github.Authentication:  "GitHub rejected authentication; replace the token in Repositories",
	github.Permission:      "GitHub denied access; check token permissions and organization approval",
	github.NotFound:        "GitHub resource was not found or is not accessible to this token",
	github.RateLimit:       "GitHub rate limit reached; wait before retrying",
	github.Transport:       "GitHub discovery connection failed; check network connectivity",
	github.InvalidResponse: "GitHub returned an invalid discovery response",
	github.Bounds:          "GitHub discovery exceeded a configured safety limit",
}

func classifiedFailureCode(err error) string {
	for _, category := range failureCategories {
		if errors.Is(err, category.cause) {
			return category.code
		}
	}
	var apiErr *github.Error
	if errors.As(err, &apiErr) {
		if _, known := githubFailureDescriptions[apiErr.Kind]; known {
			return "engine_failed_github_" + string(apiErr.Kind)
		}
	}
	return ""
}

func classifiedFailureDescription(code string) string {
	for _, category := range failureCategories {
		if code == category.code {
			return category.description
		}
	}
	for kind, description := range githubFailureDescriptions {
		if code == "engine_failed_github_"+string(kind) {
			return description
		}
	}
	return ""
}

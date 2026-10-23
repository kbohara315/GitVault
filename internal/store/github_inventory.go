package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHubAccount is the persisted identity and latest inventory attempt state.
type GitHubAccount struct {
	ID, Login, Type string
	Complete        bool
	AttemptedAt     time.Time
}

// GitHubRepository contains display metadata only. Repository IDs are the
// provider's stable numeric IDs; names and clone URLs may change over time.
type GitHubRepository struct {
	ID, OwnerID, OwnerLogin, OwnerType string
	Name, FullName, CloneURL           string
	Description                        string
	Private, Fork, Archived            bool
	Available                          bool
	LastSeenAt                         time.Time
}

// ReconcileGitHubInventory records one validated discovery result atomically.
// A complete pass marks absent records inaccessible but never deletes them.
// A partial pass updates only rows observed in its successful pages.
func (s *Store) ReconcileGitHubInventory(ctx context.Context, account GitHubAccount, repositories []GitHubRepository, complete bool) error {
	if err := validateGitHubInventory(account, repositories); err != nil {
		return err
	}
	now := githubInventoryStamp(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO github_accounts(provider,account_id,login,account_type,inventory_complete,attempted_at) VALUES('github',?,?,?,?,?) ON CONFLICT(provider,account_id) DO UPDATE SET login=excluded.login,account_type=excluded.account_type,inventory_complete=excluded.inventory_complete,attempted_at=excluded.attempted_at`, account.ID, account.Login, account.Type, complete, now); err != nil {
		return err
	}
	if complete {
		if _, err = tx.ExecContext(ctx, `UPDATE github_repositories SET availability='inaccessible' WHERE provider='github' AND account_id=?`, account.ID); err != nil {
			return err
		}
	}
	for _, repository := range repositories {
		_, err = tx.ExecContext(ctx, `INSERT INTO github_repositories(provider,account_id,repository_id,owner_id,owner_login,owner_type,name,full_name,clone_url,private,fork,archived,description,availability,last_seen_at) VALUES('github',?,?,?,?,?,?,?,?,?,?,?,?,'available',?) ON CONFLICT(provider,account_id,repository_id) DO UPDATE SET owner_id=excluded.owner_id,owner_login=excluded.owner_login,owner_type=excluded.owner_type,name=excluded.name,full_name=excluded.full_name,clone_url=excluded.clone_url,private=excluded.private,fork=excluded.fork,archived=excluded.archived,description=excluded.description,availability='available',last_seen_at=excluded.last_seen_at`, account.ID, repository.ID, repository.OwnerID, repository.OwnerLogin, repository.OwnerType, repository.Name, repository.FullName, repository.CloneURL, repository.Private, repository.Fork, repository.Archived, repository.Description, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LatestGitHubInventory returns the most recently attempted account and its
// retained repository rows, including records no longer accessible.
func (s *Store) LatestGitHubInventory(ctx context.Context) (GitHubAccount, []GitHubRepository, error) {
	var account GitHubAccount
	var complete int
	var attempted string
	err := s.db.QueryRowContext(ctx, `SELECT account_id,login,account_type,inventory_complete,attempted_at FROM github_accounts WHERE provider='github' ORDER BY attempted_at DESC,account_id LIMIT 1`).Scan(&account.ID, &account.Login, &account.Type, &complete, &attempted)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubAccount{}, nil, ErrNotFound
	}
	if err != nil {
		return GitHubAccount{}, nil, err
	}
	account.Complete = complete == 1
	account.AttemptedAt, err = parseStamp(attempted)
	if err != nil {
		return GitHubAccount{}, nil, err
	}
	repositories, err := listGitHubRepositories(ctx, s.db, account.ID)
	return account, repositories, err
}

// GitHubInventory returns retained rows for one stable account ID.
func (s *Store) GitHubInventory(ctx context.Context, accountID string) (GitHubAccount, []GitHubRepository, error) {
	var account GitHubAccount
	var complete int
	var attempted string
	err := s.db.QueryRowContext(ctx, `SELECT account_id,login,account_type,inventory_complete,attempted_at FROM github_accounts WHERE provider='github' AND account_id=?`, accountID).Scan(&account.ID, &account.Login, &account.Type, &complete, &attempted)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubAccount{}, nil, ErrNotFound
	}
	if err != nil {
		return GitHubAccount{}, nil, err
	}
	account.Complete = complete == 1
	account.AttemptedAt, err = parseStamp(attempted)
	if err != nil {
		return GitHubAccount{}, nil, err
	}
	repositories, err := listGitHubRepositories(ctx, s.db, account.ID)
	return account, repositories, err
}

// HasGitHubRepository checks a provider-qualified stable selection ID against
// retained inventory. Inaccessible historical rows remain known identities.
func (s *Store) HasGitHubRepository(ctx context.Context, selectionID string) (bool, error) {
	if !strings.HasPrefix(selectionID, "github:") || !validNumericID(strings.TrimPrefix(selectionID, "github:")) {
		return false, nil
	}
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM github_repositories WHERE provider='github' AND repository_id=? LIMIT 1`, strings.TrimPrefix(selectionID, "github:")).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return exists == 1, err
}

type inventoryQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listGitHubRepositories(ctx context.Context, q inventoryQuery, accountID string) ([]GitHubRepository, error) {
	rows, err := q.QueryContext(ctx, `SELECT repository_id,owner_id,owner_login,owner_type,name,full_name,clone_url,private,fork,archived,description,availability,last_seen_at FROM github_repositories WHERE provider='github' AND account_id=? ORDER BY full_name COLLATE NOCASE,repository_id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repositories := []GitHubRepository{}
	for rows.Next() {
		var repository GitHubRepository
		var private, fork, archived int
		var availability, lastSeen string
		if err = rows.Scan(&repository.ID, &repository.OwnerID, &repository.OwnerLogin, &repository.OwnerType, &repository.Name, &repository.FullName, &repository.CloneURL, &private, &fork, &archived, &repository.Description, &availability, &lastSeen); err != nil {
			return nil, err
		}
		repository.Private = private == 1
		repository.Fork = fork == 1
		repository.Archived = archived == 1
		repository.Available = availability == "available"
		repository.LastSeenAt, err = parseStamp(lastSeen)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func validateGitHubInventory(account GitHubAccount, repositories []GitHubRepository) error {
	if !validNumericID(account.ID) || !validLogin(account.Login) || (account.Type != "User" && account.Type != "Organization") || len(repositories) > 100000 {
		return errors.New("invalid GitHub inventory identity or size")
	}
	seen := make(map[string]bool, len(repositories))
	for _, repository := range repositories {
		if !validNumericID(repository.ID) || !validNumericID(repository.OwnerID) || !validLogin(repository.OwnerLogin) || (repository.OwnerType != "User" && repository.OwnerType != "Organization") || !validRepositoryName(repository.Name) || repository.FullName != repository.OwnerLogin+"/"+repository.Name || len(repository.Description) > 4096 || !cleanInventoryText(repository.Description) || seen[repository.ID] {
			return errors.New("invalid GitHub repository inventory row")
		}
		clone, err := url.Parse(repository.CloneURL)
		if err != nil || clone.Scheme != "https" || clone.Host != "github.com" || clone.User != nil || clone.RawQuery != "" || clone.ForceQuery || clone.Fragment != "" || clone.RawPath != "" || clone.Path != "/"+repository.FullName+".git" {
			return errors.New("invalid GitHub repository identity URL")
		}
		seen[repository.ID] = true
	}
	return nil
}

func githubInventoryStamp(value time.Time) string {
	// Fixed-width UTC precision keeps SQLite's text ordering equivalent to time
	// ordering when choosing the last account attempted across discovery runs.
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func validNumericID(value string) bool {
	if len(value) == 0 || len(value) > 32 || strings.TrimSpace(value) != value {
		return false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == value
}

func validLogin(value string) bool {
	if len(value) == 0 || len(value) > 256 || !isASCIILetterOrDigit(value[0]) {
		return false
	}
	for i := 1; i < len(value); i++ {
		if !isASCIILetterOrDigit(value[i]) && value[i] != '-' && value[i] != '_' {
			return false
		}
	}
	return true
}

func validRepositoryName(value string) bool {
	if len(value) == 0 || len(value) > 256 || value == "." || value == ".." {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !isASCIILetterOrDigit(c) && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

func isASCIILetterOrDigit(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func cleanInventoryText(value string) bool {
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

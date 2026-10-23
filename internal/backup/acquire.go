package backup

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type githubRemote struct {
	host       string
	repository string
	url        string
	urlPath    string
}

func validatedGitHubRemote(raw string) (githubRemote, error) {
	var remote githubRemote
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return remote, ErrGitTransportDenied
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 || strings.Contains(u.Path, "%") || !validGitHubPathComponent(parts[0], true) || !strings.HasSuffix(parts[1], ".git") || !validGitHubPathComponent(strings.TrimSuffix(parts[1], ".git"), false) {
		return remote, ErrGitTransportDenied
	}
	if u.EscapedPath() != u.Path || strings.ContainsAny(raw, "\x00\r\n") {
		return remote, ErrGitTransportDenied
	}
	remote = githubRemote{host: "github.com", repository: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), url: "https://github.com" + u.Path, urlPath: u.Path}
	return remote, nil
}

func validGitHubPathComponent(value string, owner bool) bool {
	if len(value) == 0 || len(value) > 256 || value == "." || value == ".." || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		letterOrDigit := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !letterOrDigit && c != '-' && !(owner && c == '_') && !(!owner && (c == '_' || c == '.')) {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (e *Engine) AcquireGitHub(ctx context.Context, repositoryID, cloneURL string, secret []byte) (string, error) {
	if ctx == nil || ctx.Err() != nil || !canonicalGitHubRepositoryID(repositoryID) || !credentialBytes(secret, credentialSecretLimit) {
		return "", ErrGitTransportDenied
	}
	remote, err := validatedGitHubRemote(cloneURL)
	if err != nil {
		return "", ErrGitTransportDenied
	}
	host := remote.host
	if e.runner.testOrigin != "" {
		testOrigin, parseErr := url.Parse(e.runner.testOrigin)
		if parseErr != nil || !isLoopbackHost(testOrigin.Hostname()) {
			return "", ErrGitTransportDenied
		}
		host = testOrigin.Host
	}
	executable := e.runner.helperExecutable
	options := CredentialBrokerOptions{Context: ctx, PrivateDir: e.runner.scratchRoot, AllowedHost: host, AllowedRepository: strings.ToLower(remote.repository), Username: "x-access-token", Secret: secret, Executable: executable, HelperPrefix: append([]string(nil), e.runner.testHelperPrefix...)}
	var broker *CredentialBroker
	if e.runner.testOrigin == "" {
		broker, err = NewCredentialBroker(options)
	} else {
		broker, err = NewCredentialBrokerForTesting(options)
	}
	if err != nil {
		return "", err
	}
	defer broker.Close()

	if err = e.validateRootIdentity(); err != nil {
		return "", err
	}
	if err = e.authorizeManagedMutation(); err != nil {
		return "", err
	}
	if err = e.ensureManagedDirectories("github"); err != nil {
		return "", err
	}
	mirrorRoot := filepath.Join(e.root, "mirrors", "github")
	unlock, err := lockGitHubMirror(ctx, mirrorRoot, repositoryID)
	if err != nil {
		return "", err
	}
	defer unlock()
	if err = e.validateRootIdentity(); err != nil {
		return "", err
	}
	if err = e.authorizeManagedMutation(); err != nil {
		return "", err
	}
	destination := filepath.Join(mirrorRoot, repositoryID+".git")
	if info, statErr := os.Lstat(destination); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrUnsafeRepository
		}
		if err = e.runner.validateGitHubMirror(ctx, destination); err != nil {
			return "", fmt.Errorf("validate existing GitHub mirror: %w", err)
		}
		if err = e.runner.configureGitHubOrigin(ctx, destination, requestGitHubURL(e.runner, remote)); err != nil {
			return "", fmt.Errorf("validate GitHub mirror origin: %w", err)
		}
		if err = e.runner.fetchGitHub(ctx, destination, cloneURL, broker); err != nil {
			return "", err
		}
		if err = e.runner.validateGitHubMirror(ctx, destination); err != nil {
			return "", fmt.Errorf("validate fetched GitHub mirror: %w", err)
		}
		return destination, nil
	} else if !os.IsNotExist(statErr) {
		return "", ErrUnsafeRepository
	}

	id, err := NewID()
	if err != nil {
		return "", err
	}
	staging := filepath.Join(mirrorRoot, ".gitvault-"+repositoryID+"-"+id+".partial.git")
	defer func() { _ = e.fs.removeAll(staging) }()
	if err = e.runner.cloneGitHub(ctx, cloneURL, staging, broker); err != nil {
		return "", err
	}
	if err = e.runner.validateGitHubMirror(ctx, staging); err != nil {
		return "", fmt.Errorf("validate cloned GitHub mirror: %w", err)
	}
	if err = e.runner.configureGitHubOrigin(ctx, staging, requestGitHubURL(e.runner, remote)); err != nil {
		return "", fmt.Errorf("validate cloned GitHub mirror origin: %w", err)
	}
	if err = e.fs.publish(staging, destination); err != nil {
		return "", fmt.Errorf("publish acquired GitHub mirror: %w", ErrUnsafeRepository)
	}
	if err = e.fs.syncDirectory(mirrorRoot); err != nil {
		return destination, err
	}
	return destination, nil
}

func requestGitHubURL(r *GitRunner, remote githubRemote) string {
	if r.testOrigin == "" {
		return remote.url
	}
	return r.testOrigin + remote.urlPath
}

func canonicalGitHubRepositoryID(value string) bool {
	if value == "" || len(value) > 32 || value[0] == '0' {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (r *GitRunner) validateGitHubMirror(ctx context.Context, path string) error {
	repository, err := r.ValidateGitRepository(ctx, path)
	if err != nil {
		return fmt.Errorf("%w: Git repository validation", ErrUnsafeRepository)
	}
	if !repository.Bare {
		return fmt.Errorf("%w: repository is not bare", ErrUnsafeRepository)
	}
	config := filepath.Join(repository.GitDir, "config")
	out, err := r.command(ctx, "", gitOffline, nil, "config", "--file", config, "--no-includes", "--null", "--list")
	if err != nil {
		return fmt.Errorf("%w: mirror configuration could not be read", ErrUnsafeRepository)
	}
	var origin, fetch string
	originCount, fetchCount := 0, 0
	for _, record := range strings.Split(string(out.stdout), "\x00") {
		if record == "" {
			continue
		}
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			return fmt.Errorf("%w: malformed mirror configuration", ErrUnsafeRepository)
		}
		switch strings.ToLower(key) {
		case "core.repositoryformatversion":
			if value != "0" {
				return fmt.Errorf("%w: unsupported mirror repository format", ErrUnsafeRepository)
			}
		case "core.bare":
			if value != "true" {
				return fmt.Errorf("%w: mirror is not marked bare", ErrUnsafeRepository)
			}
		case "core.filemode", "core.logallrefupdates":
			if value != "true" && value != "false" {
				return fmt.Errorf("%w: invalid core mirror option", ErrUnsafeRepository)
			}
		case "remote.origin.url":
			origin, originCount = value, originCount+1
		case "remote.origin.fetch":
			fetch, fetchCount = value, fetchCount+1
		case "remote.origin.mirror":
			if value != "true" {
				return fmt.Errorf("%w: origin is not configured as a mirror", ErrUnsafeRepository)
			}
		case "remote.origin.tagopt":
			if value != "--no-tags" {
				return fmt.Errorf("%w: unexpected mirror tag policy", ErrUnsafeRepository)
			}
		default:
			return fmt.Errorf("%w: unexpected mirror configuration key", ErrUnsafeRepository)
		}
	}
	if originCount != 1 || fetchCount != 1 || fetch != "+refs/*:refs/*" || !r.validGitHubOrigin(origin) {
		return fmt.Errorf("%w: mirror origin or refspec mismatch", ErrUnsafeRepository)
	}
	return nil
}

func (r *GitRunner) validGitHubOrigin(raw string) bool {
	if _, err := validatedGitHubRemote(raw); err == nil {
		return true
	}
	if r.testOrigin == "" {
		return false
	}
	base, baseErr := url.Parse(r.testOrigin)
	u, err := url.Parse(raw)
	return baseErr == nil && err == nil && u.Scheme == "https" && u.Host == base.Host && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawPath == "" && u.EscapedPath() == u.Path && validGitHubPath(raw)
}

func validGitHubPath(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := u.Path
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return len(parts) == 2 && !strings.Contains(path, "%") && validGitHubPathComponent(parts[0], true) && strings.HasSuffix(parts[1], ".git") && validGitHubPathComponent(strings.TrimSuffix(parts[1], ".git"), false)
}

func (r *GitRunner) configureGitHubOrigin(ctx context.Context, mirror, expected string) error {
	if err := r.validateGitHubMirror(ctx, mirror); err != nil {
		return err
	}
	repository, err := r.ValidateGitRepository(ctx, mirror)
	if err != nil {
		return err
	}
	config := filepath.Join(repository.GitDir, "config")
	check, err := r.command(ctx, "", gitOffline, nil, "config", "--file", config, "--no-includes", "--get", "remote.origin.url")
	if err != nil {
		return ErrUnsafeRepository
	}
	current := strings.TrimSuffix(string(check.stdout), "\n")
	if current == expected {
		return nil
	}
	if _, err = r.command(ctx, "", gitOffline, nil, "config", "--file", config, "--no-includes", "remote.origin.url", expected); err != nil {
		return ErrUnsafeRepository
	}
	return r.validateGitHubMirror(ctx, mirror)
}

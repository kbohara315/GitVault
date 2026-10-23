CREATE TABLE github_accounts(
  provider TEXT NOT NULL CHECK(provider='github'),
  account_id TEXT NOT NULL CHECK(length(account_id) BETWEEN 1 AND 32),
  login TEXT NOT NULL CHECK(length(login) BETWEEN 1 AND 256),
  account_type TEXT NOT NULL CHECK(account_type IN('User','Organization')),
  inventory_complete INTEGER NOT NULL CHECK(inventory_complete IN(0,1)),
  attempted_at TEXT NOT NULL,
  PRIMARY KEY(provider,account_id)
);
CREATE TABLE github_repositories(
  provider TEXT NOT NULL,
  account_id TEXT NOT NULL,
  repository_id TEXT NOT NULL CHECK(length(repository_id) BETWEEN 1 AND 32),
  owner_id TEXT NOT NULL CHECK(length(owner_id) BETWEEN 1 AND 32),
  owner_login TEXT NOT NULL CHECK(length(owner_login) BETWEEN 1 AND 256),
  owner_type TEXT NOT NULL CHECK(owner_type IN('User','Organization')),
  name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 256),
  full_name TEXT NOT NULL CHECK(length(full_name) BETWEEN 1 AND 513),
  clone_url TEXT NOT NULL CHECK(length(clone_url) BETWEEN 1 AND 2048),
  private INTEGER NOT NULL CHECK(private IN(0,1)),
  fork INTEGER NOT NULL CHECK(fork IN(0,1)),
  archived INTEGER NOT NULL CHECK(archived IN(0,1)),
  description TEXT NOT NULL CHECK(length(description)<=4096),
  availability TEXT NOT NULL CHECK(availability IN('available','inaccessible')),
  last_seen_at TEXT NOT NULL,
  PRIMARY KEY(provider,account_id,repository_id),
  FOREIGN KEY(provider,account_id) REFERENCES github_accounts(provider,account_id)
);
CREATE INDEX github_repositories_account_availability
  ON github_repositories(provider,account_id,availability,full_name);

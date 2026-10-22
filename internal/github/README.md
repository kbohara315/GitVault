# Bounded GitHub discovery seam

```go
func DefaultOptions() Options
func New(Options) (*Client, error)
func (*Client) Discover(context.Context, string) (Result, error)
func (Filter) Match(Repository, User) bool
```

`Discover` takes an explicit token for one operation. It does not persist or log it. Production composition uses the default HTTPS origin `https://api.github.com`; tests supply a `httptest.NewTLSServer` origin and its trusted HTTP client. Never expose origin/transport overrides as ordinary user configuration. Provided clients are copied, their redirect policy is replaced with a same-origin HTTPS policy, and a known insecure TLS transport is rejected. Arbitrary custom round-trippers are trusted injection seams.

`Result` contains typed `User`, `Repositories []Repository`, and `Complete bool`. IDs are GitHub numeric stable IDs (`int64`), never display names. Repository metadata includes owner, name/full name, clean GitHub HTTPS clone URL, private/fork/archive flags and description. No field represents backup selection. The coordinator owns explicit stable-ID selection and must not erase saved selections on incomplete results. On mid-page or subsequent-page failure, validated preceding metadata is returned with `Complete=false` and an error. Duplicates keep the first validated representation of a stable ID; rename reconciliation belongs to a completed refresh.

Identity uses `GET /user`; inventory uses every page of `GET /user/repos?affiliation=owner,collaborator,organization_member&per_page=100&sort=full_name`. Filters only change display: owned means matching authenticated stable owner ID; organization means owner type `Organization`; collaborating means other user-owned accessible repositories, not a separately verified direct collaborator relationship. These categories combine by union; fork exclusion is independent. The zero filter displays all accessible repositories. Stars are not fetched or inferred: starring grants neither access nor backup permission.

Options capture finite repository/page/body/attempt/request-time/rate-wait/backoff budgets plus clock and cancellation-aware sleep seams. Defaults: 10,000 repositories, 1,000 pages, 16 MiB response, three attempts, 60-second attempts, 15-minute total rate wait, jittered 1–30-second retry backoff. Link headers have an additional 64 KiB cap. Next links must be syntactically valid, unambiguous, same-origin HTTPS `/user/repos` links; relative links resolve against the current page. Cycles and pagination exhaustion are incomplete errors. Responses and failed-response bodies are bounded and closed. Transport and response diagnostics never include raw error strings, URLs, token or response body.

`*Error` exposes `Kind` and HTTP `Status` (when available): authentication (401), permission/organization policy (403), not-found (404), rate-limit (429 or signalled 403), transport, invalid-response or resource-limit. Organization restriction specifics cannot be safely inferred from arbitrary response messages. Cancellation retains standard context errors. Only transient transport/server and signalled rate-limit responses retry; rate waits share one operation budget. No live GitHub access, Git transfer, selection persistence or UI wiring is included.

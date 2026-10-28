package imageupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/distribution/reference"
)

// maxTagPages caps how many pages of a tag list are followed (1000 tags
// each) - enough for any real repository, while a misbehaving registry
// that keeps returning a "next" link can't keep a request running forever.
const maxTagPages = 20

// Credential is a registry login; the zero value means anonymous access.
type Credential struct {
	Username string
	Password string
}

// Ref is an image reference split into what a registry lookup needs.
type Ref struct {
	// Repository is the reference without tag or digest, as the user wrote
	// it (nginx, ghcr.io/org/app) - a new tag is appended to this.
	Repository string
	// Domain is the registry host as Docker names it ("docker.io" for
	// Docker Hub), Path the repository path on it ("library/nginx").
	Domain string
	Path   string
	Tag    string
}

// WithTag returns the image reference for another tag of the same
// repository.
func (r Ref) WithTag(tag string) string {
	return r.Repository + ":" + tag
}

// ParseRef parses an image reference. A digest pin (as Swarm adds to every
// service's image, e.g. nginx:1.27.1@sha256:...) is ignored; a reference
// with no tag at all is an error, since there's no version to upgrade.
func ParseRef(image string) (Ref, error) {
	if i := strings.IndexByte(image, '@'); i != -1 {
		image = image[:i]
	}
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return Ref{}, err
	}
	tagged, ok := named.(reference.Tagged)
	if !ok {
		return Ref{}, errors.New("image has no tag")
	}
	repo := image
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		repo = image[:i]
	}
	return Ref{
		Repository: repo,
		Domain:     reference.Domain(named),
		Path:       reference.Path(named),
		Tag:        tagged.Tag(),
	}, nil
}

// Client lists tags through the Docker Registry HTTP API v2, handling both
// token (Docker Hub, GHCR, GitLab, Harbor...) and basic auth.
type Client struct {
	HTTP *http.Client
}

// DefaultClient is used when no client is configured explicitly.
var DefaultClient = &Client{HTTP: &http.Client{Timeout: 30 * time.Second}}

// apiHost maps Docker's name for a registry to the host serving its API -
// they only differ for Docker Hub.
func apiHost(domain string) string {
	if domain == "docker.io" {
		return "registry-1.docker.io"
	}
	return domain
}

// ListTags returns every tag of ref's repository.
func (c *Client) ListTags(ctx context.Context, ref Ref, cred Credential) ([]string, error) {
	next := fmt.Sprintf("https://%s/v2/%s/tags/list?n=1000", apiHost(ref.Domain), ref.Path)
	var (
		tags []string
		auth string
	)
	for page := 0; next != "" && page < maxTagPages; page++ {
		resp, err := c.get(ctx, next, auth)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && auth == "" {
			challenge := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			if auth, err = c.authorize(ctx, challenge, ref.Domain, "repository:"+ref.Path+":pull", cred); err != nil {
				return nil, err
			}
			if resp, err = c.get(ctx, next, auth); err != nil {
				return nil, err
			}
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = decodeOK(resp, &body)
		if err != nil {
			return nil, fmt.Errorf("list tags of %s: %w", ref.Repository, err)
		}
		tags = append(tags, body.Tags...)
		next, err = nextPage(next, resp.Header.Get("Link"))
		if err != nil {
			return nil, err
		}
	}
	return tags, nil
}

func (c *Client) get(ctx context.Context, u, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return c.HTTP.Do(req)
}

// CheckLogin verifies cred against the registry at domain (as Docker names
// it, e.g. "docker.io" or "ghcr.io"), the way `docker login` does: by
// authenticating to the API's /v2/ endpoint.
func (c *Client) CheckLogin(ctx context.Context, domain string, cred Credential) error {
	u := fmt.Sprintf("https://%s/v2/", apiHost(domain))
	resp, err := c.get(ctx, u, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil // the registry doesn't require a login at all
	case http.StatusUnauthorized:
	default:
		return fmt.Errorf("registry %s: %s", domain, resp.Status)
	}
	auth, err := c.authorize(ctx, resp.Header.Get("WWW-Authenticate"), domain, "", cred)
	if err != nil {
		return err
	}
	resp, err = c.get(ctx, u, auth)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry %s rejected the login: %s", domain, resp.Status)
	}
	return nil
}

// authorize answers a 401's WWW-Authenticate challenge with the
// Authorization header to retry with: basic auth as-is, or a bearer token
// fetched from the challenge's realm (anonymously when cred is empty -
// public Docker Hub/GHCR repositories still require a token). scope is
// requested when the challenge doesn't name one; "" requests none.
func (c *Client) authorize(ctx context.Context, challenge, domain, scope string, cred Credential) (string, error) {
	scheme, params := parseChallenge(challenge)
	switch strings.ToLower(scheme) {
	case "basic":
		if cred.Username == "" {
			return "", fmt.Errorf("registry %s requires a login (add it under Settings -> Registries)", domain)
		}
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.SetBasicAuth(cred.Username, cred.Password)
		return req.Header.Get("Authorization"), nil
	case "bearer":
	default:
		return "", fmt.Errorf("registry %s: unsupported auth challenge %q", domain, challenge)
	}

	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme == "" {
		return "", fmt.Errorf("registry %s: bad token realm %q", domain, params["realm"])
	}
	q := realm.Query()
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	if params["scope"] != "" {
		scope = params["scope"]
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	realm.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	if cred.Username != "" {
		req.SetBasicAuth(cred.Username, cred.Password)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := decodeOK(resp, &tok); err != nil {
		return "", fmt.Errorf("registry %s: get token: %w", domain, err)
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	return "Bearer " + tok.Token, nil
}

func decodeOK(resp *http.Response, v any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// parseChallenge splits `Bearer realm="...",service="...",scope="..."`.
func parseChallenge(h string) (scheme string, params map[string]string) {
	params = map[string]string{}
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	for rest != "" {
		var key, val string
		key, rest, _ = strings.Cut(strings.TrimLeft(rest, ", "), "=")
		if strings.HasPrefix(rest, `"`) {
			val, rest, _ = strings.Cut(rest[1:], `"`)
		} else {
			val, rest, _ = strings.Cut(rest, ",")
		}
		params[strings.ToLower(strings.TrimSpace(key))] = val
	}
	return scheme, params
}

// nextPage resolves an RFC 5988 `Link: </v2/...?last=x>; rel="next"`
// header against the current page's URL, or returns "" when there isn't
// one.
func nextPage(current, link string) (string, error) {
	if link == "" || !strings.Contains(link, `rel="next"`) {
		return "", nil
	}
	start, end := strings.IndexByte(link, '<'), strings.IndexByte(link, '>')
	if start == -1 || end < start {
		return "", nil
	}
	base, err := url.Parse(current)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(link[start+1 : end])
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

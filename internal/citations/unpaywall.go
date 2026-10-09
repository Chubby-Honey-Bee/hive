package citations

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/useragent"
)

// unpaywallEndpoint is the public Unpaywall v2 API.
const unpaywallEndpoint = "https://api.unpaywall.org/v2"

// EmailEnv names the variable that holds the contact address Unpaywall
// requires on every request, as its `email` query parameter. Unpaywall uses
// it to reach the caller about its traffic, not to authenticate. HIVE ships
// no default: each install sends an address its user reads.
const EmailEnv = "HIVE_UNPAYWALL_EMAIL"

// ErrNoContact is why a verifier with no contact address sends no request.
var ErrNoContact = errors.New("no contact address, which Unpaywall requires on every request: set " + EmailEnv + " to one you read")

// VerifyResult is what every verifier (Unpaywall today, OpenAlex /
// DOAJ as future fallbacks) collapses into. Only IsOA is consulted
// by the MSS no-laundering audit; the rest are surfaced for context.
type VerifyResult struct {
	DOI        string `json:"doi"`
	IsOA       bool   `json:"is_oa"`
	OAURL      string `json:"oa_url,omitempty"`
	License    string `json:"license,omitempty"`
	HostType   string `json:"host_type,omitempty"` // "publisher" | "repository"
	Title      string `json:"title,omitempty"`
	Year       int    `json:"year,omitempty"`
	Source     string `json:"source"` // "unpaywall" | "openalex" | "cache"
	VerifiedAt string `json:"verified_at"`
	Error      string `json:"error,omitempty"`
}

// HTTPVerifier hits api.unpaywall.org over HTTPS. Reuses an
// http.Client with a sane timeout so a slow API doesn't stall a
// research run. Stateless — safe to share across goroutines.
type HTTPVerifier struct {
	Client   *http.Client
	Email    string
	Endpoint string
}

// NewHTTPVerifier constructs a verifier with a 10s timeout and the contact
// address EmailEnv names; Email is empty when it names none.
func NewHTTPVerifier() *HTTPVerifier {
	return &HTTPVerifier{
		Client:   &http.Client{Timeout: 10 * time.Second},
		Email:    os.Getenv(EmailEnv),
		Endpoint: unpaywallEndpoint,
	}
}

// Verify resolves the DOI against Unpaywall. Returns a result with
// IsOA=false + Error populated on lookup miss / rate-limit / network
// error, never a hard error — the caller treats "couldn't verify" as
// "not OA" for MSS purposes (the conservative default).
func (v *HTTPVerifier) Verify(ctx context.Context, doi string) (*VerifyResult, error) {
	doi = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(doi, "https://doi.org/"), "http://doi.org/"))
	if doi == "" {
		return &VerifyResult{Source: "unpaywall", Error: "empty doi"}, nil
	}
	status, body, err := v.fetch(ctx, doi)
	if err != nil {
		return &VerifyResult{DOI: doi, Source: "unpaywall", Error: err.Error()}, nil
	}
	return resultOf(doi, status, body), nil
}

// fetch GETs doi's Unpaywall record and returns the response's status and
// up to 1 MiB of its body. The error is a request that could not be built
// or sent, or ErrNoContact when the verifier has no contact address.
func (v *HTTPVerifier) fetch(ctx context.Context, doi string) (int, []byte, error) {
	if v.Email == "" {
		return 0, nil, ErrNoContact
	}
	endpoint := cmp.Or(v.Endpoint, unpaywallEndpoint)
	u := fmt.Sprintf("%s/%s?email=%s", endpoint, url.PathEscape(doi), url.QueryEscape(v.Email))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", useragent.For("verify-citations"))

	resp, err := v.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	return resp.StatusCode, body, nil
}

// resultOf is the result an Unpaywall response of status and body gives for
// doi. A miss and an HTTP error are results with Error set.
func resultOf(doi string, status int, body []byte) *VerifyResult {
	now := time.Now().UTC().Format(time.RFC3339)
	if status == http.StatusNotFound {
		return &VerifyResult{DOI: doi, Source: "unpaywall", VerifiedAt: now, Error: "not found in Unpaywall"}
	}
	if status >= 400 {
		return &VerifyResult{DOI: doi, Source: "unpaywall", VerifiedAt: now,
			Error: fmt.Sprintf("HTTP %d", status)}
	}
	return decodeRecord(doi, now, body)
}

// unpaywallRecord is the subset of an Unpaywall response we care about:
//
//	{ is_oa: bool, best_oa_location: { url_for_pdf, license, host_type },
//	  title, year, ... }
type unpaywallRecord struct {
	IsOA           bool        `json:"is_oa"`
	Title          string      `json:"title"`
	Year           int         `json:"year"`
	BestOALocation *oaLocation `json:"best_oa_location"`
}

// oaLocation is where a record's best open-access copy lives.
type oaLocation struct {
	URLForPDF string `json:"url_for_pdf"`
	URL       string `json:"url"`
	License   string `json:"license"`
	HostType  string `json:"host_type"`
}

// decodeRecord is the result body, an Unpaywall record of doi verified at
// now, gives; a body that does not decode is a result with Error set.
func decodeRecord(doi, now string, body []byte) *VerifyResult {
	var raw unpaywallRecord
	if err := json.Unmarshal(body, &raw); err != nil {
		return &VerifyResult{DOI: doi, Source: "unpaywall", VerifiedAt: now,
			Error: "decode unpaywall response: " + err.Error()}
	}
	res := &VerifyResult{
		DOI:        doi,
		IsOA:       raw.IsOA,
		Title:      raw.Title,
		Year:       raw.Year,
		Source:     "unpaywall",
		VerifiedAt: now,
	}
	if loc := raw.BestOALocation; loc != nil {
		res.OAURL = cmp.Or(loc.URLForPDF, loc.URL)
		res.License = loc.License
		res.HostType = loc.HostType
	}
	return res
}

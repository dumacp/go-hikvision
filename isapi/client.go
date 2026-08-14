// Package isapi talks to Hikvision cameras over ISAPI (HTTP + XML with Digest auth).
//
// It is transport only: no actors, no logging policy, no retries. The caller decides
// how often to call and what to do with an error, which is what lets the camera actor
// keep its retry and rate limiting rules in one place.
package isapi

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

// ErrUnauthorized is returned when the device rejects the credentials.
//
// Treat it as terminal: ISAPI_general.pdf §3.1 states the device returns the remaining
// attempts and LOCKS the user when they run out. Retrying a wrong password in a loop
// leaves the camera unreachable in the field.
var ErrUnauthorized = errors.New("isapi: credentials rejected")

// maxBody caps the response we read. Camera answers are small XML documents; the cap
// is there so a misdirected request cannot exhaust the gateway's memory.
const maxBody = 1 << 20

// DefaultTimeout applies when New gets a non positive timeout. Explicit and finite on
// purpose: the keep alive in ping-actor.go uses http.Get with no timeout at all.
const DefaultTimeout = 10 * time.Second

// Client is a camera endpoint. Safe for sequential use by one actor; it keeps a
// request counter for Digest and does not lock it.
type Client struct {
	host string
	user string
	pass string
	hc   *http.Client
	nc   uint32
}

// New builds a client for host, which may be "ip" or "ip:port".
func New(host, user, pass string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		host: host,
		user: user,
		pass: pass,
		hc:   &http.Client{Timeout: timeout},
	}
}

// Host returns the address this client talks to, for logging.
func (c *Client) Host() string { return c.host }

func (c *Client) send(ctx context.Context, method, path string, body []byte, auth string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.host+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", `application/xml; charset="UTF-8"`)
	}
	if len(auth) > 0 {
		req.Header.Set("Authorization", auth)
	}
	return c.hc.Do(req)
}

// do performs one ISAPI call: the device answers 401 with a Digest challenge, and the
// request is replayed once with the computed Authorization header.
//
// It returns the response body. A body that is a ResponseStatus reporting failure is
// returned as an error even when the HTTP status is 200, which does happen.
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	resp, err := c.send(ctx, method, path, body, "")
	if err != nil {
		return nil, fmt.Errorf("isapi %s %s: %w", method, path, err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		header := resp.Header.Get("WWW-Authenticate")
		// Drain before reusing the connection.
		io.Copy(ioutil.Discard, io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()

		ch, err := parseChallenge(header)
		if err != nil {
			return nil, fmt.Errorf("isapi %s %s: %w", method, path, err)
		}
		c.nc++
		auth, err := ch.authorization(c.user, c.pass, method, path, c.nc)
		if err != nil {
			return nil, fmt.Errorf("isapi %s %s: %w", method, path, err)
		}
		if resp, err = c.send(ctx, method, path, body, auth); err != nil {
			return nil, fmt.Errorf("isapi %s %s: %w", method, path, err)
		}
		// Only one retry, on purpose: see ErrUnauthorized.
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return nil, fmt.Errorf("isapi %s %s: %w", method, path, ErrUnauthorized)
		}
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("isapi %s %s: reading body: %w", method, path, err)
	}

	if status, ok := parseResponseStatus(data); ok && !status.OK() {
		return data, fmt.Errorf("isapi %s %s: %w", method, path, status)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return data, fmt.Errorf("isapi %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return data, nil
}

// get retrieves path and unmarshals the XML body into out.
func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("isapi GET %s: parsing response: %w", path, err)
	}
	return nil
}

// put marshals in as XML and sends it to path.
func (c *Client) put(ctx context.Context, path string, in interface{}) error {
	body, err := xml.Marshal(in)
	if err != nil {
		return fmt.Errorf("isapi PUT %s: building request: %w", path, err)
	}
	_, err = c.do(ctx, http.MethodPut, path, append([]byte(xml.Header), body...))
	return err
}

// post sends in and unmarshals the answer into out. out may be nil.
func (c *Client) post(ctx context.Context, path string, in, out interface{}) error {
	body, err := xml.Marshal(in)
	if err != nil {
		return fmt.Errorf("isapi POST %s: building request: %w", path, err)
	}
	data, err := c.do(ctx, http.MethodPost, path, append([]byte(xml.Header), body...))
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := xml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("isapi POST %s: parsing response: %w", path, err)
	}
	return nil
}

// parseResponseStatus reports whether data is a ResponseStatus document, and decodes it.
func parseResponseStatus(data []byte) (*ResponseStatus, bool) {
	if !bytes.Contains(data, []byte("<ResponseStatus")) {
		return nil, false
	}
	status := new(ResponseStatus)
	if err := xml.Unmarshal(data, status); err != nil {
		return nil, false
	}
	return status, true
}

// tolerantTime parses a device timestamp.
//
// GET /ISAPI/System/time answers a well formed offset ("-05:00"), but the event push
// of the same camera sends it without the leading zero ("-5:00"), which time.Parse
// rejects. Both are accepted here so a firmware quirk cannot break a time check.
// The event path has its own normalisation in client/listen-actor.go.
func tolerantTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	// Insert the missing zero in a "-5:00" style offset.
	if i := strings.LastIndexAny(value, "+-"); i > 0 && len(value)-i == 5 {
		fixed := value[:i+1] + "0" + value[i+1:]
		if t, err := time.Parse(time.RFC3339, fixed); err == nil {
			return t, nil
		}
	}
	// Some devices answer local time with no offset at all.
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", value, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse device time %q", value)
}

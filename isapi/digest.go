package isapi

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// challenge holds a parsed "WWW-Authenticate: Digest ..." header.
//
// The camera answers 401 with this challenge on the first request of every call, as
// described in ISAPI_general.pdf §3.1. Only MD5 with qop="auth" (or no qop) is
// implemented, which is what the verified firmware offers:
//
//	Digest qop="auth", realm="IP Camera(K9754)", nonce="4e57...", stale="FALSE"
type challenge struct {
	realm     string
	nonce     string
	qop       string
	opaque    string
	algorithm string
}

func parseChallenge(header string) (*challenge, error) {
	const prefix = "Digest "
	i := strings.Index(header, prefix)
	if i < 0 {
		// Basic-only devices exist; say so instead of failing opaquely.
		return nil, fmt.Errorf("no Digest challenge in %q", header)
	}

	c := new(challenge)
	for _, field := range splitChallenge(header[i+len(prefix):]) {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
		switch key {
		case "realm":
			c.realm = value
		case "nonce":
			c.nonce = value
		case "qop":
			c.qop = value
		case "opaque":
			c.opaque = value
		case "algorithm":
			c.algorithm = value
		}
	}
	if len(c.nonce) == 0 {
		return nil, fmt.Errorf("Digest challenge without nonce: %q", header)
	}
	if alg := strings.ToLower(c.algorithm); len(alg) > 0 && alg != "md5" {
		return nil, fmt.Errorf("unsupported Digest algorithm %q", c.algorithm)
	}
	return c, nil
}

// splitChallenge splits on commas that are not inside a quoted value.
func splitChallenge(s string) []string {
	var out []string
	var buf strings.Builder
	inQuotes := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			buf.WriteRune(r)
		case r == ',' && !inQuotes:
			out = append(out, buf.String())
			buf.Reset()
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

func md5hex(parts ...string) string {
	sum := md5.Sum([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(sum[:])
}

// authorization builds the Authorization header value for one request.
//
//	A1 = user:realm:password
//	A2 = method:uri
//	qop=auth  -> response = MD5(MD5(A1):nonce:nc:cnonce:qop:MD5(A2))
//	no qop    -> response = MD5(MD5(A1):nonce:MD5(A2))
func (c *challenge) authorization(user, pass, method, uri string, nc uint32) (string, error) {
	ha1 := md5hex(user, c.realm, pass)
	ha2 := md5hex(method, uri)

	fields := []string{
		fmt.Sprintf(`username=%q`, user),
		fmt.Sprintf(`realm=%q`, c.realm),
		fmt.Sprintf(`nonce=%q`, c.nonce),
		fmt.Sprintf(`uri=%q`, uri),
	}

	var response string
	if qop := pickQop(c.qop); qop != "" {
		cnonce, err := newCnonce()
		if err != nil {
			return "", err
		}
		ncValue := fmt.Sprintf("%08x", nc)
		response = md5hex(ha1, c.nonce, ncValue, cnonce, qop, ha2)
		fields = append(fields,
			fmt.Sprintf(`cnonce=%q`, cnonce),
			// nc goes unquoted, per RFC 2617.
			"nc="+ncValue,
			fmt.Sprintf(`qop=%s`, qop),
		)
	} else {
		response = md5hex(ha1, c.nonce, ha2)
	}

	fields = append(fields, fmt.Sprintf(`response=%q`, response))
	if len(c.opaque) > 0 {
		fields = append(fields, fmt.Sprintf(`opaque=%q`, c.opaque))
	}
	if len(c.algorithm) > 0 {
		fields = append(fields, fmt.Sprintf(`algorithm=%s`, c.algorithm))
	}
	return "Digest " + strings.Join(fields, ", "), nil
}

// pickQop takes "auth" out of a list like "auth,auth-int". auth-int would require
// hashing the body into A2 and no verified device asks for it.
func pickQop(qop string) string {
	for _, v := range strings.Split(qop, ",") {
		if strings.TrimSpace(v) == "auth" {
			return "auth"
		}
	}
	return ""
}

func newCnonce() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

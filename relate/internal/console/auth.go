package console

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
)

// sessionCookieName is the cookie the browser session is authenticated
// with after the initial token handshake (see requireAuth).
const sessionCookieName = "symrelate_session"

// GenerateToken returns a fresh random session token, hex-encoded. Called
// once per `symrelate console` process start — see internal/cli/console_cmd.go.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		// subtle.ConstantTimeCompare requires equal-length inputs;
		// mismatched lengths are already a safe-to-short-circuit no-match
		// (no secret-dependent branching on the actual token value).
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// requireAuth gates every request behind the session token: as a cookie
// (set once the browser presents the token via ?token=, see below) or an
// Authorization: Bearer header (for programmatic/API callers). This is
// the "authenticated local session" the console's acceptance criteria
// require — the console never serves a page or API response to a request
// that cannot prove it holds the token printed at startup.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(sessionCookieName); err == nil && constantTimeEqual(cookie.Value, s.token) {
			next.ServeHTTP(w, r)
			return
		}
		if bearer, ok := bearerToken(r); ok && constantTimeEqual(bearer, s.token) {
			next.ServeHTTP(w, r)
			return
		}

		// Bootstrap: a GET carrying ?token=<token> establishes the
		// session cookie and redirects to the same URL with the token
		// stripped, so the token never lingers in browser history past
		// the very first load.
		if r.Method == http.MethodGet {
			if qToken := r.URL.Query().Get("token"); qToken != "" && constantTimeEqual(qToken, s.token) {
				http.SetCookie(w, &http.Cookie{
					Name:     sessionCookieName,
					Value:    s.token,
					Path:     "/",
					HttpOnly: true,
					// Browsers treat 127.0.0.1/localhost as a secure context,
					// so the Secure attribute is honored even over plain HTTP
					// there — and required everywhere else.
					Secure:   true,
					SameSite: http.SameSiteStrictMode,
				})
				redirectURL := *r.URL
				q := redirectURL.Query()
				q.Del("token")
				redirectURL.RawQuery = q.Encode()
				// The target derives from the request URL; only redirect when
				// it is a local path (no host), so a crafted request target
				// like "//evil.example/x" cannot become an off-site redirect.
				target, err := url.Parse(strings.ReplaceAll(redirectURL.RequestURI(), "\\", "/"))
				if err != nil || target.Hostname() != "" {
					writeJSONError(w, http.StatusBadRequest, "invalid redirect target")
					return
				}
				// Redirect to the *validated* target (not the raw request
				// URI), so the check above and the redirect below always
				// operate on the same value.
				http.Redirect(w, r, target.String(), http.StatusFound)
				return
			}
		}

		writeJSONError(w, http.StatusUnauthorized, "unauthorized: missing or invalid session")
	})
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return "", false
	}
	return h[len(prefix):], true
}

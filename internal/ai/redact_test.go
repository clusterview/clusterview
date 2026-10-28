package ai

import (
	"strings"
	"testing"
)

func TestRedactRemovesSecrets(t *testing.T) {
	secrets := map[string]string{
		"password in config":  `db password=hunter2hunter2 host=db`,
		"json token":          `{"access_token": "abcdEFGH1234ijkl", "user": "ann"}`,
		"env style":           `STRIPE_SECRET_KEY: sk_live_abc123def456`,
		"bearer header":       `Authorization: Bearer abc.def-ghi_jkl123456`,
		"url credentials":     `postgres://app:s3cretPass@orders-db:5432/orders`,
		"jwt":                 `cookie eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`,
		"aws key":             `using AKIAIOSFODNN7EXAMPLE for s3`,
		"openai key":          `key sk-proj-abcdefghijklmnopqrstuvwx`,
		"github token":        `ghp_abcdefghijklmnopqrstuvwxyz0123456789`,
		"private key":         "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----",
		"random base64 blob":  `X-Signature-ish Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MEFCQ0RFRkdISUpL`,
		"long hex":            `api key value 4f2a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a`,
		"quoted yaml":         `  apiKey: "AbC123xyz"`,
		"query parameter":     `GET /callback?code=1&client_secret=xyz789abc`,
		"connection string":   `DATABASE_DSN=host=db user=app`,
		"session cookie pair": `session=8f1b2c3d4e5f`,
	}
	for name, text := range secrets {
		t.Run(name, func(t *testing.T) {
			got, n := Redact(text)
			if n == 0 || !strings.Contains(got, "REDACTED") {
				t.Fatalf("nothing removed from %q: %q", text, got)
			}
			for _, leak := range []string{"hunter2", "abcdEFGH1234", "sk_live", "s3cretPass", "AKIAIOSFODNN7", "ghp_", "MIIEow", "Zm9vYmFy", "4f2a9b8c", "AbC123", "xyz789", "8f1b2c3d"} {
				if strings.Contains(got, leak) {
					t.Errorf("%q still contains %q: %q", text, leak, got)
				}
			}
		})
	}
}

func TestRedactKeepsEvidence(t *testing.T) {
	keep := []string{
		`Error: connect ECONNREFUSED 10.96.12.4:5432`,
		`Unauthorized: invalid api key`,
		`FATAL: password authentication failed for user "app"`,
		`image registry.example.com/shop/payments@sha256:4f2a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a`,
		`Back-off restarting failed container app in pod payments-api-7c9f6d8b5-x2x4q_shop(1b4e0f5a-2c3d-4e5f-8a9b-0c1d2e3f4a5b)`,
		`OOMKilled: container exceeded its memory limit of 256Mi`,
		`/usr/local/lib/python3.13/site-packages/payments/provider.py line 88`,
		`Readiness probe failed: HTTP probe failed with statuscode: 503`,
		`configmap "payments-config" not found`,
		`BackOff on Pod/report-builder-86f55df5f5-j4fht, 3×`,
		`kube-api-access-xqwc4 at /var/run/secrets/kubernetes.io/serviceaccount`,
	}
	for _, text := range keep {
		if got, n := Redact(text); got != text || n != 0 {
			t.Errorf("Redact(%q) = %q, %d; want it unchanged", text, got, n)
		}
	}
}

package redact

import (
	"strings"
	"testing"
)

const rsaFixture = "-----BEGIN RSA PRIVATE KEY-----\n" +
	"MIIEowIBAAKCAQEA0FixtureLineOneThatMustNotLeak\n" +
	"c2VjcmV0Rml4dHVyZUJvZHlMaW5lVHdvTXVzdE5vdExlYWs=\n" +
	"-----END RSA PRIVATE KEY-----"

func TestPrivateKeyBlockIsFullyRedacted(t *testing.T) {
	input := "before\n" + rsaFixture + "\nafter"
	out := Text(input)
	for _, leaked := range []string{"BEGIN RSA", "MIIEow", "c2VjcmV0Rml4dHVyZQ", "END RSA"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("private key material %q leaked: %q", leaked, out)
		}
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Fatalf("surrounding text lost: %q", out)
	}
	findings := Findings(input)
	if len(findings) != 1 || findings[0] != "PRIVATE_KEY" {
		t.Fatalf("unexpected findings: %#v", findings)
	}
}

func TestOpenSSHAndPGPKeysAreRedacted(t *testing.T) {
	fixtures := []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABodyMustNotLeak\n-----END OPENSSH PRIVATE KEY-----",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQOYBGFixtureBodyMustNotLeak\n-----END PGP PRIVATE KEY BLOCK-----",
	}
	for _, input := range fixtures {
		out := Text(input)
		if strings.Contains(out, "b3BlbnNzaC1rZXktdjE") || strings.Contains(out, "lQOYBG") {
			t.Fatalf("key body leaked: %q", out)
		}
		if !contains(Findings(input), "PRIVATE_KEY") {
			t.Fatalf("missing PRIVATE_KEY finding: %#v", Findings(input))
		}
	}
}

func TestTruncatedPrivateKeyHeaderRedactsRemainder(t *testing.T) {
	input := "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIFixtureBodyThatMustNotLeak\nrest"
	out := Text(input)
	if strings.Contains(out, "MHcCAQEE") || strings.Contains(out, "rest") {
		t.Fatalf("truncated key leaked: %q", out)
	}
	if !contains(Findings(input), "PRIVATE_KEY") {
		t.Fatalf("missing finding")
	}
}

func TestCommonSecretsAndEmail(t *testing.T) {
	input := "mail alice@example.test\n" +
		"key sk-FAKEEXAMPLE1234\n" +
		"auth Bearer FAKETOKENabcdef\n" +
		"db password=hunter2secret\n" +
		"url https://bob:pw123456@example.test/path?token=abcd1234\n" +
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U\n" +
		"aws AKIAIOSFODNN7EXAMPLE"
	out := Text(input)
	for _, secret := range []string{
		"alice@example.test", "sk-FAKEEXAMPLE1234", "FAKETOKENabcdef", "hunter2secret",
		"bob:pw123456", "abcd1234", "eyJhbGciOiJIUzI1NiJ9", "AKIAIOSFODNN7EXAMPLE",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked in %q", secret, out)
		}
	}
	findings := Findings(input)
	for _, want := range []string{"EMAIL", "API_KEY", "BEARER_TOKEN", "SECRET_ASSIGNMENT", "URL_CREDENTIALS", "URL_QUERY", "JWT", "AWS_ACCESS_KEY"} {
		if !contains(findings, want) {
			t.Fatalf("missing %s in %#v", want, findings)
		}
	}
	if !strings.Contains(out, "password=[REDACTED:SECRET_ASSIGNMENT]") {
		t.Fatalf("assignment not redacted structurally: %q", out)
	}
}

func TestTextIsIdempotentAndFindingsDoNotLeak(t *testing.T) {
	input := "alice@example.test sk-FAKEEXAMPLE1234 " + rsaFixture
	once := Text(input)
	twice := Text(once)
	if once != twice {
		t.Fatalf("not idempotent:\n%q\n%q", once, twice)
	}
	if findings := Findings(once); len(findings) != 0 {
		t.Fatalf("redacted text still reports findings: %#v", findings)
	}
	for _, finding := range Findings(input) {
		if strings.Contains(finding, "alice") || strings.Contains(finding, "FAKE") || strings.Contains(finding, "MIIE") {
			t.Fatalf("finding contains secret material: %q", finding)
		}
	}
}

func TestOrdinaryChineseTextIsPreserved(t *testing.T) {
	input := "先实现桥接，再核对回执。没有密钥。"
	if got := Text(input); got != input {
		t.Fatalf("ordinary text changed: %q", got)
	}
	if findings := Findings(input); len(findings) != 0 {
		t.Fatalf("unexpected findings: %#v", findings)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

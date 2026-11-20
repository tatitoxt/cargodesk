package piiscan

import (
	"os"
	"slices"
	"testing"
)

func rulesOf(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		if !slices.Contains(out, f.Rule) {
			out = append(out, f.Rule)
		}
	}
	slices.Sort(out)
	return out
}

func TestForbiddenFixtureIsDetected(t *testing.T) {
	b, err := os.ReadFile("testdata/forbidden.json")
	if err != nil {
		t.Fatal(err)
	}
	got := rulesOf(Scan("forbidden.json", b))
	want := []string{"email", "password_assignment", "phone", "street_address"}
	if !slices.Equal(got, want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
}

func TestSyntheticFixtureIsClean(t *testing.T) {
	b, err := os.ReadFile("testdata/allowed.json")
	if err != nil {
		t.Fatal(err)
	}
	if fs := Scan("allowed.json", b); len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}
}

// Secret-shaped values are assembled at runtime so the repository itself
// never contains a string that secret scanners would flag.
var (
	fakeAWSKey = "AKIA" + "ABCDEFGHIJKLMNOP"
	fakeGHPat  = "ghp" + "_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	fakeAPIKey = "sk" + "-" + "abcdefghijklmnopqrstuvwx"
)

func TestSecretShapes(t *testing.T) {
	cases := map[string]string{fakeAWSKey: "aws_access_key", fakeGHPat: "github_token", fakeAPIKey: "api_key"}
	for in, want := range cases {
		fs := Scan("s", []byte(`"value": "`+in+`"`))
		if len(fs) != 1 || fs[0].Rule != want {
			t.Errorf("%s: findings = %+v", want, fs)
		}
	}
}

func TestFindingsAreRedactedAndLocated(t *testing.T) {
	fs := Scan("x", []byte("ok\nkey="+fakeAWSKey+"\n"))
	if len(fs) != 1 || fs[0].Line != 2 {
		t.Fatalf("findings = %+v", fs)
	}
	if fs[0].Match == fakeAWSKey {
		t.Fatal("secret echoed unredacted")
	}
}

func TestPrivateKeyHeader(t *testing.T) {
	fs := Scan("k", []byte("-----BEGIN RSA PRIVATE KEY-----"))
	if len(fs) != 1 || fs[0].Rule != "private_key" {
		t.Fatalf("findings = %+v", fs)
	}
}

func TestUUIDsAreNotPhones(t *testing.T) {
	if f := Scan("x", []byte(`{"id":"00000000-0000-4000-b000-000000000001"}`)); len(f) != 0 {
		t.Fatalf("findings: %+v", f)
	}
}
